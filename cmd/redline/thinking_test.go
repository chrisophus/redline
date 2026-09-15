package main

import (
	"strings"
	"testing"
)

func TestThinkingIsRefusedBesideBriefAndExplore(t *testing.T) {
	dir := worktreeSession(t)
	for _, o := range []opts{
		{out: dir, dryRun: true, noOpen: true, thinking: true, brief: true},
		{out: dir, dryRun: true, noOpen: true, thinking: true, mode: "explore"},
	} {
		var err error
		captureStdout(t, func() { err = cmdReview(o) })
		if err == nil || !strings.Contains(err.Error(), "--thinking") {
			t.Errorf("brief=%v mode=%q: %v, want --thinking refused", o.brief, o.mode, err)
		}
	}
}
