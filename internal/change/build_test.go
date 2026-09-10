package change_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/target"
)

type buildRepo struct {
	t   *testing.T
	dir string
}

func newBuildRepo(t *testing.T) *buildRepo {
	t.Helper()
	r := &buildRepo{t: t, dir: t.TempDir()}
	r.git("init", "-b", "main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "test")
	return r
}

func (r *buildRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *buildRepo) writeFile(path string, b []byte) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, b, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *buildRepo) open() *gitx.Repo {
	r.t.Helper()
	repo, err := gitx.Open(r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	return repo
}

func (r *buildRepo) head() string {
	r.t.Helper()
	h, err := r.open().Head()
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

// A binary file must not be carried whole into the session on a newline count
// alone: a multi-megabyte blob with few newlines would otherwise land in
// session.json and the prompt. Detection matches git: a NUL byte in the first
// block marks the content binary.
func TestBuildDoesNotCarryBinaryHead(t *testing.T) {
	r := newBuildRepo(t)
	bin := make([]byte, 4096)
	bin[10] = 0x00
	r.writeFile("blob.bin", bin)
	r.git("add", "-A")
	r.git("commit", "-m", "add binary")

	head := r.head()
	set := change.Build(r.open(), &target.Target{Head: head}, head, []string{"blob.bin"})
	if len(set.Files) != 1 {
		t.Fatalf("expected one file, got %d", len(set.Files))
	}
	if set.Files[0].Head != "" {
		t.Fatalf("a binary file must not carry Head, got %d bytes", len(set.Files[0].Head))
	}
}

// Truncating the diff for display must not shrink the coverage denominator.
// AddedLines is read from the full diff, so a line past the 60,000-byte cut is
// still counted. Before this, the truncated diff fed cover.AddedLines and lines
// past the cut dropped out of the coverage and mutation denominators.
func TestBuildAddedLinesSpanTruncatedDiff(t *testing.T) {
	r := newBuildRepo(t)
	r.writeFile("keep.go", []byte("package keep\n"))
	r.git("add", "-A")
	r.git("commit", "-m", "init")

	const lines = 9000
	var b strings.Builder
	for i := range lines {
		fmt.Fprintf(&b, "// added line %d\n", i)
	}
	r.writeFile("big.go", []byte(b.String()))

	head := r.head()
	set := change.Build(r.open(), &target.Target{Head: ""}, head, []string{"big.go"})
	if len(set.Files) != 1 {
		t.Fatalf("expected one file, got %d", len(set.Files))
	}
	f := set.Files[0]
	if len(f.AddedLines) != lines {
		t.Fatalf("AddedLines must count every added line from the full diff, got %d want %d", len(f.AddedLines), lines)
	}
	fromTruncated := len(cover.AddedLines(f.Diff))
	if fromTruncated >= len(f.AddedLines) {
		t.Fatalf("the display diff should be truncated (%d lines) below the full added set (%d)", fromTruncated, len(f.AddedLines))
	}
	if last := f.AddedLines[len(f.AddedLines)-1]; last != lines {
		t.Fatalf("the last added line must be present, got %d want %d", last, lines)
	}
}
