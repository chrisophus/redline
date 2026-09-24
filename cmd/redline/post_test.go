package main

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/post"
	"github.com/chrisophus/redline/internal/target"
)

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

// A walkthrough post picks up the recap on its own when the session has one,
// measured from the commit the session stored with it. --recap only makes it
// required.
func TestTheRecapAppliesWithoutTheFlag(t *testing.T) {
	const head, since = "cccccccccccccccc", "aaaaaaaaaaaaaaaa"
	rep := &findings.Report{Agent: &findings.AgentReview{
		Overview: "The whole change.", Recap: "The loop moved.",
		RecapSince: since, RecapFiles: []string{"b.go"},
	}}
	prof := &post.Profile{BodyStyle: post.BodyWalkthrough}
	tgt := &target.Target{Kind: target.KindPR, Head: head, PR: &target.PullRequest{Number: 1}}
	payload := post.BuildAttest(rep, tgt, "", nil, prof, nil)

	got, err := withRecap(opts{}, payload, rep.Agent, true, nil, "", head)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Body, "Since the last review") || !strings.Contains(got.Body, "aaaaaaaaaaaa") {
		t.Errorf("the recap should apply without --recap, from the stored commit:\n%s", got.Body)
	}

	// Without a recap in the session the body keeps its overview, and only
	// --recap turns that into an error.
	plain := &findings.AgentReview{Overview: "The whole change."}
	if got, err := withRecap(opts{}, payload, plain, true, nil, "", head); err != nil || got.Body != payload.Body {
		t.Errorf("no recap should leave the body alone, got err=%v", err)
	}
	if _, err := withRecap(opts{recap: true, since: since}, payload, plain, true, nil, "", head); err == nil {
		t.Error("--recap with no recap paragraph should be refused")
	}

	// A --since that names another commit than the recap was written against
	// would put the wrong commit beside the paragraph.
	if _, err := withRecap(opts{since: "bbbbbbbb"}, payload, rep.Agent, true, nil, "", head); err == nil {
		t.Error("a --since that disagrees with the stored commit should be refused")
	}
}
