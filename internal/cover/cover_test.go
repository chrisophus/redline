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
