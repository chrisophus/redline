package post

import (
	"strings"
	"testing"
)

// FuzzCommentableLines fuzzes the diff-position mapping in diff.go with
// arbitrary bytes standing in for a GitHub "patch" field. This mapping is
// load-bearing for safety, not just correctness: a wrong position puts a
// review comment on the wrong line, and GitHub 422s the whole review if any
// comment targets a line outside the diff — so a false-commentable position
// is exactly the failure CommentableLines exists to prevent.
//
// KNOWN BUG, deliberately not seeded here (a seed corpus entry is replayed as
// a normal test on every `go test`, so a permanently-failing one would break
// the build): hunkHeader's capture group `\+(\d+)` accepts "0" as a new-file
// start line, which is not a valid 1-indexed line. A hunk header claiming to
// start at line 0, followed by a "+" or context line, marks line 0
// commentable (diff.go:39-40, diff.go:48-60). Repro:
//
//	CommentableLines(map[string]string{"a.go": "@@ -0 +0 @@\n 0000"})["a.go"] == map[int]bool{0: true} // minimized by `go test -fuzz`
//
// See the final report's "Bugs found" section.
func FuzzCommentableLines(f *testing.F) {
	// The real multi-hunk fixture from TestCommentableLinesParsesHunks.
	f.Add(hunkPatch)
	// Same patch with the trailing newline GitHub's API does not send today,
	// covered by TestCommentableLinesIgnoresTrailingNewline.
	f.Add(hunkPatch + "\n")
	// A header the regex does not match at all: exercises the "not a hunk"
	// path without a following +/context line, so it stays commentable-free.
	f.Add("@@ this is not a hunk header @@\n-removed\n")
	// CRLF line endings.
	f.Add("@@ -1,3 +1,3 @@\r\n ctx\r\n+add\r\n ctx2\r\n")
	// The "no newline at end of file" marker.
	f.Add("@@ -1,2 +1,2 @@\n+add\n\\ No newline at end of file")
	// An absurd new-file start number, immediately followed by an added
	// line. Safe under the invariants below (a single position is bounded by
	// a single '+' line), but the shape of input that makes a nonsense
	// position look plausible.
	f.Add("@@ -1,1 +999999999,1 @@\n+x")
	// Empty and header-only patches: no content line ever follows a hunk
	// header, so nothing should ever become commentable.
	f.Add("")
	f.Add("@@ -1,3 +1,3 @@\n")

	f.Fuzz(func(t *testing.T, patch string) {
		got := CommentableLines(map[string]string{"a.go": patch})["a.go"]

		// Every commentable position must be a valid 1-indexed file line.
		for ln := range got {
			if ln <= 0 {
				t.Fatalf("CommentableLines(%q) marked non-positive line %d commentable: %v", patch, ln, got)
			}
		}

		// commentableInPatch only ever marks a line commentable from a "+" or
		// a literal " " (context) line. A patch with neither can never
		// produce a commentable line — this is the general form of "never
		// returns a position for an empty or header-only diff".
		hasContentLine := false
		for _, line := range strings.Split(patch, "\n") {
			if (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) || strings.HasPrefix(line, " ") {
				hasContentLine = true
				break
			}
		}
		if !hasContentLine && len(got) != 0 {
			t.Fatalf("CommentableLines(%q) produced %v with no '+' or context line in the input", patch, got)
		}

		// Cardinality bound: the function can never mark more lines
		// commentable than there are '+' (excluding "+++") and context " "
		// lines in the raw patch, since those are the only two cases that
		// populate the result.
		plusAndCtx := 0
		for _, line := range strings.Split(patch, "\n") {
			if strings.HasPrefix(line, "+++") {
				continue
			}
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, " ") {
				plusAndCtx++
			}
		}
		if len(got) > plusAndCtx {
			t.Fatalf("CommentableLines(%q) marked %d lines commentable but input has only %d '+'/context lines: %v", patch, len(got), plusAndCtx, got)
		}
	})
}
