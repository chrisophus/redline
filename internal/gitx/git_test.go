package gitx_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/gitx"
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
