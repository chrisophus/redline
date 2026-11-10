package dispositions

import (
	"slices"
	"testing"
)

func TestParseHunks(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/x b/x\n@@ -10,2 +10,3 @@ func f() {\n-a\n-b\n+c\n+d\n+e\n@@ -40 +41,0 @@\n-gone\n@@ -50,0 +50,2 @@\n+x\n+y\n"
	want := []Hunk{
		{OldStart: 10, OldCount: 2, NewStart: 10, NewCount: 3},
		{OldStart: 40, OldCount: 1, NewStart: 41, NewCount: 0},
		{OldStart: 50, OldCount: 0, NewStart: 50, NewCount: 2},
	}
	if got := ParseHunks(diff); !slices.Equal(got, want) {
		t.Errorf("ParseHunks = %+v, want %+v", got, want)
	}
	if got := ParseHunks("no hunks here"); len(got) != 0 {
		t.Errorf("ParseHunks without hunks = %+v, want none", got)
	}
}

func TestAnchorWindow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                 string
		line, startLine, pad int
		want                 Window
	}{
		{"padded both ways", 100, 0, 5, Window{95, 105}},
		{"clamped to line 1", 3, 0, 5, Window{1, 8}},
		{"startLine widens the window", 100, 90, 5, Window{90, 100}},
		{"a start line after the line is ignored", 100, 120, 5, Window{95, 105}},
	}
	for _, tc := range cases {
		if got := AnchorWindow(tc.line, tc.startLine, tc.pad); got != tc.want {
			t.Errorf("%s: AnchorWindow(%d, %d, %d) = %v, want %v", tc.name, tc.line, tc.startLine, tc.pad, got, tc.want)
		}
	}
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
		if got := w.Overlaps([]Hunk{tc.h}); got != tc.want {
			t.Errorf("%s: Overlaps = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWindowForward(t *testing.T) {
	t.Parallel()
	w := Window{95, 105}
	cases := []struct {
		name  string
		from  Window
		hunks []Hunk
		want  Window
	}{
		{"three lines added above shift it down by three", w,
			[]Hunk{{OldStart: 10, OldCount: 0, NewStart: 11, NewCount: 3}}, Window{98, 108}},
		{"two lines removed above shift it up by two", w,
			[]Hunk{{OldStart: 10, OldCount: 2, NewStart: 10, NewCount: 0}}, Window{93, 103}},
		{"an edit below leaves it alone", w,
			[]Hunk{{OldStart: 200, OldCount: 1, NewStart: 200, NewCount: 4}}, w},
		{"an edit inside grows its end", w,
			[]Hunk{{OldStart: 100, OldCount: 1, NewStart: 100, NewCount: 3}}, Window{95, 107}},
		{"hunks apply in order", w, []Hunk{
			{OldStart: 5, OldCount: 0, NewStart: 6, NewCount: 2},
			{OldStart: 20, OldCount: 1, NewStart: 22, NewCount: 0},
		}, Window{96, 106}},
		// Outside a merge commit that edit is already reported as a fix before
		// the window is carried forward, so this only keeps the window valid.
		{"a hunk that swallows the window leaves one line at its start", Window{2, 3},
			[]Hunk{{OldStart: 1, OldCount: 10, NewStart: 1, NewCount: 0}}, Window{2, 2}},
		{"a removal above never moves it above line 1", Window{2, 6},
			[]Hunk{{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 0}}, Window{1, 5}},
	}
	for _, tc := range cases {
		if got := tc.from.Forward(tc.hunks); got != tc.want {
			t.Errorf("%s: %v.Forward = %v, want %v", tc.name, tc.from, got, tc.want)
		}
	}
}
