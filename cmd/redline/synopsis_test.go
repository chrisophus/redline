package main

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/target"
)

// The describing call runs on every review that can have one, so the shapes
// that write their own walkthrough have to clear it rather than refuse it.
// Before the default flipped, the shapes that write their own walkthrough were
// refused beside --synopsis, and a default that still tripped those refusals
// would have made them unrunnable.
//
// Dry runs throughout: they assemble the request and call nothing.
func TestTheDefaultDescribingCallDoesNotBreakTheShapesThatReplaceIt(t *testing.T) {
	dir := worktreeSession(t)
	for _, tc := range []struct {
		name string
		o    opts
	}{
		{"the default shape", opts{}},
		{"one call, by --no-synopsis", opts{noSynopsis: true}},
		{"a fan-out", opts{cohorts: 4}},
		{"a tool loop", opts{mode: review.ModeExplore}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			o.out, o.dryRun, o.noOpen = dir, true, true
			var err error
			captureStdout(t, func() { err = cmdReview(o) })
			if err != nil {
				t.Errorf("a dry run must be accepted: %v", err)
			}
		})
	}
}

// --synopsis names the default, so passing it beside a shape that writes its
// own walkthrough asks for nothing that shape does not already do. It used to
// be refused there, by an error that only said the flag was implied; the
// shape wins and the run goes ahead.
func TestAnExplicitSynopsisBesideTheShapesThatReplaceItIsAccepted(t *testing.T) {
	dir := worktreeSession(t)
	for _, tc := range []struct {
		name string
		o    opts
	}{
		{"a fan-out", opts{cohorts: 4, synopsis: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			o.out, o.dryRun, o.noOpen = dir, true, true
			var err error
			captureStdout(t, func() { err = cmdReview(o) })
			if err != nil {
				t.Errorf("a dry run must be accepted: %v", err)
			}
		})
	}
}

// --plan and --only-cohorts work on the partition, and one cohort draws none,
// so without --cohorts above 1 each is refused by naming the flag that turns
// the split on.
func TestThePartitionFlagsNeedTheSplit(t *testing.T) {
	dir := worktreeSession(t)
	for _, tc := range []struct {
		name string
		o    opts
	}{
		{"--plan", opts{planOnly: true}},
		{"--only-cohorts", opts{onlyCohorts: "1"}},
		{"--plan at one cohort", opts{planOnly: true, cohorts: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			o.out, o.dryRun, o.noOpen = dir, true, true
			var err error
			captureStdout(t, func() { err = cmdReview(o) })
			if err == nil || !strings.Contains(err.Error(), "--cohorts") {
				t.Errorf("err = %v, want a refusal naming --cohorts", err)
			}
		})
	}
}

// --reuse-synopsis stands in for the describing call, so it is refused
// beside the flag that already decides what that call is: --no-synopsis asks
// for none. Above --cohorts 1 it
// is accepted - see TestReuseSynopsisCanStandInForAStagedRunsPartitionToo -
// but only once review.json actually carries a partition to reuse.
func TestReuseSynopsisIsRefusedBesideTheFlagsThatReplaceTheDescribingCall(t *testing.T) {
	dir := worktreeSession(t)
	for _, tc := range []struct {
		name string
		o    opts
		want string
	}{
		{"--no-synopsis", opts{reuseSynopsis: true, noSynopsis: true}, "--no-synopsis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			o.out, o.dryRun, o.noOpen = dir, true, true
			var err error
			captureStdout(t, func() { err = cmdReview(o) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want a refusal naming %s", err, tc.want)
			}
		})
	}
}

// A review.json with a walkthrough but no partition has nothing for
// --reuse-synopsis to fan a split review out over: --cohorts above 1 is
// refused, naming the flag, until review.json actually carries one.
func TestReuseSynopsisNeedsAPartitionForCohortsAboveOne(t *testing.T) {
	dir := worktreeSession(t)
	want := change.ReviewIdentity("abc123", &change.Set{Target: &target.Target{Kind: target.KindWorktree}})
	if err := review.Merge(dir+"/review.json", findings.Review{
		Revision: want, Overview: "an overview with no cohorts",
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() {
		err = cmdReview(opts{out: dir, dryRun: true, noOpen: true, reuseSynopsis: true, cohorts: 3})
	})
	if err == nil || !strings.Contains(err.Error(), "--cohorts") {
		t.Errorf("err = %v, want a refusal naming --cohorts", err)
	}
}

// --reuse-synopsis stands in for a staged run's describing call too, once
// review.json carries the partition that call drew: the split runs straight
// to the cohort calls, at no describing-call cost.
func TestReuseSynopsisCanStandInForAStagedRunsPartitionToo(t *testing.T) {
	dir := worktreeSession(t)
	want := change.ReviewIdentity("abc123", &change.Set{Target: &target.Target{Kind: target.KindWorktree}})
	if err := review.Merge(dir+"/review.json", findings.Review{
		Revision: want, Overview: "reused overview",
		Cohorts: []findings.Cohort{{Name: "a", Files: []string{"a.go"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() {
		err = cmdReview(opts{out: dir, dryRun: true, noOpen: true, reuseSynopsis: true, cohorts: 3})
	})
	if err != nil {
		t.Fatalf("a review.json carrying a partition must be accepted for --cohorts above 1: %v", err)
	}
}

// --reuse-synopsis is refused without a review.json to reuse: a caller who
// has never reviewed this session has nothing standing in for the describing
// call, and running the review anyway would be the flag doing nothing
// silently rather than what was asked for.
func TestReuseSynopsisIsRefusedWithNoReviewJSON(t *testing.T) {
	dir := worktreeSession(t)
	var err error
	captureStdout(t, func() { err = cmdReview(opts{out: dir, dryRun: true, noOpen: true, reuseSynopsis: true}) })
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %v, want a refusal naming the missing review.json", err)
	}
}

// A review.json written against a different change is not reused: a stale
// walkthrough on a new diff is a wrong report, not a saving.
func TestReuseSynopsisIsRefusedAgainstAStaleReviewJSON(t *testing.T) {
	dir := worktreeSession(t)
	if err := review.Merge(dir+"/review.json", findings.Review{
		Revision: "stale:revision", Overview: "an old walkthrough",
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() { err = cmdReview(opts{out: dir, dryRun: true, noOpen: true, reuseSynopsis: true}) })
	if err == nil || !strings.Contains(err.Error(), "stale:revision") {
		t.Errorf("err = %v, want a refusal naming the stale revision", err)
	}
}

// A review.json with no walkthrough - written by a describing call that
// broke - has nothing for --reuse-synopsis to stand in with either.
func TestReuseSynopsisIsRefusedAgainstAReviewJSONWithNoWalkthrough(t *testing.T) {
	dir := worktreeSession(t)
	want := change.ReviewIdentity("abc123", &change.Set{Target: &target.Target{Kind: target.KindWorktree}})
	if err := review.Merge(dir+"/review.json", findings.Review{Revision: want}); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() { err = cmdReview(opts{out: dir, dryRun: true, noOpen: true, reuseSynopsis: true}) })
	if err == nil || !strings.Contains(err.Error(), "no walkthrough") {
		t.Errorf("err = %v, want a refusal naming the missing walkthrough", err)
	}
}

// A review.json that matches this change and carries a walkthrough is
// accepted and its overview and files are carried onto ReuseSynopsis for the
// judging call to answer alone, at no describing-call cost.
func TestReuseSynopsisIsAcceptedAgainstAMatchingReviewJSON(t *testing.T) {
	dir := worktreeSession(t)
	want := change.ReviewIdentity("abc123", &change.Set{Target: &target.Target{Kind: target.KindWorktree}})
	if err := review.Merge(dir+"/review.json", findings.Review{
		Revision: want, Overview: "reused overview",
		Files: map[string]string{"a.go": "does a thing"},
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() { err = cmdReview(opts{out: dir, dryRun: true, noOpen: true, reuseSynopsis: true}) })
	if err != nil {
		t.Fatalf("a matching review.json must be accepted: %v", err)
	}
}
