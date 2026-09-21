package main

import "testing"

// --recap measures from the most recent earlier review, and which one that is
// comes from when it was submitted rather than where it sits in the list.
// Reading the last entry was right only while the API returned them oldest
// first, and a recap measured from the wrong baseline describes the wrong
// change without anything saying so.
func TestTheRecapBaselineIsTheLatestEarlierReview(t *testing.T) {
	body := func(sha string) string { return "a posted review <!-- redline:review:" + sha + " -->" }
	const me, head = "redline-bot", "cccccccccccc"
	reviews := []ghAuthoredBody{
		{Login: me, Body: body("aaaaaaaaaaaa"), At: "2026-09-01T10:00:00Z"},
		// Out of order, which is the case position alone gets wrong.
		{Login: me, Body: body("bbbbbbbbbbbb"), At: "2026-09-20T10:00:00Z"},
		{Login: me, Body: body("dddddddddddd"), At: "2026-09-10T10:00:00Z"},
		// This review, re-posted at the head being posted now.
		{Login: me, Body: body(head), At: "2026-09-21T10:00:00Z"},
		// Somebody else's review carrying a marker.
		{Login: "someone-else", Body: body("eeeeeeeeeeee"), At: "2026-09-21T11:00:00Z"},
	}
	if got := latestReviewedHead(reviews, me, head); got != "bbbbbbbbbbbb" {
		t.Errorf("the recap would be measured from %s, and the latest earlier review ran against bbbbbbbbbbbb", got)
	}
	// With no earlier review there is nothing to measure from, which is the
	// case cmdPost turns into a refusal rather than a guess.
	only := []ghAuthoredBody{{Login: me, Body: body(head), At: "2026-09-21T10:00:00Z"}}
	if got := latestReviewedHead(only, me, head); got != "" {
		t.Errorf("a pull request whose only review is this one offered %q as a baseline", got)
	}
}
