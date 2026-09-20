package scout

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// Every lookup bounds what it puts into the conversation. The three that count
// their own unit do it themselves; the two that hand back a subprocess's output
// need this, and gorefactor's context for a widely-used symbol is every caller
// in the repository. A result goes into the next turn's input as it stands.
func TestASubprocessLookupIsCutAndSaysSo(t *testing.T) {
	var b strings.Builder
	for b.Len() < maxSymbolContextBytes*2 {
		b.WriteString("caller: internal/some/package/file.go:120\n")
	}
	got := capLookOutput(b.String(), maxSymbolContextBytes, "narrow the symbol")
	if len(got) > maxSymbolContextBytes+200 {
		t.Errorf("output is %d bytes, want it cut near %d", len(got), maxSymbolContextBytes)
	}
	if !strings.Contains(got, "cut at") || !strings.Contains(got, "narrow the symbol") {
		t.Errorf("a cut result must say it was cut and what to do:\n%s", got[len(got)-200:])
	}
	if strings.HasSuffix(strings.TrimSpace(strings.Split(got, "(cut at")[0]), "file.go:12") {
		t.Error("the cut must land on a line boundary, not mid-line")
	}
	// Under the bound, nothing is added.
	if short := capLookOutput("one line\n", maxSymbolContextBytes, "narrow"); short != "one line\n" {
		t.Errorf("an answer that fits must pass through unchanged: %q", short)
	}
}

// A repository with more documents than one listing holds gets a map, not a
// dead end. The reason to list documents is not yet knowing which one to ask
// for, so "there are more, go and grep" is advice that cannot be taken.
func TestACutDocListingSaysWhereTheRestAre(t *testing.T) {
	var docs []docFile
	for i := 0; i < 54; i++ {
		docs = append(docs, docFile{Path: fmt.Sprintf("docs/adr/%03d.md", i), Lines: 10})
	}
	for i := 0; i < 33; i++ {
		docs = append(docs, docFile{Path: fmt.Sprintf("internal/notes/%03d.md", i), Lines: 10})
	}
	docs = append(docs, docFile{Path: "README.md", Lines: 10, Heading: "Redline"})
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })

	got := renderDocs(docs, 40, "")
	if !strings.Contains(got, "40 of 88 shown") {
		t.Errorf("the cut must say how much it did not show:\n%s", got)
	}
	if !strings.Contains(got, "docs/adr (") || !strings.Contains(got, "internal/notes (") {
		t.Errorf("the cut must name the directories holding the rest:\n%s", got)
	}
	if !strings.Contains(got, "Pass path") {
		t.Errorf("the cut must say how to ask for them:\n%s", got)
	}
	// Biggest first, so the one worth asking for is read first. 40 shown of 88
	// sorted by path leaves 15 under docs/adr and all 33 under internal/notes,
	// so the smaller directory is the one already half read.
	if !strings.Contains(got, "internal/notes (33), docs/adr (15)") {
		t.Errorf("the remainder must be counted and listed biggest first:\n%s", got)
	}
	// A listing that fits says nothing extra.
	if full := renderDocs(docs, 0, ""); strings.Contains(full, "shown, sorted by path") {
		t.Error("an uncut listing must not claim it was cut")
	}
	if none := renderDocs(nil, 40, "docs/adr/"); !strings.Contains(none, "no documents under docs/adr/") {
		t.Errorf("an empty filtered listing must name the filter: %q", none)
	}
}
