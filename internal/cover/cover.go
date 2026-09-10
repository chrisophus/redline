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

// Result is the coverage of the changed lines, plus the profile's whole-repo
// total — the diff number says whether what changed is tested, the total
// says how well-tested the codebase it landed in already was, so a reviewer
// can tell one from the other rather than reading a single ambiguous percent.
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

	// TotalLines, TotalCovered, and TotalPercent are every coverable line the
	// whole profile has anything to say about, not just the lines this
	// change added — the same line-granular measure as Lines/Covered/Percent,
	// applied to the profile's full scope rather than the diff's. TotalPercent
	// is -1 when TotalLines is zero, for the same reason Percent is.
	TotalLines   int     `json:"totalLines"`
	TotalCovered int     `json:"totalCovered"`
	TotalPercent float64 `json:"totalPercent"`
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

// UsableProfile returns a non-stale coverage profile for this change, if any.
// observeDir is the tree under review; originDir is the caller checkout when
// it describes the same revision (see run.originCoverageDir).
func UsableProfile(observeDir, originDir string, changedPaths []string) (profile string, stale bool) {
	if name := Locate(observeDir); name != "" {
		if !ArtifactStale(observeDir, name, changedPaths) {
			return name, false
		}
		if originDir != "" && originDir != observeDir {
			if alt := Locate(originDir); alt != "" && !ArtifactStale(originDir, alt, changedPaths) {
				return alt, false
			}
		}
		return name, true
	}
	if originDir != "" && originDir != observeDir {
		if alt := Locate(originDir); alt != "" {
			return alt, ArtifactStale(originDir, alt, changedPaths)
		}
	}
	return "", false
}

// IsProfilePath reports whether rel is a conventional Go coverage profile path.
func IsProfilePath(rel string) bool {
	for _, n := range profileNames {
		if rel == n {
			return true
		}
	}
	return false
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
	defer func() { _ = f.Close() }()

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

// AddedLineText maps each added line's new-side number to its text.
//
// It shares walkAdded with AddedLines rather than reimplementing the walk,
// because the two are used as a matched pair: a caller that has line numbers
// from one and text from the other is pointing at the wrong code the moment
// they disagree, and they disagreed on exactly the two cases walkAdded
// documents.
func AddedLineText(diff string) map[int]string {
	out := map[int]string{}
	walkAdded(diff, func(line int, text string) { out[line] = text })
	return out
}

// walkAdded is the one reading of a unified diff's added lines.
//
// Two cases are easy to get wrong and are the reason this exists once rather
// than at each call site. "+++" is deliberately not special-cased: file
// headers live outside hunks (the "diff " reset below ends one), so inside a
// hunk "+++i;" is added code, and skipping it shifts every added-line number
// after it. And the "\" no-newline marker occupies no new line, so counting
// it shifts everything after it the other way.
func walkAdded(diff string, fn func(line int, text string)) {
	newLine := 0
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			n, ok := hunkStart(line)
			// An unparseable header names no real line; treating it as
			// line 0 would fabricate a location that cannot exist in any
			// file, so the hunk is skipped rather than guessed at (the
			// same "degrade the number, not the review" rule this file
			// applies to a malformed profile).
			newLine, inHunk = n, ok
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff "):
			// The next file's headers follow, outside any hunk.
			inHunk = false
		case strings.HasPrefix(line, "+"):
			fn(newLine, line[1:])
			newLine++
		case strings.HasPrefix(line, "-"), strings.HasPrefix(line, `\`):
			// Deleted content and the no-newline marker occupy no new line.
		default:
			newLine++
		}
	}
}

// AddedLines returns the new-side line numbers a unified diff adds. Coverage is
// only asked about lines this change introduced: whether the rest of the file is
// tested is a different, older question.
func AddedLines(diff string) []int {
	var out []int
	walkAdded(diff, func(line int, _ string) { out = append(out, line) })
	return out
}

// hunkStart reads the new-side start line out of `@@ -a,b +c,d @@`. The
// second result is false when the header cannot be parsed, or names a
// start below 1 — not a line any file has.
func hunkStart(header string) (int, bool) {
	plus := strings.Index(header, "+")
	if plus < 0 {
		return 0, false
	}
	rest := header[plus+1:]
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
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

	res := &Result{Profile: name, Percent: -1, TotalPercent: -1}
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
	res.TotalLines, res.TotalCovered = totalCoverage(blocks)
	if res.TotalLines > 0 {
		res.TotalPercent = float64(res.TotalCovered) / float64(res.TotalLines) * 100
	}
	res.Stale = isStale(root, full, changed)
	sortGaps(res.Uncovered)
	return res
}

// Profile is a parsed coverage profile, kept so per-line coverage can be read
// for many files without re-parsing. It is the raw material the report overlays
// on the diff: which of the lines you are reading a test actually ran.
type Profile struct {
	blocks map[string][]block
}

// Load reads the coverage profile under root, or nil when none is found or it
// does not parse. Like the rest of this package, absence is a normal answer.
func Load(root string) *Profile {
	name := Locate(root)
	if name == "" {
		return nil
	}
	blocks, err := parseProfile(filepath.Join(root, name))
	if err != nil || len(blocks) == 0 {
		return nil
	}
	return &Profile{blocks: blocks}
}

// LineCoverage returns, for one file, whether each coverable line was executed.
// A line no block mentions is not coverable (blank, import, declaration) and is
// omitted, so a caller can tell "ran", "did not run", and "nothing to run"
// apart. Nil when the profile says nothing about the file.
func (p *Profile) LineCoverage(file string) map[int]bool {
	if p == nil {
		return nil
	}
	fileBlocks := blocksFor(p.blocks, file)
	if fileBlocks == nil {
		return nil
	}
	out := map[int]bool{}
	for _, b := range fileBlocks {
		covered := b.count > 0
		for line := b.startLine; line <= b.endLine; line++ {
			out[line] = out[line] || covered
		}
	}
	return out
}

// blocksFor matches a repository path against the import-qualified names a Go
// profile uses. `github.com/x/y/internal/a/b.go` is the entry for
// `internal/a/b.go`. Several keys can share a path suffix (two files with the
// same trailing path in different modules or a vendored copy), so the match is
// chosen deterministically: the shortest qualifying key, where the requested
// path is the largest part of the name, with the name itself breaking ties.
// Returning the first key map iteration yields would report a different
// coverage number on two runs of identical input.
func blocksFor(blocks map[string][]block, path string) []block {
	if b, ok := blocks[path]; ok {
		return b
	}
	want := "/" + filepath.ToSlash(path)
	best := ""
	for name := range blocks {
		if !strings.HasSuffix(name, want) {
			continue
		}
		if best == "" || len(name) < len(best) || (len(name) == len(best) && name < best) {
			best = name
		}
	}
	if best == "" {
		return nil
	}
	return blocks[best]
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

// totalCoverage sums coverable and covered lines across every file the
// profile mentions, independent of what this change touched — the same
// per-line rule lineState applies to a single line, applied to every line
// any block in the file claims.
func totalCoverage(blocks map[string][]block) (lines, covered int) {
	for _, fileBlocks := range blocks {
		seen := map[int]bool{}
		for _, b := range fileBlocks {
			for ln := b.startLine; ln <= b.endLine; ln++ {
				seen[ln] = true
			}
		}
		for ln := range seen {
			isCovered, isCoverable := lineState(fileBlocks, ln)
			if !isCoverable {
				continue
			}
			lines++
			if isCovered {
				covered++
			}
		}
	}
	return lines, covered
}

// ArtifactStale reports whether artifact at root/rel is older than any of the
// changed paths. Used by the harness to decide whether to re-run a produce step.
func ArtifactStale(root, rel string, changedPaths []string) bool {
	changed := make([]Changed, len(changedPaths))
	for i, p := range changedPaths {
		changed[i] = Changed{Path: p}
	}
	return isStale(root, filepath.Join(root, rel), changed)
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
