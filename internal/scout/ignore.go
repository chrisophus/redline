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
// .cursorindexingignore means exactly "automated reader, skip this," and
// treating it that way is never wrong, the way treating .gitignore that way
// would be.
const ignoreFile = ".cursorindexingignore"

// loadIgnorePatterns reads ignoreFile at root and returns its patterns,
// comments and blank lines dropped. A missing file is not an error: a tree
// with none has nothing to exclude.
//
// Only the root-level file is read, not one per directory. --look's own
// patterns (the glob a search is narrowed by) are root-relative for the same
// reason, and a bare name matches at any depth, the way it does in
// .gitignore, which covers the common case of a nested exclusion - re-stating
// a name at the top - without a second walk to find one.
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

// skipDir reports whether a walk should not descend into a directory.
//
// One list for the search and the document listing, because a directory worth
// skipping in one is worth skipping in the other and two lists drift. It is
// named directories rather than every dot directory: .github holds the CI
// config, .claude holds the house rules, and both are things a claim about
// this repository needs to check against.
//
// What is here is build output, dependency trees and tool caches. A hit in one
// of them is a copy of code that lives somewhere else, so it points the reader
// at the wrong file, and walking them is the bulk of the time a search spends
// on a large checkout.
//
// What is deliberately not here is build, out and coverage. Each names a
// build directory often enough to be tempting and a real source directory
// often enough that skipping it would hide code from a search that reported no
// match. Skipping them would make the search faster, but a search that
// quietly cannot see a file gives a wrong answer, which matters more than
// speed.
func skipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn":
		return true
	case "node_modules", "vendor", "bower_components", "Pods":
		return true
	case ".venv", "venv", "__pycache__", ".tox", ".mypy_cache", ".pytest_cache", ".ruff_cache":
		return true
	case "target", "dist", ".gradle", ".m2":
		return true
	case ".next", ".nuxt", ".svelte-kit", ".turbo", ".parcel-cache", ".cache":
		return true
	case ".terraform", ".serverless":
		return true
	case ".redline", "graphify-out", ".idea", ".vscode-test":
		return true
	}
	return false
}
