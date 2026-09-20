package scout

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisophus/redline/internal/change"
)

// ignoreFiles are read once per Looker and merged into one pattern list.
// .gitignore is the tree's own account of what does not belong in it;
// .cursorindexingignore is the same thing in as many words for an automated
// reader specifically, which --look is.
var ignoreFiles = []string{".gitignore", ".cursorindexingignore"}

// loadIgnorePatterns reads every file in ignoreFiles at root and returns
// their patterns as one list, comments and blank lines dropped. A missing
// file is not an error: a tree with neither has nothing to exclude.
//
// Only the root-level files are read, not one per directory. --look's own
// patterns (the glob a search is narrowed by) are root-relative for the same
// reason, and matchPattern below folds a bare name into "wherever it sits"
// the way git does, which covers the common case of a nested .gitignore
// - re-stating a name at the top - without a second walk to find one.
func loadIgnorePatterns(root string) []string {
	var patterns []string
	for _, name := range ignoreFiles {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// Negation is a real gitignore feature this does not have, since
			// matching one pattern at a time cannot un-exclude what an
			// earlier one already dropped. Treated as an ordinary pattern
			// would exclude the path it means to keep, which is the wrong
			// direction for a tool whose failure mode should be "still
			// visible" rather than "silently gone"; skipped instead.
			if strings.HasPrefix(line, "!") {
				continue
			}
			patterns = append(patterns, line)
		}
	}
	return patterns
}

// ignoredPath reports whether a repository-relative path is one .gitignore
// or .cursorindexingignore says to leave out, by the same glob rules
// internal/change already reads .redline.yml's exclude patterns with.
func ignoredPath(patterns []string, path string) bool {
	return change.ExcludeReason(path, patterns) != ""
}
