package cover

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAddedLinesUsesNewSideNumbering(t *testing.T) {
	diff := `diff --git a/a.go b/a.go
index 111..222 100644
--- a/a.go
+++ b/a.go
@@ -10,3 +10,5 @@ func X() {
 	ctx := context()
+	if err != nil {
+		return err
+	}
 	return nil
`
	got := AddedLines(diff)
	// Context line 10, then three added lines at 11, 12, 13.
	want := []int{11, 12, 13}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// A deleted line consumes no new-side number. Getting this wrong shifts every
// following line and attributes coverage to the wrong code.
func TestAddedLinesIgnoresDeletions(t *testing.T) {
	diff := `--- a/a.go
+++ b/a.go
@@ -1,4 +1,4 @@
 package a
-var old = 1
+var new = 1
 var keep = 2
`
	got := AddedLines(diff)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("got %v, want [2]", got)
	}
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// The failure this exists to prevent: a missing profile rendering as 0%, which
// reads as "nothing is tested" — a far stronger claim than "nobody measured".
func TestNoProfileIsNilNotZero(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")

	if got := Compute(dir, []Changed{{Path: "a.go", Added: []int{1}}}); got != nil {
		t.Fatalf("expected nil for a missing profile, got %+v", got)
	}
	if got := Locate(dir); got != "" {
		t.Fatalf("Locate found %q in an empty tree", got)
	}
}

func TestComputeCountsOnlyCoverableAddedLines(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	// Two statement blocks: lines 10-12 ran, lines 20-22 did not. Line 30 is
	// in no block at all, so it is not coverable.
	write(t, dir, "coverage.out", "mode: set\n"+
		"github.com/x/y/a.go:10.2,12.3 2 4\n"+
		"github.com/x/y/a.go:20.2,22.3 2 0\n")

	got := Compute(dir, []Changed{{Path: "a.go", Added: []int{10, 11, 20, 21, 30}}})
	if got == nil {
		t.Fatal("expected a result")
	}
	if got.Lines != 4 {
		t.Errorf("Lines = %d, want 4 — line 30 is in no block and is not coverable", got.Lines)
	}
	if got.Covered != 2 {
		t.Errorf("Covered = %d, want 2", got.Covered)
	}
	if got.Percent != 50 {
		t.Errorf("Percent = %v, want 50", got.Percent)
	}
	if len(got.Uncovered) != 1 || got.Uncovered[0].Path != "a.go" {
		t.Fatalf("uncovered: %+v", got.Uncovered)
	}
	if len(got.Uncovered[0].Lines) != 2 {
		t.Errorf("expected lines 20 and 21 named, got %v", got.Uncovered[0].Lines)
	}
	if got.Profile != "coverage.out" {
		t.Errorf("Profile = %q — the reader needs to know where the number came from", got.Profile)
	}
	// The whole file has 6 coverable lines (10-12 ran, 20-22 did not); the
	// diff only touched 4 of them. Total must reflect the whole profile, not
	// just what this change added.
	if got.TotalLines != 6 {
		t.Errorf("TotalLines = %d, want 6", got.TotalLines)
	}
	if got.TotalCovered != 3 {
		t.Errorf("TotalCovered = %d, want 3", got.TotalCovered)
	}
	if got.TotalPercent != 50 {
		t.Errorf("TotalPercent = %v, want 50", got.TotalPercent)
	}
}

// A file the diff never touches still counts toward the repository-wide
// total: that number answers "how tested is the codebase", not "how tested
// is the change".
func TestComputeTotalSpansFilesTheDiffNeverTouched(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "b.go", "package b\n")
	write(t, dir, "coverage.out", "mode: set\n"+
		"github.com/x/y/a.go:10.2,10.3 1 1\n"+
		"github.com/x/y/b.go:20.2,20.3 1 0\n")

	got := Compute(dir, []Changed{{Path: "a.go", Added: []int{10}}})
	if got == nil {
		t.Fatal("expected a result")
	}
	if got.Lines != 1 || got.Covered != 1 {
		t.Fatalf("diff coverage should see only a.go: Lines=%d Covered=%d", got.Lines, got.Covered)
	}
	if got.TotalLines != 2 || got.TotalCovered != 1 {
		t.Fatalf("total must include b.go too: TotalLines=%d TotalCovered=%d", got.TotalLines, got.TotalCovered)
	}
	if got.TotalPercent != 50 {
		t.Errorf("TotalPercent = %v, want 50", got.TotalPercent)
	}
}

// A file the profile never mentions may simply not have been in the test run.
// Counting its lines as uncovered would invent a failure.
func TestFileAbsentFromProfileIsNotCountedAsUncovered(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "b.go", "package b\n")
	write(t, dir, "coverage.out", "mode: set\ngithub.com/x/y/a.go:1.1,2.2 1 1\n")

	got := Compute(dir, []Changed{
		{Path: "a.go", Added: []int{1}},
		{Path: "b.go", Added: []int{1, 2, 3}},
	})
	if got.Lines != 1 {
		t.Errorf("Lines = %d, want 1: b.go is absent from the profile, not uncovered", got.Lines)
	}
	if got.Percent != 100 {
		t.Errorf("Percent = %v, want 100", got.Percent)
	}
}

func TestNoCoverableLinesIsNotZeroPercent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "coverage.out", "mode: set\ngithub.com/x/y/a.go:10.1,12.2 1 1\n")

	got := Compute(dir, []Changed{{Path: "a.go", Added: []int{1, 2}}})
	if got.Lines != 0 {
		t.Fatalf("Lines = %d, want 0", got.Lines)
	}
	if got.Percent != -1 {
		t.Errorf("Percent = %v, want -1 so the report can say 'nothing coverable' rather than 0%%", got.Percent)
	}
}

func TestOverlappingBlocksTakeTheOptimisticReading(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "coverage.out", "mode: set\n"+
		"github.com/x/y/a.go:5.1,9.2 1 0\n"+
		"github.com/x/y/a.go:6.1,7.2 1 3\n")

	got := Compute(dir, []Changed{{Path: "a.go", Added: []int{6}}})
	if got.Covered != 1 {
		t.Errorf("a line covered by any executed block counts as covered, got %+v", got)
	}
}

// A profile written before the code cannot be describing it, and a number that
// looks authoritative is worse than no number.
func TestStaleProfileIsFlagged(t *testing.T) {
	dir := t.TempDir()
	profile := write(t, dir, "coverage.out", "mode: set\ngithub.com/x/y/a.go:1.1,3.2 1 1\n")
	source := write(t, dir, "a.go", "package a\n")

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(profile, old, old); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(source, now, now); err != nil {
		t.Fatal(err)
	}

	got := Compute(dir, []Changed{{Path: "a.go", Added: []int{1}}})
	if got == nil || !got.Stale {
		t.Fatalf("expected the profile to be flagged stale: %+v", got)
	}

	// Fresh the other way round.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(profile, future, future); err != nil {
		t.Fatal(err)
	}
	if got := Compute(dir, []Changed{{Path: "a.go", Added: []int{1}}}); got.Stale {
		t.Error("a profile newer than the change is not stale")
	}
}

func TestMalformedProfileLinesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "coverage.out", "mode: set\n"+
		"garbage\n"+
		"github.com/x/y/a.go:notanumber,12.3 2 4\n"+
		"github.com/x/y/a.go:10.2,12.3 2 4\n")

	got := Compute(dir, []Changed{{Path: "a.go", Added: []int{10}}})
	if got == nil || got.Covered != 1 {
		t.Fatalf("a malformed line should degrade the number, not the run: %+v", got)
	}
}

func TestOnlyGoFilesAreConsidered(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "coverage.out", "mode: set\ngithub.com/x/y/a.go:1.1,3.2 1 1\n")

	got := Compute(dir, []Changed{{Path: "README.md", Added: []int{1, 2, 3}}})
	if got.Lines != 0 {
		t.Errorf("a markdown file has no coverage, got %+v", got)
	}
}

// CRLF line endings must not shift line numbers or break hunk-header
// parsing: the "+" and context prefixes are unaffected by a trailing \r, and
// the header's numbers appear before it.
func TestAddedLinesHandlesCRLF(t *testing.T) {
	diff := "@@ -1,3 +1,3 @@\r\n ctx\r\n+add\r\n ctx2\r\n"
	got := AddedLines(diff)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("got %v, want [2]", got)
	}
}

// Git's "no newline at end of file" marker must not be mistaken for content:
// it consumes no new-side line number and does not disturb a hunk that
// follows it.
func TestAddedLinesSkipsNoNewlineMarker(t *testing.T) {
	diff := "@@ -1,2 +1,2 @@\n+add\n\\ No newline at end of file\n" +
		"@@ -10,1 +11,2 @@\n+second\n"
	got := AddedLines(diff)
	want := []int{1, 11}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A pure rename has no hunk at all, so it must add no lines — a rename must
// never be attributed coverage for lines it never touched.
func TestAddedLinesEmptyForPureRename(t *testing.T) {
	diff := "diff --git a/old.go b/new.go\nsimilarity index 100%\n" +
		"rename from old.go\nrename to new.go\n"
	if got := AddedLines(diff); len(got) != 0 {
		t.Fatalf("a pure rename should add no lines, got %v", got)
	}
}

// A deleted file's hunk removes every line and adds none; the new side is
// empty ("+0,0"), and no "+" line should ever appear for it.
func TestAddedLinesEmptyForDeletedFile(t *testing.T) {
	diff := "--- a/x.go\n+++ /dev/null\n@@ -1,3 +0,0 @@\n-a\n-b\n-c\n"
	if got := AddedLines(diff); len(got) != 0 {
		t.Fatalf("a deleted file should add no lines, got %v", got)
	}
}

// Multiple hunks in one file must each restart from their own header's
// new-side start, not continue counting from the previous hunk's end.
func TestAddedLinesAcrossMultipleHunks(t *testing.T) {
	diff := "--- a/a.go\n+++ b/a.go\n" +
		"@@ -1,2 +1,3 @@\n ctx\n+first\n ctx2\n" +
		"@@ -20,2 +21,3 @@\n ctx\n+second\n ctx2\n"
	got := AddedLines(diff)
	want := []int{2, 22}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// An added line whose content itself begins with "++" renders as "+++…" in
// the diff. Inside a hunk that is code (routine in JS: "++i;"), not a file
// header; headers live between "diff " and "@@". Skipping it would shift
// every added-line number after it in the hunk.
func TestAddedLinesCountsPlusPlusContent(t *testing.T) {
	diff := "--- a/a.js\n+++ b/a.js\n@@ -1,2 +1,4 @@\n line1\n+++i;\n+--j;\n line2\n"
	got := AddedLines(diff)
	want := []int{2, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
