package feedback

import (
	"encoding/hex"
	"strings"
	"testing"
)

// marker builds the hidden comment `post` writes, so these tests read the
// real format rather than a copy of it that can drift.
func marker(head, fingerprint string) string {
	return "<!-- redline:fp:" + head + ":" + hex.EncodeToString([]byte(fingerprint)) + " -->"
}

func response(threads string) []byte {
	return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[` + threads + `]}}}}}`)
}

// The whole point: a reply is a convention nobody wrote down, handed over by
// the one person who knows it. It has to survive the parse intact.
func TestAReplyReachesTheReviewWithItsAuthor(t *testing.T) {
	raw := response(`{
      "isResolved": true, "isOutdated": false,
      "path": "internal/feed/offer.go", "line": 42,
      "comments": {"nodes": [
        {"body": "**Warning · agent** — StageObject checks before the transaction.\n` +
		marker("abc123", "internal/feed/offer.go\x00agent-comment\x00races") + `",
         "author": {"login": "redline"}, "reactionGroups": []},
        {"body": "Not fixing. Same pre-transaction idempotency check as account feed staging.",
         "author": {"login": "chrisophus"}, "reactionGroups": []}
      ]}
    }`)

	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want one thread, got %d", len(got))
	}
	th := got[0]
	if th.Fingerprint != "internal/feed/offer.go\x00agent-comment\x00races" {
		t.Fatalf("fingerprint = %q", th.Fingerprint)
	}
	if th.File != "internal/feed/offer.go" || th.Line != 42 {
		t.Fatalf("location = %s:%d", th.File, th.Line)
	}
	if !th.Resolved {
		t.Fatal("the thread was closed and the parse lost it")
	}
	if len(th.Replies) != 1 {
		t.Fatalf("replies = %+v", th.Replies)
	}
	if th.Replies[0].Author != "chrisophus" {
		t.Fatalf("a reply without its author cannot be attributed: %+v", th.Replies[0])
	}
	if !strings.Contains(th.Replies[0].Body, "account feed staging") {
		t.Fatalf("the reason has to survive: %q", th.Replies[0].Body)
	}
}

// A marker is bookkeeping between two runs of this tool. Putting one in front
// of a model invites the model to write one.
func TestMarkersDoNotReachTheModel(t *testing.T) {
	raw := response(`{
      "isResolved": false, "isOutdated": false, "path": "a.go", "line": 1,
      "comments": {"nodes": [
        {"body": "**Warning · agent** — this leaks.\n` + marker("abc", "a.go\x00r\x00m") + `",
         "author": {"login": "redline"}, "reactionGroups": []}
      ]}
    }`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got[0].Said, "redline:fp") || strings.Contains(got[0].Said, "<!--") {
		t.Fatalf("a marker survived into the prompt: %q", got[0].Said)
	}
	if !strings.Contains(got[0].Said, "this leaks") {
		t.Fatalf("the prose did not: %q", got[0].Said)
	}
}

// Somebody else's conversation is not Redline's to carry. Putting a human
// reviewer's thread into a model's context as though Redline had said it is a
// misattribution, and it spends the ceiling on a thread about something else.
func TestThreadsRedlineDidNotStartAreDropped(t *testing.T) {
	raw := response(`{
      "isResolved": false, "isOutdated": false, "path": "a.go", "line": 1,
      "comments": {"nodes": [
        {"body": "Could you rename this?", "author": {"login": "a-human"}, "reactionGroups": []},
        {"body": "done", "author": {"login": "chrisophus"}, "reactionGroups": []}
      ]}
    }`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want nothing, got %+v", got)
	}
}

// The one unambiguous signal on the list, and the cheapest for a reader to
// give.
func TestReactionsAreCounted(t *testing.T) {
	raw := response(`{
      "isResolved": false, "isOutdated": true, "path": "a.go", "line": 1,
      "comments": {"nodes": [
        {"body": "x ` + marker("abc", "a.go\x00r\x00m") + `",
         "author": {"login": "redline"},
         "reactionGroups": [
           {"content": "THUMBS_UP", "reactors": {"totalCount": 2}},
           {"content": "THUMBS_DOWN", "reactors": {"totalCount": 3}},
           {"content": "ROCKET", "reactors": {"totalCount": 9}}
         ]}
      ]}
    }`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Up != 2 || got[0].Down != 3 {
		t.Fatalf("up=%d down=%d, want 2 and 3", got[0].Up, got[0].Down)
	}
	if !got[0].Outdated {
		t.Fatal("the lines moved and the parse lost it")
	}
	if !got[0].Disputed() {
		t.Fatal("more thumbs down than up is the shape of a finding nobody wanted")
	}
}

// A session written twice from one pull request has to be the same session, or
// a fixture built from it does not replay.
func TestThreadOrderIsStable(t *testing.T) {
	raw := response(`{
      "isResolved": false, "isOutdated": false, "path": "z.go", "line": 5,
      "comments": {"nodes": [{"body": "z ` + marker("deadbeef", "z") + `", "author": {"login": "r"}, "reactionGroups": []}]}
    },{
      "isResolved": false, "isOutdated": false, "path": "a.go", "line": 9,
      "comments": {"nodes": [{"body": "a ` + marker("deadbeef", "a") + `", "author": {"login": "r"}, "reactionGroups": []}]}
    }`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].File != "a.go" {
		t.Fatalf("order = %+v", got)
	}
}

// A pull request nobody has reviewed is the common case and is not an error.
func TestAnEmptyPullRequestParsesToNothing(t *testing.T) {
	got, err := Parse(response(``))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want nothing, got %+v", got)
	}
}

// The predicate the second field round corrected. That team replies "Not
// fixing (intentional)" on every thread and leaves it open for the merge gate
// to close later, so a rule that waited for resolution would have counted a
// wall of replies as silence.
func TestAReplyIsAnAnswerWhetherOrNotTheThreadIsClosed(t *testing.T) {
	open := Thread{Replies: []Reply{{Author: "chrisophus", Body: "Not fixing (intentional)."}}}
	if !open.Answered() {
		t.Fatal("an open thread with a reply is answered; the reply is the answer")
	}
	if open.Disputed() {
		t.Fatal("a reply is not a thumbs down; read the text to know which it was")
	}
	if (Thread{Resolved: true}).Answered() {
		t.Fatal("a click with no words is not an answer")
	}
}

// The question survives rewording where the fingerprint does not, so it is
// what a later review matches on.
func TestTheQuestionTravelsWithThePostedComment(t *testing.T) {
	q := "precedent\x00awsofferfeedrawcolumns"
	raw := response(`{
      "isResolved": false, "isOutdated": false, "path": "feed.go", "line": 1,
      "comments": {"nodes": [
        {"body": "the column list will drift ` + marker("abc123", "feed.go\x00r\x00m") +
		` <!-- redline:q:` + hexOf(q) + ` -->",
         "author": {"login": "redline"}, "reactionGroups": []}
      ]}
    }`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Question != q {
		t.Fatalf("question = %q, want %q", got[0].Question, q)
	}
	if strings.Contains(got[0].Said, "redline:q") {
		t.Fatalf("the marker leaked into the prompt: %q", got[0].Said)
	}
}

// A comment posted before the marker existed carries no question, and that has
// to be an empty string rather than a match against everything.
func TestAnOlderCommentHasNoQuestion(t *testing.T) {
	raw := response(`{
      "isResolved": false, "isOutdated": false, "path": "a.go", "line": 1,
      "comments": {"nodes": [
        {"body": "old ` + marker("abc123", "a.go\x00r\x00m") + `",
         "author": {"login": "redline"}, "reactionGroups": []}
      ]}
    }`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Question != "" {
		t.Fatalf("question = %q, want empty", got[0].Question)
	}
}

func hexOf(s string) string { return hex.EncodeToString([]byte(s)) }
