package run

import (
	"testing"

	"github.com/chrisophus/redline/internal/feedback"
)

// The session is the whole input to `review`, so what a pull request already
// heard has to survive the round trip. Fetching it again at review time would
// make the review a function of whatever was posted in the meantime, which is
// exactly the property freezing a session exists to give up.
func TestSessionKeepsWhatThePullRequestAlreadyHeard(t *testing.T) {
	dir := t.TempDir()
	res := &Result{PriorReview: []feedback.Thread{{
		Fingerprint: "a.go\x00agent-comment\x00races",
		File:        "a.go", Line: 3, Said: "this races", Resolved: true,
		Replies: []feedback.Reply{{Author: "chrisophus", Body: "intentional, see the sibling"}},
	}}}
	if err := SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PriorReview) != 1 {
		t.Fatalf("the conversation did not survive the session: %+v", got.PriorReview)
	}
	if got.PriorReview[0].Replies[0].Body != "intentional, see the sibling" {
		t.Fatalf("the reply did not: %+v", got.PriorReview[0])
	}
}
