package run

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/target"
)

const (
	baseSHA = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
	headA   = "aaaaaaa1111111111111111111111111111111aa"
	headB   = "bbbbbbb2222222222222222222222222222222bb"
)

func resultAt(head string) *Result {
	tgt := &target.Target{Kind: target.KindPR, Head: head, PR: &target.PullRequest{Number: 46}}
	return &Result{
		Report: findings.Report{BaseSHA: baseSHA},
		Target: tgt,
		Change: &change.Set{Target: tgt, BaseSHA: baseSHA,
			Files: []change.File{{Path: "web/operator.html", Status: "modified", AddedLines: []int{10, 11}}}},
	}
}

// writeRun does what a run and a review leave in a session directory: the
// session, and a review stamped with the change it was written against.
func writeRun(t *testing.T, dir string, res *Result, reviewNote string) {
	t.Helper()
	if err := SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	rev := `{"revision": "` + change.ReviewIdentity(res.Report.BaseSHA, res.Change) + `", "note": "` + reviewNote + `"}`
	if err := os.WriteFile(filepath.Join(dir, "review.json"), []byte(rev), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The done condition for keeping heads: the sessions of two successive heads
// of one pull request both exist afterwards and read back as themselves.
func TestTwoSuccessiveHeadsAreBothKeptAndReadable(t *testing.T) {
	dir := t.TempDir()
	for _, head := range []string{headA, headB} {
		res := resultAt(head)
		writeRun(t, dir, res, "review of "+head[:7])
		got, err := ArchiveHead(dir, res)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, head); got != want {
			t.Fatalf("archived into %q, want %q", got, want)
		}
	}
	for _, head := range []string{headA, headB} {
		loaded, err := LoadSession(filepath.Join(dir, head))
		if err != nil {
			t.Fatalf("head %s: %v", head[:7], err)
		}
		if loaded.Change.Target.Head != head {
			t.Errorf("the session kept under %s is for %s", head[:7], loaded.Change.Target.Head)
		}
		rev, err := os.ReadFile(filepath.Join(dir, head, "review.json"))
		if err != nil || !strings.Contains(string(rev), "review of "+head[:7]) {
			t.Errorf("head %s: review.json = %q, %v; want the review of that head", head[:7], rev, err)
		}
	}
	// The top level is still the latest head.
	latest, err := LoadSession(dir)
	if err != nil || latest.Change.Target.Head != headB {
		t.Errorf("the session directory itself = %v, %v; want the latest head", latest, err)
	}
}

// A run of a new head leaves the last head's review.json in the directory
// until its own review overwrites it. That one must not be filed under the new
// head.
func TestAnotherHeadsReviewIsNotArchivedUnderThisOne(t *testing.T) {
	dir := t.TempDir()
	writeRun(t, dir, resultAt(headA), "review of A")
	// A new push is observed, and not yet reviewed: session.json moves on and
	// review.json stays.
	next := resultAt(headB)
	if err := SaveSession(dir, next); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveHead(dir, next); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, headB, "review.json")); !os.IsNotExist(err) {
		t.Errorf("head B's archive holds a review.json written for head A (stat err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, headB, "session.json")); err != nil {
		t.Errorf("head B's archive has no session: %v", err)
	}
}

func TestArchivingTwiceKeepsOneCopyAndAddsTheReview(t *testing.T) {
	dir := t.TempDir()
	res := resultAt(headA)
	if err := SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveHead(dir, res); err != nil {
		t.Fatal(err)
	}
	writeRun(t, dir, res, "the review")
	if err := os.MkdirAll(filepath.Join(dir, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence", "x.txt"), []byte("proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveHead(dir, res); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"session.json", "review.json", "evidence/x.txt"} {
		if _, err := os.Stat(filepath.Join(dir, headA, rel)); err != nil {
			t.Errorf("archive lacks %s: %v", rel, err)
		}
	}
	heads, err := Heads(dir)
	if err != nil || len(heads) != 1 {
		t.Fatalf("heads = %v, %v; want one", heads, err)
	}
	// Archived heads never nest inside each other.
	if _, err := os.Stat(filepath.Join(dir, headA, headA)); !os.IsNotExist(err) {
		t.Errorf("the archive copied itself into itself (stat err %v)", err)
	}
}

func TestAWorkingTreeHasNoHeadToKeep(t *testing.T) {
	dir := t.TempDir()
	res := resultAt("")
	got, err := ArchiveHead(dir, res)
	if err != nil || got != "" {
		t.Errorf("ArchiveHead of a working tree = %q, %v; want nothing archived", got, err)
	}
}

func TestResolveHeadTakesAPrefix(t *testing.T) {
	dir := t.TempDir()
	for _, head := range []string{headA, headB} {
		res := resultAt(head)
		writeRun(t, dir, res, "r")
		if _, err := ArchiveHead(dir, res); err != nil {
			t.Fatal(err)
		}
	}
	got, full, err := ResolveHead(dir, "AAAAAAA")
	if err != nil || full != headA || got != filepath.Join(dir, headA) {
		t.Errorf("ResolveHead(AAAAAAA) = %q, %q, %v", got, full, err)
	}
	if _, _, err := ResolveHead(dir, "cccccc"); !errors.Is(err, ErrNoHead) {
		t.Errorf("an unknown head: %v, want ErrNoHead", err)
	}
	if _, _, err := ResolveHead(dir, "../x"); err == nil {
		t.Error("a head that is not hex was accepted")
	}
	// Two heads sharing a prefix are ambiguous, not a pick of the first.
	res := resultAt("aaaaaaa9999999999999999999999999999999aa")
	writeRun(t, dir, res, "r")
	if _, err := ArchiveHead(dir, res); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveHead(dir, "aaaaaaa"); err == nil || errors.Is(err, ErrNoHead) {
		t.Errorf("an ambiguous prefix: %v, want an error that is not ErrNoHead", err)
	}
}
