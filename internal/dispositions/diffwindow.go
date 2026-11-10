package dispositions

import (
	"regexp"
	"strconv"
	"strings"
)

// Hunk is one `git diff -U0` hunk: OldCount lines starting at OldStart were replaced by NewCount lines
// starting at NewStart. A pure insertion has OldCount 0 and OldStart naming the line it follows.
type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseHunks reads the hunk headers of a `git diff -U0` for one file.
func ParseHunks(diff string) []Hunk {
	var out []Hunk
	for line := range strings.SplitSeq(diff, "\n") {
		m := hunkRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		h := Hunk{OldStart: atoi(m[1]), OldCount: 1, NewStart: atoi(m[3]), NewCount: 1}
		if m[2] != "" {
			h.OldCount = atoi(m[2])
		}
		if m[4] != "" {
			h.NewCount = atoi(m[4])
		}
		out = append(out, h)
	}
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Window is an inclusive line range in one version of a file.
type Window struct{ Start, End int }

// AnchorWindow is the range a finding is anchored to: line ± radius, widened to startLine..line when the
// finding carries a start line. It never starts before line 1.
func AnchorWindow(line, startLine, radius int) Window {
	w := Window{Start: line - radius, End: line + radius}
	if startLine > 0 && startLine <= line {
		w = Window{Start: startLine, End: line}
	}
	if w.Start < 1 {
		w.Start = 1
	}
	return w
}

// Overlaps reports whether any hunk touched lines of the old file inside the window. An insertion touches
// the line it follows and the next one, so an insertion at the edge of the window counts.
func (w Window) Overlaps(hunks []Hunk) bool {
	for _, h := range hunks {
		start, end := h.OldStart, h.OldStart+h.OldCount-1
		if h.OldCount == 0 {
			start, end = h.OldStart, h.OldStart+1
		}
		if start <= w.End && end >= w.Start {
			return true
		}
	}
	return false
}

// Forward maps the window from the old file to the new file across the hunks, so edits above the window
// shift it instead of leaving it pointing at the wrong lines. Edits inside the window stretch or shrink it.
func (w Window) Forward(hunks []Hunk) Window {
	shiftStart, shiftEnd := 0, 0
	for _, h := range hunks {
		delta := h.NewCount - h.OldCount
		if h.OldCount == 0 {
			// Pure insertion after line OldStart.
			if h.OldStart < w.Start {
				shiftStart += delta
				shiftEnd += delta
			} else if h.OldStart < w.End {
				shiftEnd += delta
			}
			continue
		}
		last := h.OldStart + h.OldCount - 1
		switch {
		case last < w.Start:
			shiftStart += delta
			shiftEnd += delta
		case h.OldStart <= w.End:
			// The hunk overlaps the window: the window absorbs the change.
			shiftEnd += delta
		}
	}
	out := Window{Start: w.Start + shiftStart, End: w.End + shiftEnd}
	if out.Start < 1 {
		out.Start = 1
	}
	if out.End < out.Start {
		out.End = out.Start
	}
	return out
}
