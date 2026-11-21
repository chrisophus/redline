package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
)

// fakeJudge answers from a table keyed by the finding's body and the earlier
// comment's text.
type fakeJudge struct {
	same  map[string]float64
	err   error
	calls int
}

func (f *fakeJudge) Same(_ context.Context, c findings.ReviewComment, earlier []feedback.Thread) ([]float64, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]float64, len(earlier))
	for i, t := range earlier {
		out[i] = f.same[c.Body+"|"+t.Said]
	}
	return out, nil
}

func reviewOf(bodies ...string) findings.Review {
	var rev findings.Review
	for _, b := range bodies {
		rev.Comments = append(rev.Comments, findings.ReviewComment{
			File: "a.go", Line: 3, Body: b, Confidence: findings.ConfidenceMedium})
	}
	return rev
}

func TestAFindingSaidBeforeIsHeld(t *testing.T) {
	prior := []feedback.Thread{{File: "a.go", Line: 2, Said: "names compare by exact case", InBody: true}}
	judge := &fakeJudge{same: map[string]float64{
		"the name check is case-sensitive|names compare by exact case":     0.95,
		"the lock is held over a network call|names compare by exact case": 0.2,
	}}
	rev, did := MarkRepeats(context.Background(), judge,
		reviewOf("the name check is case-sensitive", "the lock is held over a network call"), prior)

	if did.Checked != 2 || did.Held != 1 || did.FollowUps != 0 {
		t.Fatalf("did = %+v", did)
	}
	held := rev.Comments[0]
	if held.Ruling.Verdict != findings.VerifiedAlreadyRaised || held.Ruling.Posts() {
		t.Errorf("the repeat was not ruled already raised: %+v", held.Ruling)
	}
	if !strings.Contains(held.Ruling.Evidence, "names compare by exact case") {
		t.Errorf("a held finding has to show what it repeats: %q", held.Ruling.Evidence)
	}
	if held.Confidence != findings.ConfidenceLow {
		t.Error("a held finding keeps a confidence that posts")
	}
	if rev.Comments[1].Ruling.Verdict != "" {
		t.Errorf("an unrelated finding was ruled on: %+v", rev.Comments[1].Ruling)
	}
}

// A new case of the same kind of mistake scored up to 0.82 on the saved
// reviews. It is the finding an author most needs, so it posts.
func TestAFindingUnderTheBarPosts(t *testing.T) {
	prior := []feedback.Thread{{File: "a.go", Said: "the list misses logs", InBody: true}}
	judge := &fakeJudge{same: map[string]float64{"the list misses .keep|the list misses logs": 0.8}}
	rev, did := MarkRepeats(context.Background(), judge, reviewOf("the list misses .keep"), prior)
	if did.Held != 0 || rev.Comments[0].Ruling.Verdict != "" {
		t.Fatalf("a finding under the bar was held: %+v", did)
	}
	if len(did.Seen) != 1 || did.Seen[0].Verdict != "" {
		t.Errorf("the comparison should still be on the record: %+v", did.Seen)
	}
}

// A reviewer that read the new code and still reports the defect is saying
// the fix did not land. Holding that back would hide it.
func TestAFindingAnsweredAsFixedIsKept(t *testing.T) {
	prior := []feedback.Thread{{File: "a.go", Said: "save holds the lock", Resolved: true,
		Replies: []feedback.Reply{{Author: "dev", Body: "Addressed in 3f9a2c1."}}}}
	judge := &fakeJudge{same: map[string]float64{"save still holds the lock|save holds the lock": 0.97}}
	rev, did := MarkRepeats(context.Background(), judge, reviewOf("save still holds the lock"), prior)
	if did.Held != 0 || did.FollowUps != 1 {
		t.Fatalf("did = %+v", did)
	}
	if !rev.Comments[0].Ruling.Posts() {
		t.Error("a finding that says a fix did not land was held back")
	}
}

func TestClaimedFixed(t *testing.T) {
	reply := func(s string) []feedback.Reply { return []feedback.Reply{{Body: s}} }
	cases := []struct {
		name string
		t    feedback.Thread
		want bool
	}{
		{"in a summary", feedback.Thread{InBody: true}, false},
		{"nobody answered", feedback.Thread{}, false},
		{"closed without a word", feedback.Thread{Resolved: true}, true},
		{"unanswered, and its lines have changed", feedback.Thread{Outdated: true}, true},
		{"answered as fixed", feedback.Thread{Resolved: true, Replies: reply("Addressed in abc1234.")}, true},
		{"turned down", feedback.Thread{Replies: reply("**Not fixing (intentional).** It mirrors staging.")}, false},
		{"answered some other way", feedback.Thread{Replies: reply("Good catch.")}, true},
	}
	for _, c := range cases {
		if got := claimedFixed(c.t); got != c.want {
			t.Errorf("%s: claimedFixed = %v, want %v", c.name, got, c.want)
		}
	}
}

// The pass must never take the review down with it.
func TestAJudgeThatFailsLeavesTheReviewAsWritten(t *testing.T) {
	prior := []feedback.Thread{{File: "a.go", Said: "x", InBody: true}}
	judge := &fakeJudge{err: errors.New("429: slow down")}
	rev, did := MarkRepeats(context.Background(), judge, reviewOf("one", "two"), prior)
	if did.Failed == "" || did.Held != 0 {
		t.Fatalf("did = %+v", did)
	}
	if judge.calls != 1 {
		t.Errorf("a failing judge was asked %d times; once is enough to know", judge.calls)
	}
	for _, c := range rev.Comments {
		if c.Ruling.Verdict != "" {
			t.Errorf("a finding was ruled on by a pass that failed: %+v", c.Ruling)
		}
	}
}

func TestNoEarlierCommentsAsksNothing(t *testing.T) {
	judge := &fakeJudge{}
	_, did := MarkRepeats(context.Background(), judge, reviewOf("one"), nil)
	if judge.calls != 0 || did.Checked != 0 {
		t.Fatalf("a first review has nothing to compare with: calls=%d did=%+v", judge.calls, did)
	}
}

// A long pull request sends the comments in the finding's own file first.
func TestEarlierCommentsAreBoundedAndOwnFileFirst(t *testing.T) {
	var prior []feedback.Thread
	for range maxRepeatEarlier + 5 {
		prior = append(prior, feedback.Thread{File: "other.go", Said: "x"})
	}
	prior = append(prior, feedback.Thread{File: "a.go", Said: "mine"})
	got := earlierFor(findings.ReviewComment{File: "a.go"}, prior)
	if len(got) != maxRepeatEarlier || got[0].Said != "mine" {
		t.Fatalf("len=%d first=%q", len(got), got[0].Said)
	}
}

// What the repeats pass ruled is not looked up or ruled on a second time.
func TestVerifyLeavesAHeldFindingAlone(t *testing.T) {
	rev := reviewOf("the name check is case-sensitive")
	rev.Comments[0].Ruling = findings.Ruling{Verdict: findings.VerifiedAlreadyRaised, Evidence: "earlier"}
	asked := false
	opts := Options{Verify: true, Answer: func(context.Context, []Question) (*envelope.Envelope, error) {
		asked = true
		return nil, nil
	}}
	out, err := Verify(context.Background(), Input{}, opts, &Result{Review: rev})
	if err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Error("the lookups were run for a finding already held")
	}
	if out.Review.Comments[0].Ruling.Verdict != findings.VerifiedAlreadyRaised {
		t.Errorf("ruling = %+v", out.Review.Comments[0].Ruling)
	}
}
