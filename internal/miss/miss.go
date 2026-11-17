// Package miss records defects a review should have caught and did not.
//
// Dispositions and learnings cover what became of findings Redline made. A
// miss is the other direction: a bug that surfaced later, in lines a review
// had in front of it. Each entry says where the defect is, which review it
// belongs to, and what that review's archived session shows about the
// defect's lines, so a later build can be run on the same session and
// compared.
package miss

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
)

// File is the ledger's name. It sits in the same directory as the
// dispositions files.
const File = "misses.jsonl"

// Entry is one miss.
type Entry struct {
	Recorded time.Time `json:"recorded"`
	PR       int       `json:"pr"`
	// Head is the commit whose review should have caught it. Empty for a
	// defect that predates Redline, which has no session to look at.
	Head string `json:"head,omitempty"`
	File string `json:"file"`
	Line int    `json:"line"`
	// Summary is what the defect was, in the words of whoever recorded it.
	Summary string `json:"summary"`
	// FixPR is the pull request that fixed it, when there is one.
	FixPR int `json:"fixPR,omitempty"`

	// Session is the archived session directory the fields below were read
	// from, so a later build can be pointed at it. Empty when no session was
	// found, and then the fields below stay unset: they are unknown, and
	// false would claim the review had looked.
	Session string `json:"session,omitempty"`
	// FileInDiff and LineInDiff say whether the reviewed change touched the
	// file at all, and whether it added the defect's line.
	FileInDiff *bool `json:"fileInDiff,omitempty"`
	LineInDiff *bool `json:"lineInDiff,omitempty"`
	// Findings are the findings, proposed or kept, that sat on the line.
	// Lookups are the lookups that read it. Either empty means nothing in the
	// review went near the line.
	Findings []Touch `json:"findings,omitempty"`
	Lookups  []Touch `json:"lookups,omitempty"`
}

// Touch is one thing in a review that covered the defect's line.
type Touch struct {
	// By says what touched it: "finding" for a finding in the session's
	// report, "proposed" for one the reviewer wrote that the ruling may have
	// withdrawn, "read_lines" or "filed" for a lookup.
	By   string `json:"by"`
	ID   string `json:"id,omitempty"`
	Line int    `json:"line,omitempty"`
	Note string `json:"note,omitempty"`
}

// Session is what Examine reads from an archived session.
type Session struct {
	Change   *change.Set
	Findings []findings.Finding
	// Trace is the postmortem, nil when the review left none.
	Trace *postmortem.Trace
}

// Examine fills the entry's session fields from s.
func Examine(e *Entry, s Session) {
	want := clean(e.File)
	e.FileInDiff, e.LineInDiff = new(bool), new(bool)
	if s.Change != nil {
		for _, f := range s.Change.Files {
			if clean(f.Path) != want {
				continue
			}
			*e.FileInDiff = true
			*e.LineInDiff = slices.Contains(f.AddedLines, e.Line)
		}
	}
	for i := range s.Findings {
		f := &s.Findings[i]
		if clean(f.File) == want && covers(f.StartLine, f.Line, e.Line) {
			e.Findings = append(e.Findings, Touch{By: "finding", ID: f.ID, Line: f.Line, Note: f.Message})
		}
	}
	if s.Trace == nil {
		return
	}
	t := s.Trace
	for _, f := range t.Findings {
		if clean(f.File) == want && f.Line == e.Line {
			e.Findings = append(e.Findings, Touch{By: "proposed", ID: f.ID, Line: f.Line, Note: f.Body})
		}
	}
	for _, l := range t.Lookups {
		if l.Tool != review.CallRead {
			continue
		}
		var in struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		// The input is cut at a fixed length, so a long one does not parse and
		// is not counted. Nothing is claimed about what it read.
		if json.Unmarshal([]byte(l.Input), &in) == nil && clean(in.Path) == want && covers(in.StartLine, in.EndLine, e.Line) {
			e.Lookups = append(e.Lookups, Touch{By: l.Tool, Line: in.StartLine, Note: l.Input})
		}
	}
	for _, f := range t.Lookup.Filed {
		if clean(f.File) == want && f.StartLine > 0 && covers(f.StartLine, max(f.EndLine, f.StartLine), e.Line) {
			e.Lookups = append(e.Lookups, Touch{By: "filed", ID: f.Answers, Line: f.StartLine, Note: f.FoundVia})
		}
	}
}

// covers reports whether line is in the span start..end. A span with no start
// is the single line end.
func covers(start, end, line int) bool {
	if start <= 0 || start > end {
		start = end
	}
	return line > 0 && start <= line && line <= end
}

func clean(p string) string {
	return path.Clean(filepath.ToSlash(p))
}

// Append adds one entry to the ledger in dir.
func Append(dir string, e Entry) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, File), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Read returns the ledger in dir. A missing ledger is no misses, not an error.
func Read(dir string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(dir, File))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return decode(f)
}

func decode(r io.Reader) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", File, n, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
