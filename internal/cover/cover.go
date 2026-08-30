// Package cover computes diff coverage: of the lines this change adds, how many
// does a test profile show executed.
//
// This is the number that stands in for reading the tests. A reviewer does not
// read test bodies; they want to know whether the code that just arrived is
// exercised at all.
//
// It reads a profile the repository already has rather than running the tests.
// Redline is pre-push and non-gating, tests can need a database or minutes of
// wall time, and a tool that silently runs your suite is not one you reach for
// before every push. The cost is that the profile can be stale, so where it came
// from and whether it predates the change are both reported.
package cover

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Result is the coverage of the changed lines.
type Result struct {
	// Profile is the file the number came from, relative to the repository.
	// Named so a reviewer can judge whether to believe it.
	Profile string `json:"profile"`
	// Lines is how many added lines are coverable — inside a function a
	// profile has something to say about. Blank lines, imports and
	// declarations are not.
	Lines int `json:"lines"`
	// Covered is how many of those the profile shows executed.
	Covered int `json:"covered"`
	// Percent is Covered over Lines, or -1 when Lines is zero, so "no
	// coverable lines changed" never renders as zero per cent.
	Percent float64 `json:"percent"`
	// Stale is set when the profile is older than a file this change touches,
	// which means it cannot be describing the code under review.
	Stale bool `json:"stale,omitempty"`
	// Uncovered names the files with added lines the profile shows unexecuted,
	// most-uncovered first. The number alone does not tell a reviewer where to
	// look.
	Uncovered []FileGap `json:"uncovered,omitempty"`
}

// FileGap is one file's uncovered added lines.
type FileGap struct {
	Path  string `json:"path"`
	Lines []int  `json:"lines"`
}

// profileNames are the conventional places a Go coverage profile lands. There
// is no standard, so this is a search rather than a lookup.
var profileNames = []string{
	"coverage.out", "cover.out", "coverage.txt", "c.out",
	"coverage/coverage.out", ".coverage/coverage.out",
}

// Locate finds a coverage profile in the repository, or returns empty. Absence
// is a normal answer and the caller must report it as absence, never as zero.
func Locate(root string) string {
	for _, name := range profileNames {
		full := filepath.Join(root, name)
		if st, err := os.Stat(full); err == nil && !st.IsDir() && st.Size() > 0 {
			return name
		}
	}
	return ""
}

// block is one entry in a Go coverage profile: a statement range and how many
// times it ran.
type block struct {
	startLine, endLine int
	count              int
}

// parseProfile reads Go's coverage profile format. Lines look like
//
//	github.com/x/y/file.go:12.34,15.2 3 1
//
// where the trailing numbers are statement count and execution count. Anything
// unparseable is skipped: a malformed profile should degrade the number, not
// fail the review.
func parseProfile(path string) (map[string][]block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string][]block{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		name, rest := line[:colon], line[colon+1:]
		fields := strings.Fields(rest)
		if len(fields) != 3 {
			continue
		}
		span := strings.Split(fields[0], ",")
		if len(span) != 2 {
			continue
		}
		start, ok := lineOf(span[0])
		if !ok {
			continue
		}
		end, ok := lineOf(span[1])
		if !ok {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		out[name] = append(out[name], block{startLine: start, endLine: end, count: count})
	}
	return out, sc.Err()
}

func lineOf(s string) (int, bool) {
	dot := strings.Index(s, ".")
	if dot < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:dot])
	if err != nil {
		return 0, false
	}
	return n, true
}

// AddedLines returns the new-side line numbers a unified diff adds. Coverage is
// only asked about lines this change introduced: whether the rest of the file is
// tested is a different, older question.
func AddedLines(diff string) []int {
	var out []int
	newLine := 0
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			newLine = hunkStart(line)
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			// File headers inside a multi-file diff.
		case strings.HasPrefix(line, "+"):
			out = append(out, newLine)
			newLine++
		case strings.HasPrefix(line, "-"), strings.HasPrefix(line, `\`):
			// Deleted content and the no-newline marker occupy no new line.
		case strings.HasPrefix(line, "diff "):
			inHunk = false
		default:
			newLine++
		}
	}
	return out
}

// hunkStart reads the new-side start line out of `@@ -a,b +c,d @@`.
func hunkStart(header string) int {
	plus := strings.Index(header, "+")
	if plus < 0 {
		return 0
	}
	rest := header[plus+1:]
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// Changed is one changed file and the lines it added.
type Changed struct {
	Path  string
	Added []int
}

// Compute intersects a coverage profile with the lines this change added.
//
// Returns nil when there is no profile. That is the honest answer: a missing
// profile means nobody knows, and a zero would read as "nothing is tested",
// which is a different and much stronger claim.
func Compute(root string, changed []Changed) *Result {
	name := Locate(root)
	if name == "" {
		return nil
	}
	full := filepath.Join(root, name)
	blocks, err := parseProfile(full)
	if err != nil || len(blocks) == 0 {
		return nil
	}

	res := &Result{Profile: name, Percent: -1}
	for _, c := range changed {
		if filepath.Ext(c.Path) != ".go" || len(c.Added) == 0 {
			continue
		}
		fileBlocks := blocksFor(blocks, c.Path)
		if fileBlocks == nil {
			// The profile says nothing about this file. That is not the same as
			// uncovered — the package may simply not have been part of the run
			// — so those lines are left out of the denominator entirely.
			continue
		}
		var gap []int
		for _, line := range c.Added {
			covered, coverable := lineState(fileBlocks, line)
			if !coverable {
				continue
			}
			res.Lines++
			if covered {
				res.Covered++
			} else {
				gap = append(gap, line)
			}
		}
		if len(gap) > 0 {
			res.Uncovered = append(res.Uncovered, FileGap{Path: c.Path, Lines: gap})
		}
	}
	if res.Lines > 0 {
		res.Percent = float64(res.Covered) / float64(res.Lines) * 100
	}
	res.Stale = isStale(root, full, changed)
	sortGaps(res.Uncovered)
	return res
}

// blocksFor matches a repository path against the import-qualified names a Go
// profile uses, by longest suffix. `github.com/x/y/internal/a/b.go` is the entry
// for `internal/a/b.go`.
func blocksFor(blocks map[string][]block, path string) []block {
	if b, ok := blocks[path]; ok {
		return b
	}
	want := "/" + filepath.ToSlash(path)
	for name, b := range blocks {
		if strings.HasSuffix(name, want) {
			return b
		}
	}
	return nil
}

// lineState reports whether a line is inside a counted block, and whether any
// block covers it at all. A line no block mentions is not coverable: a blank
// line, an import, a declaration.
func lineState(blocks []block, line int) (covered, coverable bool) {
	for _, b := range blocks {
		if line < b.startLine || line > b.endLine {
			continue
		}
		coverable = true
		if b.count > 0 {
			// Any block covering this line having run is enough. Overlapping
			// blocks are normal and the optimistic reading is the one the go
			// tool itself takes.
			return true, true
		}
	}
	return false, coverable
}

// isStale compares the profile's timestamp with the files under review. A
// profile written before the code it supposedly covers cannot be describing it.
func isStale(root, profile string, changed []Changed) bool {
	st, err := os.Stat(profile)
	if err != nil {
		return false
	}
	profileTime := st.ModTime()
	for _, c := range changed {
		fst, err := os.Stat(filepath.Join(root, c.Path))
		if err != nil {
			continue
		}
		if fst.ModTime().After(profileTime.Add(time.Second)) {
			return true
		}
	}
	return false
}

func sortGaps(gaps []FileGap) {
	for i := 1; i < len(gaps); i++ {
		for j := i; j > 0 && len(gaps[j].Lines) > len(gaps[j-1].Lines); j-- {
			gaps[j], gaps[j-1] = gaps[j-1], gaps[j]
		}
	}
}
