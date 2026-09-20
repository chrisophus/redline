package scout

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/chrisophus/redline/internal/review"
)

// Looker answers the lookups a judging pass can make for itself: searching the
// tree, reading a span of a file, naming what the team wrote down, resolving a
// Go symbol's callers, and asking git why a span of lines is there.
//
// It is this package's tools behind review.Looker's contract, and it exists so
// there is one implementation rather than two. These paths are the ones that
// have been hardened: a path is resolved inside the tree under review and one
// that climbs out is refused, a symlink that escapes the root is skipped, files
// over a megabyte are not read, and what comes back is capped. A reviewer
// searching through a second copy of that would be one audit behind this one.
//
// review cannot import this package - scout imports review - so the wiring goes
// the other way: the command builds this and hands it over as review.Looker.
type Looker struct {
	root string
	res  *resolver
	// base is the revision the change is measured against, and empty when the
	// caller does not know it. LineHistory drops the commits between it and
	// HEAD: in a pull request worktree HEAD is the change under review, so a
	// history walk that keeps them answers "why is this line here" with the
	// diff the reviewer is already reading.
	base string
	// ignore is .cursorindexingignore's patterns, read once at construction.
	// Both search and read refuse a path it names: it says in as many words
	// that a path is not for an automated reader, and --look is one.
	ignore []string
}

// NewLooker returns the lookups for one tree. root is the tree under review,
// which is the caller's checkout when it is clean at the reviewed revision and
// a detached worktree otherwise -- the same root the scout is given, because it
// is the same question being asked earlier.
func NewLooker(root, base string) *Looker {
	return &Looker{root: root, base: base, res: newResolver(root, Limits{}), ignore: loadIgnorePatterns(root)}
}

// Calls names the lookups this tree can answer.
//
// Searching, reading, the document list and line history need nothing but the
// checkout, so they are always there. symbol_context needs gorefactor, and a
// tool on the catalogue that cannot run costs the pass a turn and an error, so
// it is offered only where the binary is. Resolved once, at construction time
// of the run rather than per call, because the catalogue has to be the same
// bytes on every call of a run for the prompt cache to serve it.
func (l *Looker) Calls() []string {
	if l == nil {
		return nil
	}
	calls := []string{review.CallGrep, review.CallRead, review.CallDocs, review.CallHistory}
	if _, err := exec.LookPath("gorefactor"); err == nil {
		calls = append(calls, review.CallSymbol)
	}
	return calls
}

// ListDocs names the repository's own documents with their first heading, so a
// pass can find the note that says a construction is deliberate.
//
// filter narrows to the paths containing it. A repository with more documents
// than the bound gets a listing that says which directories hold the rest and
// with how many, so the next call asks for one of those instead of paging or
// guessing a word to grep for.
func (l *Looker) ListDocs(filter string) (string, error) {
	if l == nil {
		return "", fmt.Errorf("no tree to list")
	}
	all := listDocsIn(l.root, filter)
	kept := all[:0]
	for _, d := range all {
		if !ignoredPath(l.ignore, normPath(d.Path)) {
			kept = append(kept, d)
		}
	}
	return renderDocs(kept, maxLookDocs, filter), nil
}

// maxLookDocs is the cap on one list_docs call, the scout's own.
//
// Five hundred, because the listing is one line each and the break-even
// against forcing a second call is around five hundred and fifty. A
// repository with more than this gets the directory map and the path filter,
// which is the case those are for; a repository with two hundred documents
// should simply be shown them.
const maxLookDocs = 500

// SymbolContext asks gorefactor for one Go symbol's definition, its callers
// resolved through the type checker, its signature types and its tests.
//
// The binary is looked up again here rather than trusted from Calls: a pass
// can only call this where Calls offered it, but a tool that shells out on the
// strength of a check made minutes earlier is one that reports an exec error
// as an answer about the code.
func (l *Looker) SymbolContext(symbol string) (string, error) {
	if l == nil {
		return "", fmt.Errorf("no tree to resolve against")
	}
	if strings.TrimSpace(symbol) == "" {
		return "", fmt.Errorf("symbol_context needs a symbol")
	}
	if _, err := exec.LookPath("gorefactor"); err != nil {
		return "", fmt.Errorf("gorefactor is not installed, so this repository's Go symbols cannot be resolved")
	}
	out, err := runCmd(l.root, "gorefactor", "context", "--json", "--", symbol)
	if err != nil {
		return "", err
	}
	return capLookOutput(out, maxSymbolContextBytes,
		"narrow the symbol, or read the callers you care about with read_lines"), nil
}

// capLookOutput bounds what one lookup puts into the conversation.
//
// Every other lookup counts its own unit: 60 matches, 200 lines, 80 documents,
// 3 commits. These two hand back whatever a subprocess printed, and
// gorefactor's context for a widely-used symbol is every caller in the
// repository. The result is not recorded anywhere on the way past: it goes
// into the next turn's input as it stands, priced against a budget that has to
// pay for the turns still to come.
//
// Cut at a line boundary and say so, because a pass told nothing cannot tell a
// symbol with no callers from an answer that was too big to send, and the
// advice is what it can act on.
func capLookOutput(s string, max int, advice string) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + fmt.Sprintf("\n\n(cut at %d bytes of %d: %s)\n", len(cut), len(s), advice)
}

// The byte bounds on the two lookups that return a subprocess's output.
//
// These exist so no single lookup can take the conversation, and they are set
// to land where the lookups that count their own unit land. That needs the
// measured ratio rather than the familiar one: envelope prices Redline's
// payloads at 2.33 characters per token, because they are code, diffs and
// JSON rather than prose. At that ratio 32 KiB is about 14,000 tokens, which
// is where 600 lines of read_lines sits; 64 KiB would be 28,000, twice
// read_lines and four times grep, which is a lookup that can take the
// conversation on its own.
const (
	maxSymbolContextBytes = 32768
	maxLineHistoryBytes   = 32768
)

// LineHistory is git's account of a span of lines, copied as git printed it.
//
// It is the resolver's own lookup, which resolves the path inside the tree and
// refuses one that climbs out, so the hardening is the same as read_lines'.
func (l *Looker) LineHistory(path string, start, end int) (string, error) {
	if l == nil {
		return "", fmt.Errorf("no tree to read")
	}
	if ignoredPath(l.ignore, normPath(path)) {
		return "", fmt.Errorf("%s is excluded by .cursorindexingignore", normPath(path))
	}
	if start < 1 {
		return "", fmt.Errorf("line numbers start at 1")
	}
	if end < start {
		end = start
	}
	// Asked for more than are wanted, because the ones this change made are
	// dropped below and the walk has to reach past them to say anything.
	out, err := l.res.historyDepth(path, start, end, historyWalk)
	if err != nil {
		return "", err
	}
	out, dropped := withoutChangeCommits(out, l.changeCommits())
	if strings.TrimSpace(out) == "" {
		if dropped > 0 {
			// Said, rather than returned as nothing found. A span the change
			// itself introduced has no prior history, and that is an answer
			// about the span: there is nothing older to have been undone.
			return "", fmt.Errorf("these lines have no history before this change; %d commit(s) touching them are the change itself", dropped)
		}
		return "", fmt.Errorf("no recorded history for those lines")
	}
	return capLookOutput(out, maxLineHistoryBytes,
		"ask for a narrower line range"), nil
}

// historyWalk is how many commits deep LineHistory asks. Larger than what
// comes back, because the change's own commits are dropped from the answer and
// a pull request can carry several touching one span.
const historyWalk = 8

// changeCommits is every commit between the base and HEAD: the change under
// review. Empty when no base is known, which leaves the history unfiltered
// rather than guessing at what to remove.
func (l *Looker) changeCommits() map[string]bool {
	if l == nil || strings.TrimSpace(l.base) == "" {
		return nil
	}
	out, err := runCmd(l.root, "git", "rev-list", l.base+"..HEAD")
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, line := range strings.Fields(out) {
		set[line] = true
	}
	return set
}

// withoutChangeCommits removes the commit blocks the change itself produced,
// and reports how many it removed.
//
// Filtered here rather than by walking the base revision. git log -L tracks a
// line range backwards through history and adjusts the coordinates as it goes,
// so walking HEAD with the head-tree line numbers the reviewer is reading is
// the one pairing that is self-consistent. Handing base the head coordinates
// is the error gorefactor's own history walk made and corrected: on any file
// whose earlier hunks shifted line numbers it silently returns the history of
// whatever now sits at that offset in the older file, and git reports no error
// for it.
func withoutChangeCommits(content string, skip map[string]bool) (string, int) {
	if len(skip) == 0 {
		return content, 0
	}
	var b strings.Builder
	var dropped int
	lines := strings.Split(content, "\n")
	keeping := true
	for _, line := range lines {
		if sha, ok := commitLineSHA(line); ok {
			keeping = !skip[sha]
			if !keeping {
				dropped++
				continue
			}
		}
		if keeping {
			b.WriteString(line + "\n")
		}
	}
	return b.String(), dropped
}

// commitLineSHA reads the sha off a "commit <hex>" line.
func commitLineSHA(line string) (string, bool) {
	const prefix = "commit "
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	sha := strings.TrimSpace(line[len(prefix):])
	if len(sha) < 7 {
		return "", false
	}
	for _, r := range sha {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return "", false
		}
	}
	return sha, true
}

// Grep searches the tree for a regular expression.
//
// The cap is the scout's, and for the scout's reason: a pattern that matches
// half the repository is a pattern the model should narrow, and sixty lines is
// enough to see that it has to.
func (l *Looker) Grep(pattern, glob string) (string, error) {
	if l == nil {
		return "", fmt.Errorf("no tree to search")
	}
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("grep needs a pattern")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("bad pattern: %w", err)
	}
	return grepTree(l.root, re, glob, maxGrepMatches, l.ignore)
}

// ReadLines returns a span of one file, with line numbers.
//
// A path outside the tree, or a start past the end of the file, is an error
// rather than an empty result: a pass that asked for the wrong thing can fix
// the call, and one told "nothing there" cannot tell that from an empty file.
func (l *Looker) ReadLines(path string, start, end int) (string, error) {
	if l == nil {
		return "", fmt.Errorf("no tree to read")
	}
	if ignoredPath(l.ignore, normPath(path)) {
		return "", fmt.Errorf("%s is excluded by .cursorindexingignore", normPath(path))
	}
	lines, err := l.res.read(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	from, to := clamp(start, end, len(lines), review.MaxReadLines)
	if from == 0 {
		return "", fmt.Errorf("%s has %d lines; %d is past the end", path, len(lines), start)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d-%d\n", normPath(path), from, to)
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	return b.String(), nil
}
