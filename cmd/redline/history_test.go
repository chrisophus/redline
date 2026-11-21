package main

import (
	"strings"
	"testing"
)

// history reads the directory that holds every session, so it runs with no
// session to resolve and on a directory that has none yet.
func TestHistoryRunsWithoutASession(t *testing.T) {
	dir := t.TempDir()
	if err := runMain([]string{"history", "--out", dir}); err != nil {
		t.Fatalf("history on an empty directory: %v", err)
	}
	if err := runMain([]string{"history", "--out", dir, "--format", "json"}); err != nil {
		t.Fatalf("history --format json: %v", err)
	}
	err := runMain([]string{"history", "--out", dir, "--format", "yaml"})
	if err == nil || !strings.Contains(err.Error(), "report or json") {
		t.Errorf("an unknown format gave %v", err)
	}
}
