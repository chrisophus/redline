package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionNamesComeFromTheTargetFlags(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		o    opts
		want string
	}{
		{"run", opts{pr: "1360"}, "pr-1360"},
		{"run", opts{pr: "#1360"}, "pr-1360"},
		{"review", opts{pr: "https://github.com/o/r/pull/1360"}, "pr-1360"},
		{"run", opts{branch: "feat/x"}, "branch-feat-x"},
		{"run", opts{commit: "HEAD"}, "commit-HEAD"},
		{"run", opts{revRange: "HEAD~3..HEAD"}, "range-HEAD-3..HEAD"},
		{"run", opts{}, ""},
		{"post", opts{branch: "feat/x"}, ""},
	} {
		got := tc.o.targetSession(tc.cmd)
		if got != tc.want {
			t.Errorf("%s %+v: session %q, want %q", tc.cmd, tc.o, got, tc.want)
		}
		if got != "" && !validSessionName(got) {
			t.Errorf("%s %+v: derived session %q would be refused as a directory name", tc.cmd, tc.o, got)
		}
	}
}

func resolveFor(t *testing.T, cmd string, o opts, set ...string) (opts, error) {
	t.Helper()
	c, ok := commandNamed(cmd)
	if !ok {
		t.Fatalf("no command %q", cmd)
	}
	flags := map[string]bool{}
	for _, name := range set {
		flags[name] = true
	}
	err := resolveSession(c, &o, flags)
	return o, err
}

func saveSessionAt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An explicit --out is the session directory, as it was before sessions: the
// eval's fixtures and the report server's own re-exec pass one.
func TestAnExplicitOutIsTheSessionItself(t *testing.T) {
	o, err := resolveFor(t, "review", opts{out: "/some/dir", pr: "7"}, "out", "pr")
	if err != nil {
		t.Fatal(err)
	}
	if o.out != "/some/dir" || o.sessionKey != "" {
		t.Errorf("out = %q key = %q, want the directory untouched and no session name", o.out, o.sessionKey)
	}
	if _, err := resolveFor(t, "run", opts{out: "/some/dir", session: "mine"}, "out", "session"); err == nil {
		t.Error("--out and --session together were accepted")
	}
}

func TestReviewNeedsASavedSessionOrRun(t *testing.T) {
	root := t.TempDir()
	_, err := resolveFor(t, "review", opts{out: root, pr: "7"}, "pr")
	if err == nil || !strings.Contains(err.Error(), "--run") {
		t.Fatalf("review of a target with no saved session: %v, want a pointer to --run", err)
	}
	saveSessionAt(t, filepath.Join(root, sessionsDir, "pr-7"))
	o, err := resolveFor(t, "review", opts{out: root, pr: "7"}, "pr")
	if err != nil {
		t.Fatal(err)
	}
	if o.out != filepath.Join(root, sessionsDir, "pr-7") {
		t.Errorf("out = %q, want the pull request's session", o.out)
	}
	if o.ledgerDir() != root {
		t.Errorf("ledger in %q, want the root every session shares", o.ledgerDir())
	}
}

func TestObserveFlagsOnReviewNeedRun(t *testing.T) {
	_, err := resolveFor(t, "review", opts{out: t.TempDir(), noLint: true}, "no-lint")
	if err == nil || !strings.Contains(err.Error(), "--run") {
		t.Errorf("--no-lint without --run: %v, want it refused with a pointer to --run", err)
	}
}

func TestACommandWithNoTargetUsesTheLatestSession(t *testing.T) {
	root := t.TempDir()
	saveSessionAt(t, filepath.Join(root, sessionsDir, "pr-7"))
	saveSessionAt(t, filepath.Join(root, sessionsDir, "branch-x"))
	if err := os.WriteFile(filepath.Join(root, latestFile), []byte("branch-x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"review", "post", "postmortem", "learnings", "open", "serve"} {
		o, err := resolveFor(t, cmd, opts{out: root})
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		if o.out != filepath.Join(root, sessionsDir, "branch-x") {
			t.Errorf("%s works in %q, want the latest session", cmd, o.out)
		}
	}
	o, err := resolveFor(t, "postmortem", opts{out: root, session: "pr-7"}, "session")
	if err != nil || o.out != filepath.Join(root, sessionsDir, "pr-7") {
		t.Errorf("--session pr-7: out %q err %v, want that session", o.out, err)
	}
}

// A session an older Redline wrote straight into .redline is still read when
// no run has used the sessions layout yet.
func TestAFlatSessionIsReadUntilARunWritesOne(t *testing.T) {
	root := t.TempDir()
	saveSessionAt(t, root)
	o, err := resolveFor(t, "review", opts{out: root})
	if err != nil {
		t.Fatal(err)
	}
	if o.out != root {
		t.Errorf("out = %q, want the flat session in %q", o.out, root)
	}
}

func TestASessionNameIsChecked(t *testing.T) {
	for _, bad := range []string{"../x", "..", ".hidden", "a b", "a/b"} {
		if _, err := resolveFor(t, "run", opts{out: t.TempDir(), session: bad}, "session"); err == nil {
			t.Errorf("--session %q was accepted", bad)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, latestFile), []byte("../../elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveFor(t, "review", opts{out: root}); err == nil {
		t.Error("a latest file naming a path outside .redline/sessions was followed")
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// review --run observes the working tree, saves its session under the
// branch's name, marks it latest, and reviews it.
func TestReviewRunObservesThenReviews(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n\nfunc A() int { return 2 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	var err error
	out := captureStdout(t, func() {
		err = runMain([]string{"review", "--run", "--dry-run", "--no-lint", "--base", "HEAD"})
	})
	if err != nil {
		t.Fatalf("review --run: %v", err)
	}
	session := filepath.Join(".redline", sessionsDir, "tree-main")
	if _, err := os.Stat(filepath.Join(session, "session.json")); err != nil {
		t.Errorf("no session saved at %s: %v", session, err)
	}
	latest, _ := os.ReadFile(filepath.Join(".redline", latestFile))
	if strings.TrimSpace(string(latest)) != "tree-main" {
		t.Errorf("latest = %q, want tree-main", latest)
	}
	if !strings.Contains(out, "--- prompt ---") || !strings.Contains(out, "a.go") {
		t.Errorf("the dry run did not print a prompt over the observed change:\n%s", out)
	}
}
