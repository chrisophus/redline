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
	// ignore is .cursorindexingignore's patterns, read once at construction.
	// Both search and read refuse a path it names: it says in as many words
	// that a path is not for an automated reader, and --look is one.
	ignore []string
}

// NewLooker returns the lookups for one tree. root is the tree under review,
// which is the caller's checkout when it is clean at the reviewed revision and
// a detached worktree otherwise -- the same root the scout is given, because it
// is the same question being asked earlier.
func NewLooker(root string) *Looker {
	return &Looker{root: root, res: newResolver(root, Limits{}), ignore: loadIgnorePatterns(root)}
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
// These exist so no single lookup can take the conversation, not to keep the
// answer small: sixty-four kilobytes is about sixteen thousand tokens, which
// is two cents on a pass and a sixteenth of one review's ceiling. Every
// caller of a widely-used symbol fits, and the pass does not spend a turn
// discovering it was cut.
const (
	maxSymbolContextBytes = 65536
	maxLineHistoryBytes   = 65536
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
	out, err := l.res.history(path, start, end)
	if err != nil {
		return "", err
	}
	return capLookOutput(out, maxLineHistoryBytes,
		"ask for a narrower line range"), nil
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
