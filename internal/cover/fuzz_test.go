package cover

import (
	"strings"
	"testing"
)

// splitFiles splits a multi-file unified diff into the segments AddedLines
// treats as separate files: a "diff " line is the only thing that resets
// AddedLines' internal state (see the "diff " case), so re-running AddedLines
// on each segment reproduces exactly the contribution that segment made to a
// single whole-diff call. It lets the fuzz target check "strictly increasing
// within a file" without re-implementing the parser.
func splitFiles(diff string) []string {
	var segs []string
	var cur []string
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "diff ") && len(cur) > 0 {
			segs = append(segs, strings.Join(cur, "\n"))
			cur = nil
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		segs = append(segs, strings.Join(cur, "\n"))
	}
	return segs
}

// FuzzAddedLines fuzzes the unified-diff parser with arbitrary bytes. The
// text AddedLines parses is exactly the pull request diff, i.e. attacker- or
// at least author-controlled content, so this is the highest-value fuzz
// target in the package.
//
// KNOWN BUG, deliberately not seeded here (it would make every `go test` run
// fail forever, since a seed corpus entry is replayed as a normal test): a
// hunk header that fails to yield a parseable "+N" start (hunkStart's
// fallback in cover.go:177-192) leaves newLine at 0, and an immediate "+"
// line right after such a header is then reported as added *line 0*
// (cover.go:162-164), which cannot exist in any file. Repro:
//
//	AddedLines("@@\n+") == []int{0} // minimized by `go test -fuzz` in ~10ms
//
// See the final report's "Bugs found" section.
func FuzzAddedLines(f *testing.F) {
	// A real hunk, from TestAddedLinesUsesNewSideNumbering.
	f.Add(`diff --git a/a.go b/a.go
index 111..222 100644
--- a/a.go
+++ b/a.go
@@ -10,3 +10,5 @@ func X() {
 	ctx := context()
+	if err != nil {
+		return err
+	}
 	return nil
`)
	// A malformed header with no line added directly after it: exercises the
	// hunkStart failure path without tripping the positivity invariant below.
	f.Add("@@ this is not a hunk header @@\n unchanged context\n-removed old\n")
	// CRLF line endings, as a diff generated or transported on Windows might
	// carry.
	f.Add("@@ -1,3 +1,3 @@\r\n ctx\r\n+add\r\n ctx2\r\n")
	// The "no newline at end of file" marker Git appends after a hunk's final
	// line when that line has none.
	f.Add("@@ -1,2 +1,2 @@\n+add\n\\ No newline at end of file\n")
	// A hunk header with an absurd new-file start number. This alone is safe
	// under the invariants below (a single reported value is trivially
	// "increasing", and 1 <= 1 '+' line), but it is the shape of input that
	// can make a wildly wrong line number look plausible downstream.
	f.Add("@@ -1,1 +999999999,1 @@\n+x\n")
	// A deleted file: every line removed, new side length 0.
	f.Add("--- a/x.go\n+++ /dev/null\n@@ -1,3 +0,0 @@\n-a\n-b\n-c\n")
	// A pure rename with no content hunk at all.
	f.Add("diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n")
	// Two files, each with an ordinary increasing hunk.
	f.Add("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n+one\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -5,1 +5,2 @@\n+two\n")

	f.Fuzz(func(t *testing.T, diff string) {
		got := AddedLines(diff)

		// Every reported new-side line number must be a valid 1-indexed file
		// line. Line 0 (or negative) cannot exist in any file and would
		// corrupt whatever the caller keys on it (coverage lookup, a posted
		// review comment).
		for _, ln := range got {
			if ln <= 0 {
				t.Fatalf("AddedLines(%q) reported non-positive line %d: %v", diff, ln, got)
			}
		}

		// Within one file, new-side line numbers must strictly increase: each
		// "+" line consumes exactly the next new-side number, and numbers
		// never repeat or go backwards inside a single file's hunks.
		for _, seg := range splitFiles(diff) {
			segLines := AddedLines(seg)
			for i := 1; i < len(segLines); i++ {
				if segLines[i] <= segLines[i-1] {
					t.Fatalf("AddedLines line numbers must strictly increase within a file, got %v for segment %q", segLines, seg)
				}
			}
		}

		// Cardinality bound: AddedLines only ever appends inside the "+" case
		// (excluding the "+++" file-header line), so it can never report more
		// added lines than there are literal '+'-prefixed content lines in
		// the raw input.
		plusLines := 0
		for _, line := range strings.Split(diff, "\n") {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				plusLines++
			}
		}
		if len(got) > plusLines {
			t.Fatalf("AddedLines(%q) reported %d lines but input has only %d '+' lines: %v", diff, len(got), plusLines, got)
		}
	})
}
