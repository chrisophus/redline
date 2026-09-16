package main

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/review"
)

// The describing call runs on every review that can have one, so the shapes
// that write their own walkthrough have to clear it rather than refuse it.
// Before the default flipped, --brief and --pipeline stepwise were refused
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
		{"a fan-out", opts{pipeline: review.PipelineStaged}},
		{"a conversation", opts{pipeline: review.PipelineStepwise}},
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

// Asking for it explicitly beside a shape that writes its own walkthrough is
// still a contradiction, and still says so. The refusals now read the flag
// rather than the resolved option, which is the whole of what makes the
// default safe.
func TestAnExplicitSynopsisIsStillRefusedBesideTheShapesThatReplaceIt(t *testing.T) {
	dir := worktreeSession(t)
	for _, tc := range []struct {
		name string
		o    opts
	}{
		{"the short prompt", opts{brief: true, synopsis: true}},
		{"a conversation", opts{pipeline: review.PipelineStepwise, synopsis: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			o.out, o.dryRun, o.noOpen = dir, true, true
			var err error
			captureStdout(t, func() { err = cmdReview(o) })
			if err == nil || !strings.Contains(err.Error(), "--synopsis") {
				t.Errorf("err = %v, want a refusal naming --synopsis", err)
			}
		})
	}
}
