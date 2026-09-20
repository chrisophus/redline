package scout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ignoreTree is lookerTree with a secret file, a build directory and both
// ignore files naming them.
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

// A file .gitignore names is neither found by a search nor readable by an
// explicit path, and the same for a directory .cursorindexingignore names.
func TestLookExcludesGitignoreAndCursorIndexingIgnore(t *testing.T) {
	l := NewLooker(ignoreTree(t))

	out, err := l.Grep("Insert", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "secret.env") {
		t.Errorf("grep found a file .gitignore excludes:\n%s", out)
	}
	if strings.Contains(out, "build/gen.go") {
		t.Errorf("grep walked a directory .cursorindexingignore excludes:\n%s", out)
	}
	if !strings.Contains(out, "store.go") {
		t.Errorf("grep should still find files neither ignore file names:\n%s", out)
	}

	if _, err := l.ReadLines("secret.env", 1, 1); err == nil {
		t.Error("reading a path .gitignore excludes must be refused")
	}
	if _, err := l.ReadLines("build/gen.go", 1, 1); err == nil {
		t.Error("reading a path .cursorindexingignore excludes must be refused")
	}
	if _, err := l.ReadLines("store.go", 1, 1); err != nil {
		t.Errorf("a path neither ignore file names must still read: %v", err)
	}
}

// Neither file existing is not an error: a tree with no ignore file at its
// root excludes nothing beyond the hardcoded housekeeping directories.
func TestLookWithNoIgnoreFilesExcludesNothingExtra(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("!store.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLooker(dir)
	if _, err := l.ReadLines("store.go", 1, 1); err != nil {
		t.Errorf("a negated pattern must not exclude the path it means to keep: %v", err)
	}
}
