package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A note that cannot be read, or two notes, is refused before anything is
// observed or sent.
func TestReviewRefusesANoteItCannotUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    opts
		want string
	}{
		{"both flags", opts{note: "a", noteFile: "b"}, "pass one or the other"},
		{"a missing file", opts{noteFile: filepath.Join(t.TempDir(), "absent.md")}, "--note-file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reviewNote(tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}

// The file's note and the flag's are the same note, trimmed, and a dry run
// shows it at the end of the request, where the judging call reads it.
func TestADryRunShowsTheNoteFromEitherFlag(t *testing.T) {
	dir := worktreeSession(t)
	file := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(file, []byte("\n  Look at the cache key.\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]opts{
		"--note":      {note: "Look at the cache key."},
		"--note-file": {noteFile: file},
	} {
		t.Run(name, func(t *testing.T) {
			o.out, o.dryRun, o.noSynopsis = dir, true, true
			var err error
			out := captureStdout(t, func() { err = cmdReview(o) })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Look at the cache key.\n") {
				t.Errorf("the dry run does not show the note:\n%s", out)
			}
		})
	}
}
