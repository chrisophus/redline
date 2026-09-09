package cover

import "testing"

// The two are used as a matched pair by the review prompt: line numbers from
// one, text from the other. A caller holding numbers that disagree with the
// text points the reviewer at the wrong code, so they have to walk the diff
// identically. They did not, on the two cases walkAdded documents.
func TestAddedLinesAndAddedLineTextAgree(t *testing.T) {
	diffs := []string{
		// A "+++" inside a hunk is added code, not a file header.
		"@@ -1,1 +1,3 @@\n context\n+++i;\n+after\n",
		// The no-newline marker occupies no line of its own.
		"@@ -1,1 +1,2 @@\n context\n+added\n\\ No newline at end of file\n",
		// Two files in one diff, so the "diff " reset is exercised.
		"@@ -1,0 +1,1 @@\n+first\ndiff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -5,0 +6,1 @@\n+second\n",
		// A malformed header names no real line and is skipped by both.
		"@@ bad header @@\n+ignored\n@@ -1,0 +2,1 @@\n+kept\n",
	}
	for _, d := range diffs {
		lines := AddedLines(d)
		text := AddedLineText(d)
		if len(lines) != len(text) {
			t.Errorf("AddedLines gave %d lines and AddedLineText %d for:\n%s", len(lines), len(text), d)
			continue
		}
		for _, n := range lines {
			if _, ok := text[n]; !ok {
				t.Errorf("line %d has a number but no text for:\n%s", n, d)
			}
		}
	}
}

func TestAddedLineTextKeepsTheLineWithoutItsMarker(t *testing.T) {
	got := AddedLineText("@@ -1,0 +1,1 @@\n+\tif err != nil {\n")
	if got[1] != "\tif err != nil {" {
		t.Errorf("text = %q, want the line without its + marker", got[1])
	}
}
