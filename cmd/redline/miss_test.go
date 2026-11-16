package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

const (
	headOne = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headTwo = "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// reviewedHead saves a pull request's session for head and keeps it, the way
// `redline run --pr 46` does. The change adds line 12 of web/operator.html.
func reviewedHead(t *testing.T, root, head string) string {
	t.Helper()
	dir := filepath.Join(root, sessionsDir, "pr-46")
	tgt := &target.Target{Kind: target.KindPR, Head: head, PR: &target.PullRequest{Number: 46}}
	res := &run.Result{
		Report: findings.Report{BaseSHA: "b0b0b0b0"},
		Target: tgt,
		Change: &change.Set{Target: tgt, BaseSHA: "b0b0b0b0", Files: []change.File{
			{Path: "web/operator.html", Status: "modified", AddedLines: []int{12}},
		}},
	}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	if _, err := run.ArchiveHead(dir, res); err != nil {
		t.Fatal(err)
	}
	return dir
}

func missOpts(root string) opts {
	return opts{out: root, pr: "46", head: "1111111", defectFile: "web/operator.html",
		defectLine: 12, summary: "dropped a closing paren", fixPR: 59}
}

// The ticket's own case: the dropped `)` was on a line the reviewed diff added.
func TestMissSaysWhetherTheLineWasInTheReviewedDiff(t *testing.T) {
	root := t.TempDir()
	reviewedHead(t, root, headOne)
	if err := cmdMiss(missOpts(root)); err != nil {
		t.Fatal(err)
	}
	got, err := miss.Read(root)
	if err != nil || len(got) != 1 {
		t.Fatalf("ledger = %+v, %v; want one entry", got, err)
	}
	e := got[0]
	if e.PR != 46 || e.FixPR != 59 || e.Head != headOne {
		t.Errorf("entry = %+v, want PR 46, fix 59 and the full head", e)
	}
	if e.LineInDiff == nil || !*e.LineInDiff || *e.FindingTouched {
		t.Errorf("line in diff %v, finding touched %v; want in the diff and nothing touching it", e.LineInDiff, e.FindingTouched)
	}
	if want := filepath.Join(root, sessionsDir, "pr-46", headOne); e.Session != want {
		t.Errorf("session = %q, want the archived head %q", e.Session, want)
	}
}

func TestMissReadsTheHeadItWasGivenNotTheLatest(t *testing.T) {
	root := t.TempDir()
	reviewedHead(t, root, headOne)
	reviewedHead(t, root, headTwo)
	o := missOpts(root)
	o.head = "2222222"
	if err := cmdMiss(o); err != nil {
		t.Fatal(err)
	}
	got, _ := miss.Read(root)
	if len(got) != 1 || got[0].Head != headTwo {
		t.Errorf("entries = %+v, want the one for head two", got)
	}
}

// A defect older than Redline has no session, and goes in with nothing worked out.
func TestMissWithNoSessionIsRecordedWithNulls(t *testing.T) {
	root := t.TempDir()
	o := opts{out: root, pr: "12", defectFile: "reset.yml", defectLine: 3, summary: "roles that never existed"}
	if err := cmdMiss(o); err != nil {
		t.Fatal(err)
	}
	// And a head that was never archived is the same.
	o.head = "abc1234"
	if err := cmdMiss(o); err != nil {
		t.Fatal(err)
	}
	got, err := miss.Read(root)
	if err != nil || len(got) != 2 {
		t.Fatalf("ledger = %+v, %v; want two entries", got, err)
	}
	for _, e := range got {
		if e.LineInDiff != nil || e.FindingTouched != nil || e.Session != "" {
			t.Errorf("entry %+v has checks it had no session to make", e)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(root, miss.File))
	if !strings.Contains(string(raw), `"lineInDiff":null`) {
		t.Errorf("ledger line does not say null: %s", raw)
	}
}

// A pull request reviewed before heads were kept has only its latest session.
func TestMissFallsBackToTheLatestSessionWhenItIsTheSameCommit(t *testing.T) {
	root := t.TempDir()
	dir := reviewedHead(t, root, headOne)
	if err := os.RemoveAll(filepath.Join(dir, headOne)); err != nil {
		t.Fatal(err)
	}
	if err := cmdMiss(missOpts(root)); err != nil {
		t.Fatal(err)
	}
	got, _ := miss.Read(root)
	if len(got) != 1 || got[0].LineInDiff == nil || !*got[0].LineInDiff {
		t.Errorf("entries = %+v, want the check made against the latest session", got)
	}
}

func TestMissRefusesIncompleteFlags(t *testing.T) {
	root := t.TempDir()
	for name, mutate := range map[string]func(*opts){
		"no pr":      func(o *opts) { o.pr = "" },
		"bad pr":     func(o *opts) { o.pr = "abc" },
		"no file":    func(o *opts) { o.defectFile = "" },
		"no line":    func(o *opts) { o.defectLine = 0 },
		"no summary": func(o *opts) { o.summary = "  " },
		"bad head":   func(o *opts) { o.head = "../x" },
	} {
		o := missOpts(root)
		mutate(&o)
		if err := cmdMiss(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got, _ := miss.Read(root); len(got) != 0 {
		t.Errorf("a refused miss left %d entries", len(got))
	}
}

func TestReadersOpenAnArchivedHead(t *testing.T) {
	root := t.TempDir()
	pr := reviewedHead(t, root, headOne)
	reviewedHead(t, root, headTwo)
	o, err := resolveFor(t, "postmortem", opts{out: root, pr: "46", head: "1111"}, "pr", "head")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(pr, headOne); o.out != want {
		t.Errorf("out = %q, want %q", o.out, want)
	}
	if _, err := resolveFor(t, "postmortem", opts{out: root, pr: "46", head: "9999999"}, "pr", "head"); err == nil {
		t.Error("a head that was never archived was accepted")
	}
	if _, err := resolveFor(t, "postmortem", opts{out: root, branch: "x", head: "1111111"}, "head"); err == nil {
		t.Error("--head on a branch session was accepted")
	}
	if _, err := resolveFor(t, "postmortem", opts{out: root, pr: "46", head: "1111111"}, "out", "pr", "head"); err == nil {
		t.Error("--head with --out was accepted")
	}
}

func TestGCKeepsTheNewestHeadsAndTheOnesAMissNeeds(t *testing.T) {
	root := t.TempDir()
	pr := reviewedHead(t, root, headOne)
	reviewedHead(t, root, headTwo)
	heads := []string{"3333333ccccccccccccccccccccccccccccccccc", "4444444ddddddddddddddddddddddddddddddddd"}
	for i, h := range heads {
		reviewedHead(t, root, h)
		// Newer than the two before it.
		when := time.Now().Add(time.Duration(i-10) * time.Hour)
		_ = os.Chtimes(filepath.Join(pr, h, "session.json"), when, when)
	}
	old := time.Now().Add(-100 * time.Hour)
	for _, h := range []string{headOne, headTwo} {
		_ = os.Chtimes(filepath.Join(pr, h, "session.json"), old, old)
	}
	// A miss points at head one.
	if err := cmdMiss(missOpts(root)); err != nil {
		t.Fatal(err)
	}
	if err := cmdGC(opts{out: root, keepHeads: 1}); err != nil {
		t.Fatal(err)
	}
	left, _ := run.Heads(pr)
	var names []string
	for _, h := range left {
		names = append(names, h.SHA[:7])
	}
	if got := strings.Join(names, " "); got != "4444444 1111111" {
		t.Errorf("heads left = %q, want the newest and the one a miss points at", got)
	}
	// The latest head at the top level is not an archived head and stays.
	if _, err := run.LoadSession(pr); err != nil {
		t.Errorf("gc took the latest session: %v", err)
	}
}

func TestGCHeadsByAge(t *testing.T) {
	root := t.TempDir()
	pr := reviewedHead(t, root, headOne)
	reviewedHead(t, root, headTwo)
	old := time.Now().Add(-100 * time.Hour)
	_ = os.Chtimes(filepath.Join(pr, headOne, "session.json"), old, old)
	if err := cmdGC(opts{out: root, headsOlderThan: "48h"}); err != nil {
		t.Fatal(err)
	}
	left, _ := run.Heads(pr)
	if len(left) != 1 || left[0].SHA != headTwo {
		t.Errorf("heads left = %+v, want only head two", left)
	}
	if err := cmdGC(opts{out: root, headsOlderThan: "soon"}); err == nil {
		t.Error("a bad duration was accepted")
	}
	if err := cmdGC(opts{out: root, keepHeads: -2}); err == nil {
		t.Error("a negative --keep-heads was accepted")
	}
}

func TestLearningsDraftsARulePerMiss(t *testing.T) {
	draft := draftMiss(miss.Entry{PR: 46, FixPR: 59, File: "web/operator.html", Line: 12,
		Summary: "dropped a closing paren", LineInDiff: ptr(false)})
	for _, want := range []string{`scope: ["web/**"]`, "web/operator.html:12", "dropped a closing paren",
		"not in the diff", `learned_from: "PR 46, fixed in PR 59"`} {
		if !strings.Contains(draft, want) {
			t.Errorf("draft lacks %q:\n%s", want, draft)
		}
	}
	root := t.TempDir()
	if err := cmdLearnings(opts{out: root, misses: true}); err != nil {
		t.Errorf("no ledger is not an error: %v", err)
	}
	if err := cmdLearnings(opts{out: root, misses: true, head: "abc"}); err == nil {
		t.Error("--head with --misses was accepted")
	}
	if err := cmdLearnings(opts{out: root, misses: true, pr: "x"}); err == nil {
		t.Error("a bad --pr was accepted")
	}
}

func ptr[T any](v T) *T { return &v }
