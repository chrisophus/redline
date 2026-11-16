// Package miss keeps the ledger of defects a review should have reported and
// did not.
//
// dispositions says what became of the findings Redline made. This is the
// other half: a bug that surfaced later, traced back to the head that
// introduced it. Each entry says whether the bug's line was inside the diff
// that head's review was shown, and whether a finding or a lookup went near
// it, worked out from the session archived for that head. Without that, a
// miss reads the same whether the review never saw the line or saw it and
// said nothing.
package miss

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/run"
)

// File is the ledger's name, one JSON object per line, next to the files
// `redline dispositions` writes.
const File = "misses.jsonl"

// Entry is one miss.
//
// The four *bool fields are null when no session was archived for the head,
// which says "not worked out" and is different from false.
type Entry struct {
	Recorded time.Time `json:"recorded"`
	PR       int       `json:"pr"`
	// Head is the full commit of the archived session when there is one, and
	// otherwise what the caller gave.
	Head    string `json:"head,omitempty"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Summary string `json:"summary"`
	// FixPR is the pull request that fixed the defect.
	FixPR int `json:"fixPR,omitempty"`
	// Session is the archived session the checks below were read from. A
	// later build can be run against it to see whether it now catches this.
	Session string `json:"session,omitempty"`

	// FileInDiff is true when the reviewed change touched the file, and
	// LineInDiff when the line was one the change added.
	FileInDiff *bool `json:"fileInDiff"`
	LineInDiff *bool `json:"lineInDiff"`
	// FindingTouched is true when a finding, kept or not, anchored on the
	// line. LookupTouched is true when a lookup read the line, or asked about
	// the file.
	FindingTouched *bool `json:"findingTouched"`
	LookupTouched  *bool `json:"lookupTouched"`
	// Touched says which findings and lookups the two flags above rest on.
	Touched []string `json:"touched,omitempty"`
}

// Examine fills in the entry's checks from the session saved in dir. The
// postmortem beside it is read when it exists: a head that was observed and
// never reviewed has none, and then no finding and no lookup touched the line.
func Examine(e *Entry, dir string) error {
	res, err := run.LoadSession(dir)
	if err != nil {
		return err
	}
	e.Session = dir
	fileIn, lineIn := false, false
	if res.Change != nil {
		for _, f := range res.Change.Files {
			if f.Path != e.File {
				continue
			}
			fileIn = true
			lineIn = slices.Contains(f.AddedLines, e.Line)
		}
	}
	e.FileInDiff, e.LineInDiff = &fileIn, &lineIn

	var touched []string
	findingHit, lookupHit := false, false
	for _, f := range res.Report.Findings {
		if covers(f.File, f.StartLine, f.Line, e) {
			findingHit = true
			touched = append(touched, "finding "+f.ID)
		}
	}
	tr, err := postmortem.Load(dir)
	if err == nil {
		for _, f := range tr.Findings {
			if covers(f.File, 0, f.Line, e) {
				findingHit = true
				touched = append(touched, "proposed "+f.ID)
			}
		}
		for _, l := range tr.Lookup.Filed {
			if covers(l.File, l.StartLine, l.EndLine, e) {
				lookupHit = true
				touched = append(touched, fmt.Sprintf("lookup read %s:%d-%d", l.File, l.StartLine, l.EndLine))
			}
		}
		for _, l := range tr.Lookup.Resolved {
			if covers(l.File, l.StartLine, l.EndLine, e) {
				lookupHit = true
				touched = append(touched, fmt.Sprintf("lookup resolved %s:%d-%d", l.File, l.StartLine, l.EndLine))
			}
		}
		for _, l := range tr.Lookups {
			// The trace keeps a lookup's arguments and not what came back, so
			// naming the file is listed but does not count as reading the line.
			if strings.Contains(l.Input, e.File) {
				touched = append(touched, fmt.Sprintf("%s named %s, no line", l.Tool, e.File))
			}
		}
	}
	e.FindingTouched, e.LookupTouched = &findingHit, &lookupHit
	e.Touched = touched
	return nil
}

// covers reports whether a location in e's file includes e's line. A record
// with no line range stands for the whole file, so it does not count: saying a
// lookup "touched" a line it only named the file of would flatter the review.
// A single line is its own range.
func covers(file string, start, end int, e *Entry) bool {
	if end == 0 {
		end = start
	}
	if file != e.File || end == 0 {
		return false
	}
	if start == 0 || start > end {
		start = end
	}
	return start <= e.Line && e.Line <= end
}

// Append adds the entry to the ledger in dir, creating the directory and the
// file when they are not there.
func Append(dir string, e Entry) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, File), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	buf, err := json.Marshal(e)
	if err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(buf, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Read returns the ledger in dir, oldest first. A directory with no ledger
// has no misses and is not an error.
func Read(dir string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(dir, File))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
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

// HeadKey names an archived head the way Held does: the pull request's session
// and the head's full commit.
func HeadKey(pr int, head string) string {
	return fmt.Sprintf("pr-%d/%s", pr, head)
}

// Held is the set of archived heads the ledger has an entry with a session
// for, by HeadKey, so `gc` leaves them alone.
func Held(entries []Entry) map[string]bool {
	held := map[string]bool{}
	for _, e := range entries {
		if e.Session != "" {
			held[HeadKey(e.PR, e.Head)] = true
		}
	}
	return held
}
