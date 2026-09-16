package main

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/review"
)

// The describing call runs on every review that can have one, so the shapes
// that write their own walkthrough have to clear it rather than refuse it.
// Before the default flipped, --brief was refused
// beside --synopsis, and a default that still tripped those refusals would
// have made both shapes unrunnable.
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
		{"the short prompt", opts{brief: true}},
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
		{"the short prompt", opts{brief: true, synopsis: true}},
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
