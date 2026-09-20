package scout

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisophus/redline/internal/change"
)

// ignoreFile is read once per Looker. .gitignore is not used for this: it
// says what should not be committed, and generated code, vendored deps and
// build output are all commonly gitignored and all things a reviewer
// legitimately needs to read to check a claim - on a repository that
// gitignores generated SQL bindings, --look would no longer be able to
// confirm the generated file matches the query that produced it.
// .cursorindexingignore means precisely "automated reader, skip this," with
// nothing to be wrong about the way .gitignore would be.
const ignoreFile = ".cursorindexingignore"

// loadIgnorePatterns reads ignoreFile at root and returns its patterns,
// comments and blank lines dropped. A missing file is not an error: a tree
// with none has nothing to exclude.
//
// Only the root-level file is read, not one per directory. --look's own
// patterns (the glob a search is narrowed by) are root-relative for the same
// reason, and a bare name folds into "wherever it sits" the way git does,
// which covers the common case of a nested exclusion - re-stating a name at
// the top - without a second walk to find one.
func loadIgnorePatterns(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ignoreFile))
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Negation would matter for a long, inherited .gitignore built as a
		// broad pattern plus rescues, which is exactly what is not being
		// read here: a hand-written .cursorindexingignore is short and
		// purpose-built, so a run of ordinary patterns has nothing to widen
		// past what was actually meant. Skipped rather than supported, since
		// this reads one pattern at a time and cannot un-exclude what an
		// earlier one already dropped.
		if strings.HasPrefix(line, "!") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// ignoredPath reports whether a repository-relative path is one
// .cursorindexingignore says to leave out, by the same glob rules
// internal/change already reads .redline.yml's exclude patterns with.
func ignoredPath(patterns []string, path string) bool {
	return change.ExcludeReason(path, patterns) != ""
}
