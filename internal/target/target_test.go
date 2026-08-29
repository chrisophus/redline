package target

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRejectsPRAndBranch(t *testing.T) {
	_, err := Resolve(Options{Dir: t.TempDir(), PR: "1", Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("expected not-both error, got %v", err)
	}
}

func TestResolveWorktree(t *testing.T) {
	dir := initRepo(t)
	tgt, err := Resolve(Options{Dir: dir, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Kind != KindWorktree {
		t.Fatalf("kind %q", tgt.Kind)
	}
	if !sameDir(tgt.Dir, dir) {
		t.Fatalf("dir %q want %q", tgt.Dir, dir)
	}
	if tgt.Head != "" {
		t.Fatalf("worktree head should be empty, got %q", tgt.Head)
	}
}

func TestResolveBranchUsesDetachedWorktree(t *testing.T) {
	dir := initRepo(t)
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	tgt, err := Resolve(Options{Dir: dir, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Kind != KindBranch {
		t.Fatalf("kind %q", tgt.Kind)
	}
	if tgt.Dir == dir {
		t.Fatal("branch target must not be the user's checkout")
	}
	if tgt.Head == "" {
		t.Fatal("branch head SHA missing")
	}
	if _, err := os.Stat(filepath.Join(tgt.Dir, "README")); err != nil {
		t.Fatal(err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

// sameDir reports whether a and b name the same directory after symlink
// resolution. macOS temp dirs are often /var vs /private/var.
func sameDir(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}
