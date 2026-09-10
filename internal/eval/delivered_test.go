package eval

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// noise is the four shapes of comment the field actually sent, written the way
// the real ones were written rather than as obvious straw men.
//
// Every one of these is modelled on a comment that reached a real pull request
// and cost a real person a thread:
//
//   - a warning that admits in its own words it may not matter, which the
//     prompt already forbids and which posted anyway;
//   - an info finding, of which one round sent eight, every one a thread the
//     team triaged to learn nothing was wrong;
//   - a speculation whose author could name nothing that would settle it;
//   - a defect the author had already answered, back in new words on a new
//     line, which no fingerprint could catch.
func noise() []findings.ReviewComment {
	return []findings.ReviewComment{{
		File: nilGuard, Line: 40, Severity: findings.SeverityWarning,
		Confidence: findings.ConfidenceHigh,
		Body: "Acceptable but worth noting: the guard message builds its string " +
			"before the nil test, so a long path is formatted on every call.",
		Question: findings.Question{Kind: findings.QuestionDiff},
	}, {
		File: nilWalk, Line: 12, Severity: findings.SeverityInfo,
		Confidence: findings.ConfidenceHigh,
		Body:       "The new rule's identifier is spelled unnecessary-nil-check, which reads well.",
		Question:   findings.Question{Kind: findings.QuestionDiff},
	}, {
		File: nilFacts, Line: 88, Severity: findings.SeverityWarning,
		Confidence: findings.ConfidenceHigh,
		Body:       "This fact table could grow without bound on a very large function.",
		Question:   findings.Question{Kind: findings.QuestionNone},
	}, {
		File: nilGuard, Line: 77, Severity: findings.SeverityWarning,
		Confidence: findings.ConfidenceHigh,
		Body: "The combined guard is dropped wholesale, which leaves the parameter " +
			"that was never proven unprotected.",
		Question: findings.Question{
			Kind: findings.QuestionPrecedent, Subject: "combinedGuard"},
		Ruling: findings.Ruling{
			Verdict:  findings.VerifiedAlreadyRaised,
			Evidence: "chrisophus, on the earlier thread: intentional, each disjunct is its own guard",
		},
	}}
}

// The measurement the plan demanded before the verifying pass could be
// trusted, and the one nothing had yet taken.
//
// A control review known to be entirely correct, with four comments modelled on
// what the field actually sent mixed into it. The gates have to remove the four
// and keep all fourteen. Either half alone is easy and worthless: a pass that
// suppresses nothing removes no noise, and one that suppresses everything
// scores perfectly on precision and costs the author every real defect.
func TestTheGatesRemoveTheNoiseAndKeepEveryLabelledDefect(t *testing.T) {
	f := nilRulesFixture(t)
	correct := nilRulesCorrectReview()

	noisy := findings.Review{Comments: append(append(
		[]findings.ReviewComment{}, correct.Comments...), noise()...)}

	delivered, written, lost := ScoreDelivered(f, noisy)
	t.Log(DeliveryReport("gorefactor-nil-rules", Deliver(noisy), lost))

	// The bar. A labelled defect the pipeline suppressed is a regression
	// whatever it did to the noise, and this is the assertion that says so.
	if len(lost) > 0 {
		t.Fatalf("the gates cost %d labelled defect(s): %v", len(lost), lost)
	}
	if len(delivered.Caught) != len(f.Annotation.Expect) {
		t.Fatalf("delivered caught %d of %d labelled defects",
			len(delivered.Caught), len(f.Annotation.Expect))
	}
	// And the other half: the noise is gone.
	d := Deliver(noisy)
	if d.SuppressedCount()+len(d.InBody) != len(noise()) {
		t.Fatalf("of %d noise comments, %d were suppressed and %d demoted to the body",
			len(noise()), d.SuppressedCount(), len(d.InBody))
	}
	if len(d.Interrupts) != len(correct.Comments) {
		t.Fatalf("%d comments opened a thread, want only the %d real defects",
			len(d.Interrupts), len(correct.Comments))
	}
	// Written and delivered catch the same defects; the difference is entirely
	// what nobody had to read.
	if len(written.Caught) != len(delivered.Caught) {
		t.Fatalf("written caught %d, delivered %d", len(written.Caught), len(delivered.Caught))
	}
}

// Each gate has to be doing its own work. If one of them caught everything the
// others do, the rest are untested and would rot without anyone noticing.
func TestEachGateAccountsForItsOwnNoise(t *testing.T) {
	f := nilRulesFixture(t)
	noisy := findings.Review{Comments: append(append(
		[]findings.ReviewComment{}, nilRulesCorrectReview().Comments...), noise()...)}
	_ = f

	d := Deliver(noisy)
	for reason, want := range map[string]int{
		"hedged":                       1,
		"unsure":                       1,
		findings.VerifiedAlreadyRaised: 1,
	} {
		if got := len(d.Suppressed[reason]); got != want {
			t.Errorf("%s suppressed %d, want %d", reason, got, want)
		}
	}
	if len(d.InBody) != 1 {
		t.Errorf("the reviewer's info should reach the body and nothing else: %+v", d.InBody)
	}
}

// The gates are the reviewer's own findings only. A measurement is a fact
// about the change and reaches the reader whatever it says, which this pins
// from the scoring side as well as the posting side.
func TestNothingSuppressesAMeasurement(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{{
		File: "a.go", Line: 1, Severity: findings.SeverityInfo,
		Confidence: findings.ConfidenceLow,
		Body:       "Not necessarily a problem: an existing test may already cover this.",
	}}}
	// A review file's comments are always source llm by construction, so the
	// measurement case is tested where measurements exist, in post. What this
	// pins is that the reviewer's own low-confidence info does not reach
	// anyone, which is the same rule seen from here.
	if d := Deliver(rev); d.SuppressedCount() != 1 {
		t.Fatalf("an unsure info finding reached a reader: %+v", d)
	}
}

// A review that went through no verifying pass has to be delivered exactly as
// it was before any of this existed, or turning the pass off would change what
// a reader sees.
func TestAReviewWithNoRulingIsDeliveredWhole(t *testing.T) {
	f := nilRulesFixture(t)
	correct := nilRulesCorrectReview()

	delivered, written, lost := ScoreDelivered(f, correct)
	if len(lost) != 0 {
		t.Fatalf("an unruled review lost %v", lost)
	}
	if len(delivered.Caught) != len(written.Caught) {
		t.Fatal("the gates changed an unruled review")
	}
	if d := Deliver(correct); d.SuppressedCount() != 0 {
		t.Fatalf("nothing here disqualifies itself: %+v", d.Suppressed)
	}
}

// The report is what a person reads when deciding whether the trade was worth
// it, so it has to name the defect a suppression cost rather than count it.
func TestTheDeliveryReportNamesWhatWasLost(t *testing.T) {
	got := DeliveryReport("fx", Delivered{
		Interrupts: []findings.ReviewComment{{}},
		Suppressed: map[string][]findings.ReviewComment{"hedged": {{}}},
	}, []string{"history-walks-head-not-base"})
	if !strings.Contains(got, "LOST 1") || !strings.Contains(got, "history-walks-head-not-base") {
		t.Fatalf("a reader cannot check a loss they cannot name: %q", got)
	}
}

// An empty-body comment produces no finding, so pairing findings with comments
// by position drifts the moment one appears. A later comment's delivered status
// then belongs to a different comment's finding, and a suppressed noise comment
// can be reported as an interrupt on the diff.
func TestAnEmptyBodyCommentDoesNotMisattributeADelivery(t *testing.T) {
	rev := findings.Review{Comments: []findings.ReviewComment{{
		File: "a.go", Line: 1, Severity: findings.SeverityWarning,
		Confidence: findings.ConfidenceHigh,
		Body:       "",
	}, {
		File: "a.go", Line: 2, Severity: findings.SeverityInfo,
		Confidence: findings.ConfidenceLow,
		Body:       "A low-confidence info finding nobody should have to read.",
	}, {
		File: "a.go", Line: 3, Severity: findings.SeverityWarning,
		Confidence: findings.ConfidenceHigh,
		Body:       "The lock is taken but never released on the error path.",
		Question:   findings.Question{Kind: findings.QuestionDiff},
	}}}

	d := Deliver(rev)
	if len(d.Interrupts) != 1 {
		t.Fatalf("want exactly the real defect interrupting, got %+v", d.Interrupts)
	}
	if d.Interrupts[0].Body != rev.Comments[2].Body {
		t.Fatalf("the interrupt is attributed to the wrong comment: %q", d.Interrupts[0].Body)
	}
	if d.SuppressedCount() != 1 {
		t.Fatalf("the low-confidence info should be the only suppression: %+v", d.Suppressed)
	}
}
