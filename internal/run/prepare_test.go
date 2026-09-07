package run

import "testing"

func TestHarnessProduceRoot(t *testing.T) {
	if got := harnessProduceRoot("/origin", "/origin"); got != "/origin" {
		t.Fatalf("same tree: %q", got)
	}
	if got := harnessProduceRoot("/origin", "/worktree"); got != "/worktree" {
		t.Fatalf("detached: %q", got)
	}
}
