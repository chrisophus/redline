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
