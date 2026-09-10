package review

import (
	"context"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

func comment(file, body string, q findings.Question) findings.ReviewComment {
	return findings.ReviewComment{
		File: file, Line: 1, Body: body,
		Severity: findings.SeverityWarning, Confidence: findings.ConfidenceHigh,
		Question: q,
	}
}

// Only what the ruling keeps reaches the author. Everything else stays on the
// report at low confidence, which is the one rule about posting the report and
// post already share.
func TestApplyDemotesEverythingTheRulingDidNotKeep(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "kept one", findings.Question{Kind: findings.QuestionDiff}),
		comment("b.go", "withdrawn one", findings.Question{Kind: findings.QuestionCaller, Subject: "Foo"}),
		comment("c.go", "justified one", findings.Question{Kind: findings.QuestionPrecedent, Subject: "Bar"}),
		comment("d.go", "unverifiable one", findings.Question{Kind: findings.QuestionRule, Subject: "Baz"}),
	}}
	cands := Candidates(rev)
	got := Apply(rev, cands, map[string]findings.Ruling{
		"c1": {Verdict: findings.VerifiedKept, Evidence: "a.go:1"},
		"c2": {Verdict: findings.VerifiedWithdrawn, Evidence: "nothing calls Foo"},
		"c3": {Verdict: findings.VerifiedJustified, Evidence: "sibling.go does the same"},
		"c4": {Verdict: findings.VerifiedUnverifiable},
	})

	if got.Comments[0].Confidence != findings.ConfidenceHigh {
		t.Fatal("a kept finding keeps the confidence its author gave it")
	}
	for _, i := range []int{1, 2, 3} {
		if got.Comments[i].Confidence != findings.ConfidenceLow {
			t.Fatalf("comment %d was not kept and must not post: %+v", i, got.Comments[i])
		}
	}
	kept, ruled := Kept(got)
	if kept != 1 || ruled != 4 {
		t.Fatalf("kept=%d ruled=%d, want 1 of 4", kept, ruled)
	}
}

// The pass is optional at every point, so a comment nobody ruled on has to
// behave exactly as it did before this existed. Otherwise a checking pass that
// failed halfway would silently empty a review.
func TestAnUnruledCommentIsUntouched(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "nobody ruled on this", findings.Question{Kind: findings.QuestionDiff}),
	}}
	got := Apply(rev, Candidates(rev), map[string]findings.Ruling{})
	if got.Comments[0].Confidence != findings.ConfidenceHigh {
		t.Fatal("an unruled comment was demoted by a pass that did not rule on it")
	}
	if kept, _ := Kept(got); kept != 1 {
		t.Fatal("an unruled comment still posts")
	}
}

// A verdict nobody can read must not be the one that lets a finding through.
func TestAnUnknownVerdictFailsClosed(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "x", findings.Question{Kind: findings.QuestionDiff}),
	}}
	got := Apply(rev, Candidates(rev), map[string]findings.Ruling{
		"c1": {Verdict: "probably fine"},
	})
	if got.Comments[0].Ruling.Verdict != findings.VerifiedUnverifiable {
		t.Fatalf("verdict = %q, want unverifiable", got.Comments[0].Ruling.Verdict)
	}
	if got.Comments[0].Confidence != findings.ConfidenceLow {
		t.Fatal("an unreadable verdict must not post")
	}
}

// Only the findings that need a lookup are sent to one. A finding the diff
// settles, and one nothing can settle, both cost nothing here.
func TestOnlyAnswerableQuestionsAreSentToTheLookups(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "the diff shows it", findings.Question{Kind: findings.QuestionDiff}),
		comment("b.go", "speculation", findings.Question{Kind: findings.QuestionNone}),
		comment("c.go", "needs a look", findings.Question{
			Kind: findings.QuestionPrecedent, Subject: "awsOfferFeedRawColumns",
			Ask: "does anything else hard-code a column list?",
		}),
	}}
	qs := QuestionsFor(rev)
	if len(qs) != 1 {
		t.Fatalf("want one lookup, got %+v", qs)
	}
	if qs[0].ID != "c3" || qs[0].Subject != "awsOfferFeedRawColumns" {
		t.Fatalf("wrong question: %+v", qs[0])
	}
	if qs[0].Claim == "" {
		t.Fatal("the lookup has to know what claim it is checking, or it searches blind")
	}
}

// The second call's prefix is the first call's, byte for byte, or the cache
// does not serve it and the pass costs full price.
func TestTheRulingSharesTheReviewsPrefix(t *testing.T) {
	in := Input{Report: priors()}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "something", findings.Question{Kind: findings.QuestionDiff}),
	}}
	two := one.ruleRequest(in, Options{}.withDefaults(), Candidates(one.Review), nil)

	if !strings.HasPrefix(two.Prompt, one.Prompt) {
		t.Fatal("the ruling's user turn does not extend the review's, so nothing is cached")
	}
	if !strings.HasPrefix(two.System, one.System) {
		t.Fatal("the ruling's system block does not extend the review's")
	}
	if !two.rulesRatherThanReviews() {
		t.Fatal("the ruling went out under the review's own contract")
	}
	if one.rulesRatherThanReviews() {
		t.Fatal("the review went out under the ruling's contract")
	}
}

// An answer that never came back is not a negative answer, and the ruling has
// to be told which it is looking at.
func TestNoAnswersSaysSoRatherThanReadingAsClean(t *testing.T) {
	got := answersSection(nil)
	if !strings.Contains(got, "has been checked") {
		t.Fatalf("an empty lookup must not read as a clean bill: %q", got)
	}
}

// The evidence has to reach the ruling grouped by the claim it bears on, or
// the ruling gets a pile of code and no way to tell what any of it settles.
func TestAnswersAreGroupedByTheFindingTheyAnswer(t *testing.T) {
	env := &envelope.Envelope{
		Expansions: []envelope.Expansion{
			{Role: envelope.RoleSibling, Symbol: "account feed", File: "feed/account.go",
				StartLine: 10, EndLine: 20, Content: "CopyFrom(ctx, cols)",
				Details: map[string]string{"answers": "c3"}},
		},
		Notes: []string{"no precedent found for Foo, searched the whole tree"},
	}
	got := answersSection(env)
	if !strings.Contains(got, "For [c3]") {
		t.Fatalf("the answer is not tied to its finding: %q", got)
	}
	if !strings.Contains(got, "CopyFrom(ctx, cols)") {
		t.Fatal("the repository's own line is what makes it evidence")
	}
	if !strings.Contains(got, "searched the whole tree") {
		t.Fatal("a search that came back empty is evidence and has to be shown")
	}
}

// A ruling that names no finding, or names one twice, is dropped rather than
// failing the pass: the findings are the review, and one unattached ruling is
// worth losing on its own.
func TestRulingsThatNameNothingAreDropped(t *testing.T) {
	got, err := parseRulings([]byte(`{"rulings":[
      {"finding":"", "verdict":"kept", "evidence":"", "why":""},
      {"finding":"[c1]", "verdict":"withdrawn", "evidence":"x", "why":"y"},
      {"finding":"c1", "verdict":"kept", "evidence":"", "why":""}
    ]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want one ruling, got %+v", got)
	}
	// The brackets are stripped, so a model echoing the id as it was printed
	// is understood.
	if got["c1"].Verdict != findings.VerifiedWithdrawn {
		t.Fatalf("the first ruling for a finding wins: %+v", got["c1"])
	}
}

// A folded finding has to say it was checked and what came back, or the fold
// reads as the reviewer merely hedging.
func TestARulingReachesTheReport(t *testing.T) {
	rev := &findings.Review{Comments: []findings.ReviewComment{{
		File: "a.go", Line: 1, Body: "the column list will drift",
		Ruling: findings.Ruling{
			Verdict:  findings.VerifiedJustified,
			Evidence: "feed/account.go:12",
			Why:      "the sibling staging file makes the same choice",
		},
	}}}
	f := rev.CommentFindings()[0]
	if !strings.Contains(f.Context, "on purpose") {
		t.Fatalf("context = %q, want the ruling in words", f.Context)
	}
	if !strings.Contains(f.Context, "feed/account.go:12") {
		t.Fatal("the evidence has to travel with the ruling")
	}
}

// A review written before this existed, or by hand, carries no ruling and must
// render exactly as it used to.
func TestNoRulingAddsNoContext(t *testing.T) {
	rev := &findings.Review{Comments: []findings.ReviewComment{{File: "a.go", Body: "x"}}}
	if got := rev.CommentFindings()[0].Context; got != "" {
		t.Fatalf("context = %q, want nothing added", got)
	}
}

// The whole pass is optional at every point. A caller with no way to run the
// lookups still gets a ruling, over no answers, and everything that needed one
// comes back unverifiable rather than confirmed.
func TestVerifyWithoutAnAnswererStillAssemblesARuling(t *testing.T) {
	in := Input{Report: priors()}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "needs a lookup nobody can run", findings.Question{
			Kind: findings.QuestionPrecedent, Subject: "Foo"}),
	}}
	got, err := Verify(context.Background(), in, Options{DryRun: true, Verify: true}, one)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "The findings to rule on") {
		t.Fatalf("the ruling request was not assembled:\n%s", got.Prompt)
	}
	if !strings.Contains(got.Prompt, "[c1]") {
		t.Fatal("the finding needs the id the ruling addresses it by")
	}
	if !strings.Contains(got.Prompt, "has been checked") {
		t.Fatal("with no lookups run, the ruling must be told nothing was checked")
	}
}

// A clean review must not pay for a second call. Returning nothing is the
// answer this whole design exists to make possible.
func TestVerifyCostsNothingOnACleanReview(t *testing.T) {
	in := Input{Report: priors()}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(context.Background(), in, Options{DryRun: true, Verify: true}, one)
	if err != nil {
		t.Fatal(err)
	}
	if got != one {
		t.Fatal("a review with nothing to rule on was sent to a second call anyway")
	}
}
