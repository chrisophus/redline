package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

var (
	headA = strings.Repeat("a", 40)
	headB = strings.Repeat("b", 40)
)

// sessionOf is a saved run of one head, with one file the change added line 10
// to.
func sessionOf(head, path string) *run.Result {
	return &run.Result{Change: &change.Set{
		Target: &target.Target{Kind: target.KindPR, Head: head},
		Files:  []change.File{{Path: path, AddedLines: []int{10}}},
	}}
}

// Two successive heads of one pull request both stay readable, and the
// session directory itself is the latest.
func TestEachReviewedHeadKeepsItsSession(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, sessionsDir, "pr-7")
	o := opts{out: dir, root: root, sessionKey: "pr-7"}

	for _, tc := range []struct{ head, path string }{{headA, "first.go"}, {headB, "second.go"}} {
		res := sessionOf(tc.head, tc.path)
		if err := run.SaveSession(dir, res); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "postmortem.json"), []byte(tc.head+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		archiveHead(o, res)
	}

	for _, tc := range []struct{ head, path string }{{headA, "first.go"}, {headB, "second.go"}} {
		got, err := run.LoadSession(filepath.Join(dir, tc.head))
		if err != nil {
			t.Fatalf("head %s: %v", tc.head[:7], err)
		}
		if len(got.Change.Files) != 1 || got.Change.Files[0].Path != tc.path {
			t.Errorf("head %s kept %+v, want %s", tc.head[:7], got.Change.Files, tc.path)
		}
		pm, _ := os.ReadFile(filepath.Join(dir, tc.head, "postmortem.json"))
		if strings.TrimSpace(string(pm)) != tc.head {
			t.Errorf("head %s kept the postmortem of %q", tc.head[:7], pm)
		}
	}
	latest, err := run.LoadSession(dir)
	if err != nil || latest.Change.Files[0].Path != "second.go" {
		t.Errorf("the session directory is not the latest head: %v %+v", err, latest)
	}
	if _, err := os.Stat(filepath.Join(dir, headB, headA)); err == nil {
		t.Error("an older head was copied into the newer one")
	}
}

func TestNoHeadMeansNothingIsKept(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, sessionsDir, "tree-main")
	res := &run.Result{Change: &change.Set{Target: &target.Target{Kind: target.KindWorktree}}}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	archiveHead(opts{out: dir, sessionKey: "tree-main"}, res)
	// An explicit --out has no session key, and a pull request's head is kept
	// only under the layout Redline manages.
	pr := sessionOf(headA, "a.go")
	archiveHead(opts{out: dir}, pr)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("kept %s, want nothing", e.Name())
		}
	}
}

func TestDebugCapturesAreNotKept(t *testing.T) {
	dir := t.TempDir()
	res := sessionOf(headA, "a.go")
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "debug", "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	archiveHead(opts{out: dir, sessionKey: "pr-1"}, res)
	if _, err := os.Stat(filepath.Join(dir, headA, "debug")); err == nil {
		t.Error("the debug captures were copied")
	}
	if _, err := os.Stat(filepath.Join(dir, headA, "evidence", "x.txt")); err != nil {
		t.Errorf("the evidence was not copied: %v", err)
	}
}

func TestHeadOpensTheKeptSession(t *testing.T) {
	root := t.TempDir()
	pr := filepath.Join(root, sessionsDir, "pr-7")
	saveSessionAt(t, pr)
	saveSessionAt(t, filepath.Join(pr, headA))
	saveSessionAt(t, filepath.Join(pr, headB))

	for _, cmd := range []string{"postmortem", "learnings", "open"} {
		o, err := resolveFor(t, cmd, opts{out: root, pr: "7", head: headA[:9]}, "pr", "head")
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		if o.out != filepath.Join(pr, headA) {
			t.Errorf("%s --head reads %q, want the kept session of that head", cmd, o.out)
		}
	}
	// Without --head it is the latest, as before.
	o, err := resolveFor(t, "postmortem", opts{out: root, pr: "7"}, "pr")
	if err != nil || o.out != pr {
		t.Errorf("no --head: out %q err %v, want %q", o.out, err, pr)
	}
}

func TestHeadIsRefusedWhenItNamesNothingOrSeveral(t *testing.T) {
	root := t.TempDir()
	pr := filepath.Join(root, sessionsDir, "pr-7")
	saveSessionAt(t, pr)
	saveSessionAt(t, filepath.Join(pr, headA))
	saveSessionAt(t, filepath.Join(pr, strings.Repeat("a", 39)+"c"))

	for name, head := range map[string]string{
		"a commit that was not kept": strings.Repeat("d", 12),
		"a prefix two heads share":   strings.Repeat("a", 8),
		"something that is no sha":   "main",
	} {
		if _, err := resolveFor(t, "postmortem", opts{out: root, pr: "7", head: head}, "pr", "head"); err == nil {
			t.Errorf("--head naming %s was accepted", name)
		}
	}
}

func TestGCTrimsKeptHeadsByCountAndAge(t *testing.T) {
	root := t.TempDir()
	pr := filepath.Join(root, sessionsDir, "pr-7")
	saveSessionAt(t, pr)
	now := time.Now()
	heads := map[string]time.Duration{
		strings.Repeat("1", 40): 72 * time.Hour,
		strings.Repeat("2", 40): 48 * time.Hour,
		strings.Repeat("3", 40): time.Hour,
	}
	for h, age := range heads {
		saveSessionAt(t, filepath.Join(pr, h))
		when := now.Add(-age)
		if err := os.Chtimes(filepath.Join(pr, h, "session.json"), when, when); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(h string) bool {
		_, err := os.Stat(filepath.Join(pr, strings.Repeat(h, 40)))
		return err == nil
	}

	if n, _ := pruneHeads(root, 2, time.Time{}); n != 1 || exists("1") || !exists("2") || !exists("3") {
		t.Errorf("--keep-heads 2 removed %d, want only the oldest head gone", n)
	}
	if n, _ := pruneHeads(root, 0, now.Add(-24*time.Hour)); n != 1 || exists("2") || !exists("3") {
		t.Errorf("an age cutoff removed %d, want the 48h-old head gone and the newest kept", n)
	}
	if n, _ := pruneHeads(root, 0, time.Time{}); n != 0 {
		t.Errorf("no limit removed %d heads", n)
	}
	if _, err := os.Stat(filepath.Join(pr, "session.json")); err != nil {
		t.Errorf("gc touched the latest session: %v", err)
	}
}

// postmortem, learnings and open take --pr, so --head is looked up in that
// pull request's session and not in whichever one is latest.
func TestHeadReadersTakePR(t *testing.T) {
	for _, name := range []string{"postmortem", "learnings", "open"} {
		c, ok := commandNamed(name)
		if !ok {
			t.Fatalf("no command %s", name)
		}
		var o opts
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		c.flags(fs, &o)
		if err := fs.Parse([]string{"--pr", "123", "--head", "abcdef1"}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if o.targetSession(name) != "pr-123" {
			t.Errorf("%s: session = %q", name, o.targetSession(name))
		}
	}
}

// A copy that fails partway leaves the earlier archive of the head as it was
// and no half-written directory or temporary one.
func TestFailedCopyLeavesTheKeptHeadAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	root := t.TempDir()
	dir := filepath.Join(root, sessionsDir, "pr-7")
	o := opts{out: dir, root: root, sessionKey: "pr-7"}
	res := sessionOf(headA, "first.go")
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	archiveHead(o, res)

	bad := filepath.Join(dir, "review.json")
	if err := os.WriteFile(bad, []byte("{}"), 0o000); err != nil {
		t.Fatal(err)
	}
	archiveHead(o, sessionOf(headA, "second.go"))

	got, err := run.LoadSession(filepath.Join(dir, headA))
	if err != nil || got.Change.Files[0].Path != "first.go" {
		t.Errorf("the earlier archive was not kept: %v %+v", err, got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpArchivePrefix) {
			t.Errorf("temporary directory %s left behind", e.Name())
		}
	}
}
