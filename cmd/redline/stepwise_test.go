package main

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/review"
)

// Stepwise replaces the shapes it would otherwise combine with, and each is
// refused by name. A dry run calls nothing, so this needs no key.
func TestReviewRefusesStepwiseBesideTheShapesItReplaces(t *testing.T) {
	dir := worktreeSession(t)
	for _, tc := range []struct {
		name string
		o    opts
		want string
	}{
		{"brief", opts{brief: true}, "--brief"},
		{"synopsis", opts{synopsis: true}, "--synopsis"},
		{"explore", opts{mode: review.ModeExplore}, "--mode explore"},
		{"the openai wire", opts{api: review.APIOpenAI}, "Anthropic wire"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			o.out, o.dryRun, o.noOpen = dir, true, true
			o.pipeline = review.PipelineStepwise
			var err error
			captureStdout(t, func() { err = cmdReview(o) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
	var err error
	captureStdout(t, func() {
		err = cmdReview(opts{out: dir, dryRun: true, noOpen: true, pipeline: review.PipelineStepwise})
	})
	if err != nil {
		t.Errorf("a stepwise dry run on its own must be accepted: %v", err)
	}
}
