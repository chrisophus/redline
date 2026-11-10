package dispositions

import (
	"testing"
)

func TestParseHunks(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/x b/x\n@@ -10,2 +10,3 @@ func f() {\n-a\n-b\n+c\n+d\n+e\n@@ -40 +41,0 @@\n-gone\n@@ -50,0 +50,2 @@\n+x\n+y\n"
	assert.Equal(t, []Hunk{
		{OldStart: 10, OldCount: 2, NewStart: 10, NewCount: 3},
		{OldStart: 40, OldCount: 1, NewStart: 41, NewCount: 0},
		{OldStart: 50, OldCount: 0, NewStart: 50, NewCount: 2},
	}, ParseHunks(diff))
	assert.Empty(t, ParseHunks("no hunks here"))
}

func TestAnchorWindow(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Window{95, 105}, AnchorWindow(100, 0, 5))
	assert.Equal(t, Window{1, 8}, AnchorWindow(3, 0, 5), "clamped to line 1")
	assert.Equal(t, Window{90, 100}, AnchorWindow(100, 90, 5), "startLine widens the window")
	assert.Equal(t, Window{95, 105}, AnchorWindow(100, 120, 5), "a start line after the line is ignored")
}

func TestWindowOverlaps(t *testing.T) {
	t.Parallel()
	w := Window{95, 105}
	cases := []struct {
		name string
		h    Hunk
		want bool
	}{
		{"inside", Hunk{OldStart: 100, OldCount: 1, NewStart: 100, NewCount: 1}, true},
		{"above", Hunk{OldStart: 10, OldCount: 3, NewStart: 10, NewCount: 3}, false},
		{"below", Hunk{OldStart: 200, OldCount: 3, NewStart: 200, NewCount: 3}, false},
		{"straddles start", Hunk{OldStart: 93, OldCount: 4, NewStart: 93, NewCount: 4}, true},
		{"insertion after last line of window", Hunk{OldStart: 105, OldCount: 0, NewStart: 106, NewCount: 2}, true},
		{"insertion just before window", Hunk{OldStart: 93, OldCount: 0, NewStart: 94, NewCount: 1}, false},
		{"insertion following the line before the window", Hunk{OldStart: 94, OldCount: 0, NewStart: 95, NewCount: 1}, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, w.Overlaps([]Hunk{tc.h}), tc.name)
	}
}

func TestWindowForward(t *testing.T) {
	t.Parallel()
	w := Window{95, 105}
	// Three lines added above shift the window down by three.
	assert.Equal(t, Window{98, 108}, w.Forward([]Hunk{{OldStart: 10, OldCount: 0, NewStart: 11, NewCount: 3}}))
	// Two lines removed above shift it up by two.
	assert.Equal(t, Window{93, 103}, w.Forward([]Hunk{{OldStart: 10, OldCount: 2, NewStart: 10, NewCount: 0}}))
	// An edit below leaves it alone.
	assert.Equal(t, w, w.Forward([]Hunk{{OldStart: 200, OldCount: 1, NewStart: 200, NewCount: 4}}))
	// An edit inside grows the window's end.
	assert.Equal(t, Window{95, 107}, w.Forward([]Hunk{{OldStart: 100, OldCount: 1, NewStart: 100, NewCount: 3}}))
	// Hunks apply in order across several edits.
	assert.Equal(t, Window{96, 106}, w.Forward([]Hunk{
		{OldStart: 5, OldCount: 0, NewStart: 6, NewCount: 2},
		{OldStart: 20, OldCount: 1, NewStart: 22, NewCount: 0},
	}))
	// A hunk that swallows the whole window leaves a one-line window at its start. Outside a merge commit that
	// edit is already reported as a fix before the window is carried forward, so this only keeps it valid.
	assert.Equal(t, Window{2, 2}, Window{2, 3}.Forward([]Hunk{{OldStart: 1, OldCount: 10, NewStart: 1, NewCount: 0}}))
	// A removal above the window moves it up but never above line 1.
	assert.Equal(t, Window{1, 5}, Window{2, 6}.Forward([]Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 0}}))
}
