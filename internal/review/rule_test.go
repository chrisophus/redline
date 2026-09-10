package review

import (
	"context"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/feedback"
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

// The second field round's first miss. The same pull request, reviewed again
// on an unchanged head after the author replied on every thread, posted the
// race finding a second time under wording with no words in common: first at
// the pre-transaction check, then at CopyFrom and ON CONFLICT. One claim, two
// wordings, two anchors, and a fingerprint that could not see it.
func TestARewordedFindingOnAnAnsweredThreadIsAlreadyRaised(t *testing.T) {
	q := findings.Question{Kind: findings.QuestionPrecedent, Subject: "ObjectAlreadyStaged"}
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("feed/offer.go", "CopyFrom cannot ON CONFLICT, so a concurrent stage fails the transaction.", q),
	}}
	cands := Candidates(rev)
	got := alreadyRaised(cands, []feedback.Thread{{
		File: "feed/offer.go", Line: 12,
		Said:     "StageObject checks for already staged before starting the transaction.",
		Question: q.Key(),
		Replies: []feedback.Reply{{
			Author: "chrisophus",
			Body:   "Not fixing (intentional). Same pre-transaction idempotency check as account feed staging.",
		}},
	}})
	if got["c1"].Verdict != findings.VerifiedAlreadyRaised {
		t.Fatalf("the reworded claim was not recognised: %+v", got)
	}
	if !strings.Contains(got["c1"].Evidence, "account feed staging") {
		t.Fatalf("a finding withheld on an earlier answer has to quote it: %q", got["c1"].Evidence)
	}
}

// The reply is the answer. That team replies and leaves the thread open for
// the merge gate to close, so waiting for resolution would recognise nothing.
func TestAnOpenThreadWithAReplyStillCounts(t *testing.T) {
	q := findings.Question{Kind: findings.QuestionCaller, Subject: "StageObject"}
	cands := Candidates(findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "worded differently", q),
	}})
	got := alreadyRaised(cands, []feedback.Thread{{
		File: "a.go", Question: q.Key(), Resolved: false,
		Replies: []feedback.Reply{{Author: "someone", Body: "intentional, see the sibling"}},
	}})
	if got["c1"].Verdict != findings.VerifiedAlreadyRaised {
		t.Fatal("an open thread with a reply is answered")
	}
}

// A thread nobody answered means the author has not looked. Saying it once
// more where they are looking is not noise, and suppressing it would hide a
// finding on the grounds that it had been ignored.
func TestAnUnansweredThreadDoesNotSuppressAnything(t *testing.T) {
	q := findings.Question{Kind: findings.QuestionCaller, Subject: "Foo"}
	cands := Candidates(findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "the same claim again", q),
	}})
	got := alreadyRaised(cands, []feedback.Thread{{File: "a.go", Question: q.Key()}})
	if len(got) != 0 {
		t.Fatalf("nobody answered, so nothing is settled: %+v", got)
	}
}

// A thread posted before the question marker existed still catches a requote,
// on the fingerprint's own identity. It cannot follow a rewording, which is
// what the model is still there for.
func TestAThreadWithoutAQuestionStillCatchesARequote(t *testing.T) {
	cands := Candidates(findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "this races on line 947", findings.Question{Kind: findings.QuestionDiff}),
	}})
	got := alreadyRaised(cands, []feedback.Thread{{
		File: "a.go", Said: "this races on line 12",
		Replies: []feedback.Reply{{Author: "someone", Body: "intentional and accepted"}},
	}})
	if got["c1"].Verdict != findings.VerifiedAlreadyRaised {
		t.Fatalf("a requote with the digits moved is the same comment: %+v", got)
	}
}

// Two different findings on one file must not collapse into each other just
// because the file has a history.
func TestADifferentFindingOnTheSameFileSurvives(t *testing.T) {
	cands := Candidates(findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "an entirely different problem", findings.Question{
			Kind: findings.QuestionPrecedent, Subject: "SomethingElse"}),
	}})
	got := alreadyRaised(cands, []feedback.Thread{{
		File: "a.go", Said: "this races",
		Question: findings.Question{Kind: findings.QuestionPrecedent, Subject: "ObjectAlreadyStaged"}.Key(),
		Replies:  []feedback.Reply{{Author: "someone", Body: "intentional and accepted"}},
	}})
	if len(got) != 0 {
		t.Fatalf("a busy file must not swallow new findings: %+v", got)
	}
}

// A re-review where every finding has already been answered has nothing for a
// ruling to decide, and paying for the call to be told so is the waste the
// field round paid for.
func TestAReReviewOfOnlyAnsweredFindingsCostsNothing(t *testing.T) {
	q := findings.Question{Kind: findings.QuestionPrecedent, Subject: "Foo"}
	in := Input{Report: priors(), Prior: []feedback.Thread{{
		File: "a.go", Question: q.Key(),
		Replies: []feedback.Reply{{Author: "someone", Body: "intentional, and here is why"}},
	}}}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "reworded entirely", q),
	}}
	// DryRun is not set: reaching the call at all would be the failure, and a
	// call with no key would error rather than return this.
	got, err := Verify(context.Background(), in, Options{Verify: true}, one)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Verified {
		t.Fatal("the pass ran and should say so")
	}
	if kept, _ := Kept(got.Review); kept != 0 {
		t.Fatal("an answered finding must not post again")
	}
}
