package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

// keepReviewedHead saves a pull request's review of one head the way a run
// does, so a miss has a session to read.
func keepReviewedHead(t *testing.T, root string, pr, head string, res *run.Result) {
	t.Helper()
	dir := filepath.Join(root, sessionsDir, "pr-"+pr)
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	archiveHead(opts{out: dir, sessionKey: "pr-" + pr}, res)
	if _, err := os.Stat(filepath.Join(dir, head, "session.json")); err != nil {
		t.Fatalf("head not kept: %v", err)
	}
}

func recordMiss(t *testing.T, args ...string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runMain(append([]string{"miss"}, args...)) })
	if err != nil {
		t.Fatalf("miss %v: %v", args, err)
	}
	return out
}

// The dropped ")" in sandbox-studio #46: the line was added by the reviewed
// head, and the review wrote a finding elsewhere in the file.
func TestMissSaysWhetherTheDefectWasInTheReviewedDiff(t *testing.T) {
	root := t.TempDir()
	res := &run.Result{
		Change: &change.Set{
			Target: &target.Target{Kind: target.KindPR, Head: headA},
			Files:  []change.File{{Path: "internal/studio/web/operator.html", AddedLines: []int{40, 41, 42}}},
		},
		Report: findings.Report{Findings: []findings.Finding{
			{ID: "f1", File: "internal/studio/web/operator.html", Line: 12, Message: "unrelated"},
		}},
	}
	keepReviewedHead(t, root, "46", headA, res)

	out := recordMiss(t, "--out", root, "--pr", "46", "--head", headA[:10],
		"--file", "internal/studio/web/operator.html", "--line", "41",
		"--summary", "a dropped ) blanked the operator page", "--fix-pr", "59")
	if !strings.Contains(out, "in the reviewed diff") {
		t.Errorf("the command did not say the line was in the diff:\n%s", out)
	}

	got, err := miss.Read(root)
	if err != nil || len(got) != 1 {
		t.Fatalf("ledger = %v, %v, want one entry", got, err)
	}
	e := got[0]
	if e.PR != 46 || e.Head != headA || e.FixPR != 59 || e.Line != 41 {
		t.Errorf("entry = %+v", e)
	}
	if e.FileInDiff == nil || !*e.FileInDiff || e.LineInDiff == nil || !*e.LineInDiff {
		t.Errorf("the dropped ) was added by the reviewed head, entry says file %v line %v", e.FileInDiff, e.LineInDiff)
	}
	if len(e.Findings) != 0 || len(e.Lookups) != 0 {
		t.Errorf("nothing covered the line, entry lists %+v and %+v", e.Findings, e.Lookups)
	}
	if want := filepath.Join(root, sessionsDir, "pr-46", headA); e.Session != want {
		t.Errorf("session = %q, want %q", e.Session, want)
	}
}

// A defect from before Redline has no session. It is still recorded, with the
// answers about the review left unknown rather than false.
func TestMissWithNoSessionIsStillRecorded(t *testing.T) {
	root := t.TempDir()
	out := recordMiss(t, "--out", root, "--pr", "12", "--file", "reset.yml", "--line", "3",
		"--summary", "roles that never existed")
	if !strings.Contains(out, "no kept session") {
		t.Errorf("the command did not say there was no session:\n%s", out)
	}
	got, _ := miss.Read(root)
	if len(got) != 1 || got[0].LineInDiff != nil || got[0].FileInDiff != nil || got[0].Session != "" {
		t.Fatalf("ledger = %+v, want one entry with the review's answers unset", got)
	}
	raw, _ := os.ReadFile(filepath.Join(root, miss.File))
	if strings.Contains(string(raw), "InDiff") {
		t.Errorf("an unknown answer was written out:\n%s", raw)
	}

	// A head with no kept session is the same, and a second miss appends.
	recordMiss(t, "--out", root, "--pr", "12", "--head", strings.Repeat("e", 12), "--file", "reset.yml",
		"--line", "4", "--summary", "another")
	if got, _ := miss.Read(root); len(got) != 2 || got[1].LineInDiff != nil {
		t.Errorf("ledger = %+v, want a second entry with no answers", got)
	}
}

func TestMissNeedsWhatItRecords(t *testing.T) {
	root := t.TempDir()
	full := []string{"--out", root, "--pr", "1", "--file", "a.go", "--line", "2", "--summary", "s"}
	for name, drop := range map[string]string{
		"--pr": "--pr", "--file": "--file", "--line": "--line", "--summary": "--summary",
	} {
		var args []string
		for i := 0; i < len(full); i += 2 {
			if full[i] != drop {
				args = append(args, full[i], full[i+1])
			}
		}
		if err := runMain(append([]string{"miss"}, args...)); err == nil {
			t.Errorf("miss without %s was accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, miss.File)); err == nil {
		t.Error("a refused miss left a ledger behind")
	}
}

func TestMissLedgerGoesBesideTheDispositionsWhenAsked(t *testing.T) {
	root, jsonl := t.TempDir(), t.TempDir()
	recordMiss(t, "--out", root, "--jsonl", jsonl, "--pr", "#5", "--file", "a.go", "--line", "2", "--summary", "s")
	if got, _ := miss.Read(jsonl); len(got) != 1 || got[0].PR != 5 {
		t.Errorf("ledger in --jsonl = %+v", got)
	}
}

func TestLearningsDraftsARuleFromAMiss(t *testing.T) {
	root := t.TempDir()
	yes := true
	if err := miss.Append(root, miss.Entry{PR: 46, File: "internal/studio/web/operator.html", Line: 41,
		Summary: "a dropped ) blanked the page", FixPR: 59, LineInDiff: &yes}); err != nil {
		t.Fatal(err)
	}
	var err error
	// No session in --out: the misses alone are enough.
	out := captureStdout(t, func() { err = cmdLearnings(opts{out: root, root: root}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`scope: ["internal/studio/web/**"]`,
		"PR #46 let through a defect at internal/studio/web/operator.html:41",
		"a dropped ) blanked the page",
		"in the reviewed diff and no finding was made",
		`"fixed in PR #59"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the draft lacks %q:\n%s", want, out)
		}
	}
}

func TestLearningsWithNoSessionAndNoMissesStillFails(t *testing.T) {
	root := t.TempDir()
	if err := cmdLearnings(opts{out: root, root: root}); err == nil {
		t.Error("learnings with no session and no misses returned no error")
	}
}

// A postmortem trace outlives the run that wrote it. One left by an earlier
// head must not credit the review of the head that was missed.
func TestMissIgnoresATraceFromAnotherHead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revision func(res *run.Result) string
		want     int
	}{
		{"stale", func(*run.Result) string { return "0000000:1111111" }, 0},
		{"own", func(res *run.Result) string { return change.ReviewIdentity(res.Report.BaseSHA, res.Change) }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			res := &run.Result{
				Change: &change.Set{
					Target: &target.Target{Kind: target.KindPR, Head: headA},
					Files:  []change.File{{Path: "a.html", AddedLines: []int{41}}},
				},
			}
			keepReviewedHead(t, root, "46", headA, res)
			tr := &postmortem.Trace{Revision: tc.revision(res), Findings: []postmortem.Finding{{ID: "c1", File: "a.html", Line: 41, Body: "x"}}}
			if err := postmortem.Write(filepath.Join(root, sessionsDir, "pr-46", headA), tr); err != nil {
				t.Fatal(err)
			}
			recordMiss(t, "--out", root, "--pr", "46", "--head", headA, "--file", "a.html", "--line", "41", "--summary", "s")
			got, _ := miss.Read(root)
			if len(got) != 1 || len(got[0].Findings) != tc.want {
				t.Fatalf("ledger = %+v, want %d finding(s) from the trace", got, tc.want)
			}
		})
	}
}

func TestLearningsReadsTheLedgerFromJSONLDir(t *testing.T) {
	root, ledger := t.TempDir(), t.TempDir()
	yes := true
	if err := miss.Append(ledger, miss.Entry{PR: 7, File: "a/b.go", Line: 2, Summary: "off by one", LineInDiff: &yes,
		Findings: []miss.Touch{{By: "proposed", ID: "c1"}}}); err != nil {
		t.Fatal(err)
	}
	var err error
	out := captureStdout(t, func() { err = cmdLearnings(opts{out: root, root: root, jsonl: ledger}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "off by one") || !strings.Contains(out, "the ruling dropped it") {
		t.Errorf("the draft lacks the miss or the dropped finding:\n%s", out)
	}
}

func TestDraftMissSeparatesAFileInTheDiffFromOneOutsideIt(t *testing.T) {
	yes, no := true, false
	in := draftMiss(miss.Entry{PR: 1, File: "a/b.go", Line: 2, Summary: "s", FileInDiff: &yes, LineInDiff: &no})
	if !strings.Contains(in, "The file was in the reviewed diff, but the change did not add this line") {
		t.Errorf("file in the diff, line not added:\n%s", in)
	}
	out := draftMiss(miss.Entry{PR: 1, File: "a/b.go", Line: 2, Summary: "s", FileInDiff: &no, LineInDiff: &no})
	if !strings.Contains(out, "reached through code the diff did not touch") {
		t.Errorf("file outside the diff:\n%s", out)
	}
}

func TestMissDoesNotRecordAHeadThatIsNotACommit(t *testing.T) {
	root := t.TempDir()
	recordMiss(t, "--out", root, "--pr", "5", "--head", "xyz", "--file", "a.go", "--line", "1", "--summary", "s")
	got, _ := miss.Read(root)
	if len(got) != 1 || got[0].Head != "" {
		t.Errorf("ledger = %+v, want one entry with no head", got)
	}
}

func TestLearningsNarrowsMissesToPRAndHead(t *testing.T) {
	root := t.TempDir()
	for _, e := range []miss.Entry{
		{PR: 5, Head: headA, File: "a/x.go", Line: 1, Summary: "five a"},
		{PR: 5, Head: headB, File: "a/x.go", Line: 2, Summary: "five b"},
		{PR: 6, Head: headA, File: "a/x.go", Line: 3, Summary: "six"},
	} {
		if err := miss.Append(root, e); err != nil {
			t.Fatal(err)
		}
	}
	draft := func(o opts) string {
		o.out, o.root = root, root
		var err error
		out := captureStdout(t, func() { err = cmdLearnings(o) })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := draft(opts{pr: "5"}); !strings.Contains(out, "five a") || !strings.Contains(out, "five b") || strings.Contains(out, "six") {
		t.Errorf("--pr 5:\n%s", out)
	}
	if out := draft(opts{pr: "5", head: headB[:9]}); strings.Contains(out, "five a") || !strings.Contains(out, "five b") {
		t.Errorf("--pr 5 --head:\n%s", out)
	}
	if out := draft(opts{}); !strings.Contains(out, "six") {
		t.Errorf("no flags should draft every miss:\n%s", out)
	}
}

func TestLearningsRefusesABadPRAndSurvivesABadLedger(t *testing.T) {
	root := t.TempDir()
	if err := cmdLearnings(opts{out: root, root: root, pr: "abc"}); err == nil {
		t.Error("learnings --pr abc was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, miss.File), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// With no session either, the session error is what comes back, not the
	// ledger's; with a session the drafts would still run.
	err := cmdLearnings(opts{out: root, root: root})
	if err == nil || strings.Contains(err.Error(), "misses.jsonl") {
		t.Errorf("err = %v, want the missing session", err)
	}
}
