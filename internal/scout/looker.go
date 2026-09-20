package scout

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/chrisophus/redline/internal/review"
)

// Looker answers the two lookups a judging pass can make for itself: searching
// the tree and reading a span of a file.
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
