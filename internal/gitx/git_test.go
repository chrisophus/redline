package gitx_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/gitx"
)

type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-b", "main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "test")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *repo) write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-m", msg)
}

func (r *repo) open() *gitx.Repo {
	r.t.Helper()
	repo, err := gitx.Open(r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	return repo
}

func TestParentOfHEAD(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("one")
	r.write("b.txt", "b\n")
	r.commit("two")
	repo := r.open()
	parent, err := repo.Parent("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	one, err := repo.Resolve("HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if parent != one {
		t.Fatalf("parent %s want %s", parent, one)
	}
	root, err := repo.Resolve("HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Parent(root); err == nil {
		t.Fatal("root commit must have no parent")
	}
}

func TestAttrSetReadsGitattributesIncludingOverrides(t *testing.T) {
	r := newRepo(t)
	// The later pattern un-sets the attribute for one file. A hand-rolled
	// .gitattributes matcher gets this precedence wrong; check-attr does not.
	r.write(".gitattributes", "*.gen.go linguist-generated=true\nkeep.gen.go -linguist-generated\n")
	r.write("api.gen.go", "package api\n")
	r.write("keep.gen.go", "package api\n")
	r.write("hand.go", "package api\n")
	r.commit("init")

	set := r.open().AttrSet("linguist-generated", []string{"api.gen.go", "keep.gen.go", "hand.go"})

	if !set["api.gen.go"] {
		t.Error("api.gen.go should be marked linguist-generated")
	}
	if set["keep.gen.go"] {
		t.Error("keep.gen.go un-sets the attribute and must not be marked")
	}
	if set["hand.go"] {
		t.Error("hand.go matches no pattern and must not be marked")
	}
}

func TestAttrSetIsEmptyWithoutGitattributes(t *testing.T) {
	r := newRepo(t)
	r.write("hand.go", "package api\n")
	r.commit("init")

	if got := r.open().AttrSet("linguist-generated", []string{"hand.go"}); len(got) != 0 {
		t.Fatalf("expected no attributes, got %v", got)
	}
	if got := r.open().AttrSet("linguist-generated", nil); len(got) != 0 {
		t.Fatalf("no paths means no work, got %v", got)
	}
}

func TestDiffPathUntrackedFileIsANewFileDiff(t *testing.T) {
	r := newRepo(t)
	r.write("keep.go", "package keep\n")
	r.commit("init")
	r.write("new_test.go", "package keep\n\nfunc TestX() {}\n")

	head, err := r.open().Head()
	if err != nil {
		t.Fatal(err)
	}
	diff := r.open().DiffPath(head, "new_test.go")
	if !strings.Contains(diff, "+package keep") {
		t.Fatalf("untracked file must have a unified diff, got %q", diff)
	}
	if !strings.Contains(diff, "new file") && !strings.Contains(diff, "/dev/null") {
		t.Fatalf("untracked diff should look like an add, got %q", diff)
	}
}

func TestDiffPathTrackedEditStillWorks(t *testing.T) {
	r := newRepo(t)
	r.write("keep.go", "package keep\n")
	r.commit("init")
	r.write("keep.go", "package keep\n\nfunc X() {}\n")

	head, err := r.open().Head()
	if err != nil {
		t.Fatal(err)
	}
	diff := r.open().DiffPath(head, "keep.go")
	if !strings.Contains(diff, "+func X()") {
		t.Fatalf("tracked edit must still produce a diff, got %q", diff)
	}
}

func TestStatCountsUntrackedAdds(t *testing.T) {
	r := newRepo(t)
	r.write("keep.go", "package keep\n")
	r.commit("init")
	r.write("new_test.go", "package keep\n\nfunc TestX() {}\n")

	head, err := r.open().Head()
	if err != nil {
		t.Fatal(err)
	}
	stats, err := r.open().Stat(head, "")
	if err != nil {
		t.Fatal(err)
	}
	var found *gitx.DiffStat
	for i := range stats {
		if stats[i].Path == "new_test.go" {
			found = &stats[i]
		}
	}
	if found == nil {
		t.Fatalf("untracked path missing from stat, got %+v", stats)
	}
	if found.Added == 0 {
		t.Fatalf("untracked add must count lines, got %+v", found)
	}
}

func TestCachedWorktreesListAndRemove(t *testing.T) {
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	r := newRepo(t)
	r.write("a.txt", "1\n")
	r.commit("one")
	r.write("a.txt", "2\n")
	r.commit("two")
	repo, err := gitx.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.CachedWorktrees(); err != nil || len(got) != 0 {
		t.Fatalf("a fresh cache must be empty, got %v err %v", got, err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := repo.Parent(head)
	if err != nil {
		t.Fatal(err)
	}
	d1, err := repo.AddWorktree(head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddWorktree(parent); err != nil {
		t.Fatal(err)
	}
	got, err := repo.CachedWorktrees()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("cached worktrees = %v, want 2", got)
	}
	for _, d := range got {
		repo.RemoveWorktree(d)
	}
	if _, err := os.Stat(d1); !os.IsNotExist(err) {
		t.Fatalf("worktree dir must be gone, stat err = %v", err)
	}
	if got, err := repo.CachedWorktrees(); err != nil || len(got) != 0 {
		t.Fatalf("cache must be empty after remove, got %v err %v", got, err)
	}
}

// A tracked symlink is stored by git as a blob of its link text. Hashing the
// file it resolves to instead made every symlink read as changed on every run,
// and a symlink to a directory read as deleted. The worktree view of a clean
// checkout must show no changes.
func TestChangedPathsIgnoresUnchangedSymlinks(t *testing.T) {
	r := newRepo(t)
	r.write("target.txt", "hello\n")
	if err := os.Symlink("target.txt", filepath.Join(r.dir, "flink")); err != nil {
		t.Fatal(err)
	}
	r.write("d/inner.txt", "x\n")
	if err := os.Symlink("d", filepath.Join(r.dir, "dlink")); err != nil {
		t.Fatal(err)
	}
	r.commit("init")

	repo := r.open()
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := repo.ChangedPaths(head)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("a clean checkout with symlinks must report no changes, got %v", changed)
	}
}

// A submodule is a gitlink: git records the commit it is checked out at, not a
// blob hashed from disk. Treating its directory as a vanished regular file made
// it read as deleted on every run.
func TestChangedPathsDoesNotDeleteSubmodule(t *testing.T) {
	sub := newRepo(t)
	sub.write("s.txt", "sub\n")
	sub.commit("sub init")

	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("init")
	add := exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", sub.dir, "mod")
	add.Dir = r.dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Skipf("submodule add is unsupported in this environment: %v\n%s", err, out)
	}
	r.commit("add submodule")

	repo := r.open()
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := repo.ChangedPaths(head)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range changed {
		if p == "mod" {
			t.Fatalf("a submodule must not read as changed or deleted, got %v", changed)
		}
	}
}

// Two histories with no common ancestor, as on a shallow clone, have no merge
// base. That is reported as an error, never as the ref itself, so a run does
// not diff against a base that is not one.
func TestMergeBaseErrsWithoutCommonAncestor(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.commit("main root")
	r.git("checkout", "--orphan", "other")
	r.write("b.txt", "b\n")
	r.commit("other root")

	if _, err := r.open().MergeBase("main"); err == nil {
		t.Fatal("MergeBase must error when rev and HEAD share no ancestor, not fall back to the ref")
	}
}
