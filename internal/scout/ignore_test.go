package scout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ignoreTree is lookerTree with a secret file and a build directory, both
// named in .cursorindexingignore, plus a .gitignore that would also exclude
// the secret file if this read it -- it must not.
func ignoreTree(t *testing.T) string {
	t.Helper()
	dir := lookerTree(t)
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("secret.env", "TOKEN=Insert-lookalike\n")
	write("build/gen.go", "package build\n\nfunc Insert() {}\n")
	write(".gitignore", "secret.env\n")
	write(".cursorindexingignore", "build/\n")
	return dir
}

// A directory .cursorindexingignore names is neither walked by a search nor
// readable by an explicit path, and a path only .gitignore names is read
// normally: .gitignore says what should not be committed, not what an
// automated reader should skip, and generated code or vendored deps are
// routinely both gitignored and something --look legitimately needs to read.
func TestLookExcludesOnlyCursorIndexingIgnore(t *testing.T) {
	l := NewLooker(ignoreTree(t))

	out, err := l.Grep("Insert", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "build/gen.go") {
		t.Errorf("grep walked a directory .cursorindexingignore excludes:\n%s", out)
	}
	if !strings.Contains(out, "secret.env") {
		t.Errorf("grep should still find a path only .gitignore names, not .cursorindexingignore:\n%s", out)
	}
	if !strings.Contains(out, "store.go") {
		t.Errorf("grep should still find files no ignore file names:\n%s", out)
	}

	if _, err := l.ReadLines("build/gen.go", 1, 1); err == nil {
		t.Error("reading a path .cursorindexingignore excludes must be refused")
	}
	if _, err := l.ReadLines("secret.env", 1, 1); err != nil {
		t.Errorf("a path only .gitignore names must still read: %v", err)
	}
}

// Grep's empty-match message must say when a path in scope was excluded,
// the same way ReadLines already refuses an excluded path by name: without
// it, a search over an ignored directory reads as proof the code is not in
// the tree rather than as a search that was not allowed to look.
func TestLookGrepNamesWhatItExcludedOnAnEmptyResult(t *testing.T) {
	dir := lookerTree(t)
	if err := os.WriteFile(filepath.Join(dir, ".cursorindexingignore"), []byte("other/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLooker(dir)
	out, err := l.Grep("func Use", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cursorindexingignore") {
		t.Errorf("an empty result over an excluded path must say so: %q", out)
	}
}

// No .cursorindexingignore at the root is not an error: a tree without one
// excludes nothing beyond the hardcoded housekeeping directories.
func TestLookWithNoIgnoreFileExcludesNothingExtra(t *testing.T) {
	l := NewLooker(lookerTree(t))
	out, err := l.Grep("Insert", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "store.go") {
		t.Errorf("grep should find what is there when no ignore file exists:\n%s", out)
	}
}

// A negated pattern is not honored as un-excluding anything, but it must not
// be read as an ordinary pattern either: that would exclude the very path a
// negation means to keep, which is the wrong failure direction for this tool.
func TestANegatedIgnorePatternExcludesNothing(t *testing.T) {
	dir := lookerTree(t)
	if err := os.WriteFile(filepath.Join(dir, ".cursorindexingignore"), []byte("!store.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLooker(dir)
	if _, err := l.ReadLines("store.go", 1, 1); err != nil {
		t.Errorf("a negated pattern must not exclude the path it means to keep: %v", err)
	}
}

// One skip list serves the search and the document listing, and it is named
// directories rather than every dot directory. A repository keeps its CI
// config in .github and its house rules in .claude, and a claim about this
// repository is exactly the kind a search has to check against those.
func TestSkipDirLeavesTheDirectoriesAClaimNeedsToCheck(t *testing.T) {
	for _, name := range []string{
		".github", ".claude", ".planning", "docs", "internal", "cmd", "testdata",
		"build", "out", "coverage",
	} {
		if skipDir(name) {
			t.Errorf("%s is skipped; a search has to be able to see it", name)
		}
	}
	for _, name := range []string{
		".git", "node_modules", "vendor", ".venv", "__pycache__", "target", "dist",
		".next", ".terraform", ".redline", "graphify-out", ".pytest_cache",
	} {
		if !skipDir(name) {
			t.Errorf("%s is walked; it is build output or a dependency tree", name)
		}
	}
}
