package review

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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

// Finding one of the outside review: a candidate the ruling never mentioned
// was posted unchecked. After a completed pass it fails closed instead.
func TestCombineFailsClosedOnACandidateTheRulingOmitted(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "kept", findings.Question{Kind: findings.QuestionDiff}),
		comment("b.go", "the model forgot this one", findings.Question{Kind: findings.QuestionDiff}),
	}}
	cands := Candidates(rev)
	model := map[string]findings.Ruling{"c1": {Verdict: findings.VerifiedKept, Evidence: "x"}}
	got := combineRulings(cands, cands, nil, model, "")
	if got["c1"].Verdict != findings.VerifiedKept {
		t.Fatalf("the ruled finding should keep its verdict: %+v", got["c1"])
	}
	if got["c2"].Verdict != findings.VerifiedUnverifiable {
		t.Fatalf("a candidate with no ruling must fail closed, got %q", got["c2"].Verdict)
	}
	out := Apply(rev, cands, got)
	if out.Comments[1].Confidence != findings.ConfidenceLow {
		t.Fatal("the unruled finding must not post")
	}
}

// The deterministic already-raised read the actual thread, so it wins over a
// model that wanted to keep the same finding.
func TestCombineLetsTheDeterministicMatchWin(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "x", findings.Question{Kind: findings.QuestionDiff}),
	}}
	cands := Candidates(rev)
	model := map[string]findings.Ruling{"c1": {Verdict: findings.VerifiedKept}}
	settled := map[string]findings.Ruling{"c1": {Verdict: findings.VerifiedAlreadyRaised}}
	got := combineRulings(cands, nil, settled, model, "")
	if got["c1"].Verdict != findings.VerifiedAlreadyRaised {
		t.Fatalf("the thread the reader answered must win, got %q", got["c1"].Verdict)
	}
}

// A model already-raised with no earlier thread and no sibling asking the same
// question is a finding suppressed on nothing, so it is demoted.
func TestSanitizeDemotesAnAlreadyRaisedWithNoBacking(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "x", findings.Question{Kind: findings.QuestionPrecedent, Subject: "Foo"}),
	}}
	got := sanitizeRulings(Candidates(rev),
		map[string]findings.Ruling{"c1": {Verdict: findings.VerifiedAlreadyRaised}}, "")
	if got["c1"].Verdict != findings.VerifiedUnverifiable {
		t.Fatalf("an unbacked already-raised must not suppress, got %q", got["c1"].Verdict)
	}
}

// Two findings asking the same question are one, so the model may rule the
// duplicate already-raised and that is honest.
func TestSanitizeKeepsAnAlreadyRaisedWithASameQuestionSibling(t *testing.T) {
	q := findings.Question{Kind: findings.QuestionPrecedent, Subject: "Foo"}
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "one wording", q),
		comment("a.go", "the other wording", q),
	}}
	got := sanitizeRulings(Candidates(rev),
		map[string]findings.Ruling{"c2": {Verdict: findings.VerifiedAlreadyRaised}}, "")
	if got["c2"].Verdict != findings.VerifiedAlreadyRaised {
		t.Fatalf("a duplicate of a same-question finding is already-raised, got %q", got["c2"].Verdict)
	}
}

// Withdrawn and justified carry a quoted line, and a quote that is nowhere in
// the material was invented. Suppressing a finding on it is the failure this
// pass exists to prevent.
func TestSanitizeDemotesAWithdrawnWithInventedEvidence(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "x", findings.Question{Kind: findings.QuestionDiff}),
	}}
	got := sanitizeRulings(Candidates(rev), map[string]findings.Ruling{
		"c1": {Verdict: findings.VerifiedWithdrawn, Evidence: "a line that is nowhere in what the ruling saw"},
	}, "the material shown to the ruling said nothing of the sort")
	if got["c1"].Verdict != findings.VerifiedUnverifiable {
		t.Fatalf("invented evidence must not withdraw a finding, got %q", got["c1"].Verdict)
	}
}

// A withdrawn that quotes a real line in the material stands.
func TestSanitizeKeepsAWithdrawnThatQuotesTheMaterial(t *testing.T) {
	corpus := "func stage() {\n\tif alreadyStaged() { return nil }\n}"
	rev := findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "x", findings.Question{Kind: findings.QuestionDiff}),
	}}
	got := sanitizeRulings(Candidates(rev), map[string]findings.Ruling{
		"c1": {Verdict: findings.VerifiedWithdrawn, Evidence: "if alreadyStaged() { return nil }"},
	}, corpus)
	if got["c1"].Verdict != findings.VerifiedWithdrawn {
		t.Fatalf("a quote found in the material stands, got %q", got["c1"].Verdict)
	}
}

// The lookups cost money, so a finding this pull request already answered is
// not sent to one. The scout is asked only about what is still open.
func TestTheLookupsSkipFindingsAlreadyAnswered(t *testing.T) {
	q1 := findings.Question{Kind: findings.QuestionPrecedent, Subject: "Already"}
	q2 := findings.Question{Kind: findings.QuestionPrecedent, Subject: "Fresh"}
	in := Input{Report: priors(), Prior: []feedback.Thread{{
		File: "a.go", Question: q1.Key(),
		Replies: []feedback.Reply{{Author: "someone", Body: "intentional"}},
	}}}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "already answered", q1),
		comment("b.go", "still open", q2),
	}}
	var asked []string
	opts := Options{Verify: true, DryRun: true, Answer: func(_ context.Context, qs []Question) (*envelope.Envelope, error) {
		for _, q := range qs {
			asked = append(asked, q.ID)
		}
		return nil, nil
	}}
	if _, err := Verify(context.Background(), in, opts, one); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "c2" {
		t.Fatalf("only the open finding should be looked up, asked %v", asked)
	}
}

// A total lookup failure must not read as a clean change. The ruling used to
// be sent with no answers at all, and a finding whose question named a lookup
// is unverifiable when no lookup ran, so every one of them was withheld in a
// single step and the author saw an empty review. The findings go out as the
// review wrote them, and the report says why.
func TestLookupsThatAnswerNothingLeaveTheFindingsAlone(t *testing.T) {
	q := findings.Question{Kind: findings.QuestionPrecedent, Subject: "Thing"}
	for _, tc := range []struct {
		name string
		env  *envelope.Envelope
		err  error
	}{
		{"the scout failed", nil, errors.New("no api key")},
		{"the scout filed nothing", &envelope.Envelope{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{Report: priors()}
			one, err := Assemble(in, Options{})
			if err != nil {
				t.Fatal(err)
			}
			one.Review = findings.Review{Comments: []findings.ReviewComment{
				comment("a.go", "still open", q),
			}}
			opts := Options{Verify: true, Answer: func(_ context.Context, _ []Question) (*envelope.Envelope, error) {
				return tc.env, tc.err
			}}
			got, err := Verify(context.Background(), in, opts, one)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Review.Comments) != 1 {
				t.Fatalf("the findings were withheld on no evidence: %+v", got.Review.Comments)
			}
			if got.Review.Comments[0].Ruling.Verdict != "" {
				t.Errorf("a finding was ruled with nothing to rule on: %q",
					got.Review.Comments[0].Ruling.Verdict)
			}
			if got.VerifyFailed == "" {
				t.Error("the report does not say the lookups came back with nothing")
			}
		})
	}
}

// A note with no records is an answer: "no precedent found for X, searched
// the whole tree" is evidence, and the ruling is sent to read it.
func TestANoteWithNoRecordsIsStillAnAnswer(t *testing.T) {
	in := Input{Report: priors()}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "still open", findings.Question{Kind: findings.QuestionPrecedent, Subject: "Thing"}),
	}}
	opts := Options{Verify: true, DryRun: true, Answer: func(_ context.Context, _ []Question) (*envelope.Envelope, error) {
		return &envelope.Envelope{Notes: []string{"no precedent found for Thing, searched the whole tree"}}, nil
	}}
	got, err := Verify(context.Background(), in, opts, one)
	if err != nil {
		t.Fatal(err)
	}
	if got.VerifyFailed != "" {
		t.Fatalf("a note is an answer, so the ruling must be sent: %q", got.VerifyFailed)
	}
	if !strings.Contains(got.Prompt, "searched the whole tree") {
		t.Error("the note did not reach the ruling")
	}
}

// The ruling instruction rides at the tail of the user turn and the system
// block stays byte-identical, or the prompt cache serves nothing and the
// second call pays full price.
func TestTheRulingInstructionRidesInTheUserTurn(t *testing.T) {
	in := Input{Report: priors()}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "x", findings.Question{Kind: findings.QuestionDiff}),
	}}
	two := one.ruleRequest(in, Options{}.withDefaults(), Candidates(one.Review), nil)
	if two.System != one.System {
		t.Fatal("the system block must stay byte-identical between the review and its ruling")
	}
	if !strings.HasSuffix(two.Prompt, rulePrompt) {
		t.Fatal("the ruling instruction must be at the tail of the user turn")
	}
	if strings.Contains(two.System, "Now check what you found") {
		t.Fatal("the ruling instruction must not be in the system block")
	}
}

// A gateway that serves claude over the OpenAI protocol does not always honour
// the json_schema and returns the rulings array wrapped in a JSON string. The
// pass must read that rather than fail open, which in the field left six
// findings unchecked on a review whose ruling came back stringified.
func TestParseRulingsToleratesAStringifiedArray(t *testing.T) {
	wrapped := []byte(`{"rulings":"[{\"finding\":\"c1\",\"verdict\":\"withdrawn\",\"evidence\":\"e\",\"why\":\"w\"}]"}`)
	got, err := parseRulings(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if got["c1"].Verdict != findings.VerifiedWithdrawn {
		t.Fatalf("a stringified array must still parse: %+v", got)
	}
	// The ordinary array shape still works.
	plain := []byte(`{"rulings":[{"finding":"c2","verdict":"kept","evidence":"e","why":"w"}]}`)
	got, err = parseRulings(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got["c2"].Verdict != findings.VerifiedKept {
		t.Fatalf("the array shape must still parse: %+v", got)
	}
}

// The candidates are shown as "[c1] file:line", and a model at high effort
// returned that whole string as the finding id. Every ruling then matched
// nothing, every finding was recorded as unruled, and the run said none were
// kept as though it had judged them. The id is the first token, however the
// model dressed it.
func TestParseRulingsReadsTheIdOutOfWhatTheModelCopied(t *testing.T) {
	for _, shape := range []string{"c1", "[c1]", "[c1] internal/scout/openai.go:132", "c1 openai.go:132", " [c1]\n"} {
		body := []byte(`{"rulings":[{"finding":` + strconv.Quote(shape) + `,"verdict":"kept","evidence":"e","why":"w"}]}`)
		got, err := parseRulings(body)
		if err != nil {
			t.Fatal(err)
		}
		if got["c1"].Verdict != findings.VerifiedKept {
			t.Errorf("finding %q did not resolve to c1: %+v", shape, got)
		}
	}
}

// A schema-dropping gateway has also returned rulings as an object instead of
// an array: one ruling on its own, or a map keyed by the finding id. Both
// carry the same rulings, and the pass reads them rather than failing open,
// which is the failure that left findings unchecked in the field.
func TestParseRulingsToleratesObjectShapes(t *testing.T) {
	single := []byte(`{"rulings":{"finding":"c1","verdict":"withdrawn","evidence":"e","why":"w"}}`)
	got, err := parseRulings(single)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["c1"].Verdict != findings.VerifiedWithdrawn {
		t.Fatalf("a single ruling object must parse: %+v", got)
	}

	keyed := []byte(`{"rulings":{"c1":{"verdict":"kept","evidence":"e","why":"w"},` +
		`"c2":{"verdict":"withdrawn","evidence":"e2","why":"w2"}}}`)
	got, err = parseRulings(keyed)
	if err != nil {
		t.Fatal(err)
	}
	if got["c1"].Verdict != findings.VerifiedKept || got["c2"].Verdict != findings.VerifiedWithdrawn {
		t.Fatalf("a map keyed by finding id must fill the finding from the key: %+v", got)
	}

	// A re-wrapped, stringified object: the gateway put a whole {"rulings":[…]}
	// as a JSON string into the rulings field. Seen on a large ruling over the
	// Marketplace gateway even with the forced tool call. Unwrap and read it.
	nested := []byte(`{"rulings":"{\"rulings\":[{\"finding\":\"c1\",\"verdict\":\"kept\",\"evidence\":\"e\",\"why\":\"w\"}]}"}`)
	got, err = parseRulings(nested)
	if err != nil {
		t.Fatal(err)
	}
	if got["c1"].Verdict != findings.VerifiedKept {
		t.Fatalf("a re-wrapped stringified object must parse: %+v", got)
	}

	// A body that is no ruling shape at all still fails the pass rather than
	// inventing one, and names what it saw.
	if _, err := parseRulings([]byte(`{"rulings":42}`)); err == nil {
		t.Fatal("a rulings value that is neither array nor object must error")
	}
}

// A gateway has also returned the whole {"rulings":[…]} object as a JSON string
// and escaped it only once, so the decoded value is not valid JSON: a quote
// inside a finding's own text (a code snippet like == "") sits raw where the
// wire should carry an escaped one. Seen on the large MKT-1360 ruling over the
// Marketplace gateway, where it dropped every verdict. Re-escape those quotes
// and read the rulings rather than leaving the findings unchecked.
func TestParseRulingsRecoversAStringifiedObjectThatLostEscaping(t *testing.T) {
	mangled := []byte(`{"rulings":"{\"rulings\":[{\"finding\":\"c1\",\"verdict\":\"kept\",` +
		`\"evidence\":\"e\",\"why\":\"w\",` +
		`\"analysis\":\"the query.buyerScopeSQL == \"\" branch is separate\"}]}"}`)
	got, err := parseRulings(mangled)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := got["c1"]
	if !ok || r.Verdict != findings.VerifiedKept {
		t.Fatalf("a stringified object that lost a level of escaping must still parse: %+v", got)
	}
	if !strings.Contains(r.Analysis, `== "" branch`) {
		t.Fatalf("the re-escaped quotes must survive into the analysis text: %q", r.Analysis)
	}
}

// The ruling reasons before it decides: the schema carries an analysis field,
// the pass reads it back, and it serializes before the verdict so a model held
// to schema order writes its working first rather than justifying a verdict it
// already wrote.
func TestRulingReasonsBeforeItDecides(t *testing.T) {
	body := []byte(`{"rulings":[{"analysis":"the caller at x.go:10 is updated in this same commit, so the missed-caller claim is false","finding":"c1","verdict":"withdrawn","evidence":"x.go:10 foo(newSig)","why":"caller updated"}]}`)
	got, err := parseRulings(body)
	if err != nil {
		t.Fatal(err)
	}
	if r := got["c1"]; r.Verdict != findings.VerifiedWithdrawn || !strings.Contains(r.Analysis, "same commit") {
		t.Fatalf("the analysis must survive the parse with the verdict: %+v", r)
	}

	raw, err := json.Marshal(ruleSchema())
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	ai, vi := strings.Index(s, `"analysis"`), strings.Index(s, `"verdict"`)
	if ai < 0 || vi < 0 || ai > vi {
		t.Fatalf("analysis must be in the schema and serialize before verdict, got analysis@%d verdict@%d", ai, vi)
	}
}

// A review whose every finding says the diff settles it asks nothing, which is
// reachable and was measured twice: at --effort high the reviewer chose the
// diff kind for every finding in both runs. The scout must not be called with
// an empty list, and the ruling still has to be sent, because the diff those
// findings point at is what it rules on.
func TestAReviewThatAsksNothingRunsNoLookupsAndIsStillRuled(t *testing.T) {
	in := Input{Report: priors()}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "the hunk shows the error going nowhere", findings.Question{Kind: findings.QuestionDiff}),
		comment("b.go", "a guess with nothing to look up", findings.Question{Kind: findings.QuestionNone}),
	}}
	var calls int
	opts := Options{Verify: true, DryRun: true, Answer: func(_ context.Context, qs []Question) (*envelope.Envelope, error) {
		calls++
		return nil, nil
	}}
	got, err := Verify(context.Background(), in, opts, one)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("the lookups ran %d time(s) with no question to answer", calls)
	}
	if got == one {
		t.Fatal("the findings were left unruled, so nothing checked them at all")
	}
	if !strings.Contains(got.Prompt, "The findings to rule on") {
		t.Fatalf("the ruling request was not assembled:\n%s", got.Prompt)
	}
	if !strings.Contains(got.Prompt, "has been checked") {
		t.Error("the ruling must be told no lookup ran, or an absent answer reads as a clean one")
	}
}

// The gate that decides what an author reads has to be the check rather than
// the guess before it. On a measured run the only real logic defect in the
// packet was rated low by stage one, kept by the ruling with a line quoted
// from the diff, and withheld from the pull request anyway.
func TestAFindingKeptOnQuotedEvidencePostsWhateverStageOneGuessed(t *testing.T) {
	corpus := "func record() error {\n\tif len(ts.records) >= ts.limits.MaxRecords {\n"
	cands := []Candidate{
		{ID: "c1", Comment: findings.ReviewComment{
			File: "a.go", Body: "the cap is checked before the dedup", Confidence: findings.ConfidenceLow,
			Question: findings.Question{Kind: findings.QuestionDiff},
		}},
		{ID: "c2", Comment: findings.ReviewComment{
			File: "b.go", Body: "same shape, no quote behind it", Confidence: findings.ConfidenceLow,
			Question: findings.Question{Kind: findings.QuestionDiff},
		}},
	}
	rulings := sanitizeRulings(cands, map[string]findings.Ruling{
		"c1": {Verdict: findings.VerifiedKept, Evidence: "if len(ts.records) >= ts.limits.MaxRecords {"},
		"c2": {Verdict: findings.VerifiedKept, Evidence: "a line nobody can find in the material"},
	}, corpus)

	if !rulings["c1"].Grounded {
		t.Fatal("a quote out of the material was not read as evidence")
	}
	if rulings["c2"].Grounded {
		t.Error("an invented quote was taken as evidence")
	}
	// Both stay kept. A kept verdict is never demoted for its quote; what the
	// quote decides is whether stage one's doubt still withholds the finding.
	for id, r := range rulings {
		if r.Verdict != findings.VerifiedKept {
			t.Errorf("%s came back %q, want it left kept", id, r.Verdict)
		}
	}

	rev := Apply(findings.Review{Comments: []findings.ReviewComment{
		cands[0].Comment, cands[1].Comment,
	}}, cands, rulings)
	got := (&rev).CommentFindings()
	if len(got) != 2 {
		t.Fatalf("want both findings, got %d", len(got))
	}
	if got[0].Confidence == findings.ConfidenceLow {
		t.Error("a finding the ruling verified is still folded away as unsure")
	}
	if got[1].Confidence != findings.ConfidenceLow {
		t.Error("a finding kept on an unquotable line was promoted past the gate")
	}
}

// The pass writes its rulings onto the review in place and folds what it did
// not keep down to low confidence, so after it runs nothing says what the
// reviewer originally proposed. That is the first thing anyone asking why a
// review came back with two of nine findings wants to read.
func TestTheVerifyingPassKeepsWhatStageOneProposed(t *testing.T) {
	in := Input{Report: priors()}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "settled by the diff", findings.Question{Kind: findings.QuestionDiff}),
		comment("b.go", "needs a lookup", findings.Question{
			Kind: findings.QuestionPrecedent, Subject: "Thing", Ask: "does anything else do this?"}),
	}}
	answers := &envelope.Envelope{Expansions: []envelope.Expansion{{
		Role: envelope.RoleCaller, File: "c.go", StartLine: 1, EndLine: 4,
		Content: "x\n", Details: map[string]string{"answers": "c2", "foundVia": "grep"},
	}}}
	opts := Options{Verify: true, DryRun: true, Answer: func(_ context.Context, _ []Question) (*envelope.Envelope, error) {
		return answers, nil
	}}
	if _, err := Verify(context.Background(), in, opts, one); err != nil {
		t.Fatal(err)
	}
	if len(one.Candidates) != 2 || one.Candidates[1].Comment.Body != "needs a lookup" {
		t.Fatalf("candidates = %+v, want stage one's findings as it wrote them", one.Candidates)
	}
	// The finding the diff settles is not worth a lookup, so only the other
	// one was asked about, and the trace has to be able to say which.
	if len(one.Questions) != 1 || one.Questions[0].ID != "c2" {
		t.Fatalf("questions = %+v, want only the answerable one", one.Questions)
	}
	if one.Answers != answers {
		t.Error("what the lookups came back with was not kept")
	}
}
