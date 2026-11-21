package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

const (
	base  = "1111111111111111111111111111111111111111"
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	model = "claude-sonnet-5"
)

var day = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

func set(head string, files ...change.File) *change.Set {
	return &change.Set{Target: &target.Target{Head: head}, BaseSHA: base, Files: files}
}

// save writes a session and, when t is not nil, the trace of its review.
func save(t *testing.T, dir string, s *change.Set, tr *postmortem.Trace) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	res := &run.Result{Report: findings.Report{BaseSHA: base}, Change: s}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	if tr == nil {
		return
	}
	if err := postmortem.Write(dir, tr); err != nil {
		t.Fatal(err)
	}
}

func trace(head string, wrote time.Time) *postmortem.Trace {
	return &postmortem.Trace{
		Wrote: wrote, Target: "pull request 7", Model: model,
		Revision: change.ReviewIdentity(base, set(head)),
		CostUSD:  0.20, CostKnown: true, CallTurns: 3,
	}
}

func writeLedger(t *testing.T, dir string, rows ...review.Entry) {
	t.Helper()
	var b strings.Builder
	for _, r := range rows {
		buf, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(buf)
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "reviews.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture is a .redline directory with a pull request reviewed at two heads,
// a session nobody reviewed, and a working-tree session that was observed
// again after its review.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	pr := filepath.Join(root, sessionsDir, "pr-7")

	small := set(headA, change.File{Path: "a/x.go", Added: 30, Removed: 10})
	first := trace(headA, day)
	first.Findings = []postmortem.Finding{{ID: "c1", File: "a/x.go", Line: 4}}
	save(t, filepath.Join(pr, headA), small, first)

	big := set(headB,
		change.File{Path: "a/x.go", Added: 900, Removed: 100},
		change.File{Path: "b/y.go", Added: 400, Removed: 0},
		change.File{Path: "b/z.go", Added: 5, Removed: 5})
	second := trace(headB, day.Add(time.Hour))
	second.CostUSD, second.CallTurns = 1.10, 10
	second.Verified = true
	second.Stopped = []string{"judging"}
	second.Findings = []postmortem.Finding{
		{ID: "c1", File: "b/y.go", Asked: true, EvidenceFrom: postmortem.FromLookup,
			Ruling: findings.Ruling{Verdict: findings.VerifiedKept}},
		{ID: "c2", File: "a/x.go", Asked: true,
			Ruling: findings.Ruling{Verdict: findings.VerifiedUnverifiable}},
	}
	second.Lookups = []review.Lookup{
		{Tool: "grep", Input: `{"pattern":"Reset"}`, Bytes: 0, Empty: true},
		{Tool: "grep", Input: `{ "pattern": "Reset" }`, Bytes: 0, Empty: true},
		{Tool: "read_lines", Input: `{"path":"b/y.go","start":1,"end":40}`, Bytes: 900},
		{Tool: "read_lines", Input: `{"path":"c/other.go","start":1,"end":40}`, Bytes: 700},
	}
	second.Lookup = postmortem.Lookup{Ran: true, Turns: 4, CostUSD: 0.25, CostKnown: true, CapHit: true,
		Calls: []postmortem.Call{
			{Turn: 1, Tool: "grep", Args: "Reset"},
			{Turn: 2, Tool: "grep", Args: "Reset"},
			{Turn: 3, Tool: "read", Args: "nope.go", Failed: true},
		}}
	// The newest head is on disk twice: in its own directory and as the
	// session itself.
	save(t, filepath.Join(pr, headB), big, second)
	save(t, pr, big, second)

	save(t, filepath.Join(root, sessionsDir, "pr-9"), set(headA), nil)

	// Reviewed at one tree, then observed at another: the session no longer
	// describes the change the trace is about.
	stale := &postmortem.Trace{Wrote: day.Add(2 * time.Hour), Model: model, Revision: "1111111:worktree:old"}
	save(t, filepath.Join(root, sessionsDir, "tree-main"),
		set("", change.File{Path: "q.go", Added: 1}), stale)

	writeLedger(t, root,
		review.Entry{At: day.Add(-time.Second), Model: model, Known: true, CostUSD: 0.20,
			InputEstimate: 9000, Ceiling: 150000},
		review.Entry{At: day.Add(time.Hour - 2*time.Second), Model: model, Known: true, CostUSD: 1.10,
			InputEstimate: 160000, Ceiling: 150000, OverCeiling: true, Truncated: true,
			Pipeline: "staged", Cohorts: 3, CohortsFailed: 1},
		// A row from some other day matches nothing.
		review.Entry{At: day.Add(-48 * time.Hour), Model: model, Known: true, CostUSD: 9},
	)
	if err := miss.Append(root, miss.Entry{PR: 7, Head: headB[:10], File: "b/y.go", Line: 3, Summary: "off by one"}); err != nil {
		t.Fatal(err)
	}
	return root
}

func collect(t *testing.T) *Report {
	t.Helper()
	root := fixture(t)
	rep, err := Collect(root, root)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestCollectCountsEachReviewOnce(t *testing.T) {
	rep := collect(t)
	if len(rep.Runs) != 3 {
		t.Fatalf("got %d reviews, want 3: the head kept twice counts once", len(rep.Runs))
	}
	if rep.NoReview != 1 {
		t.Errorf("NoReview = %d, want 1 for the session with no trace", rep.NoReview)
	}
	for i := 1; i < len(rep.Runs); i++ {
		if rep.Runs[i].When.Before(rep.Runs[i-1].When) {
			t.Errorf("reviews are not in the order they ran")
		}
	}
}

func TestCollectJoinsSessionTraceAndLedger(t *testing.T) {
	rep := collect(t)
	a, b, stale := rep.Runs[0], rep.Runs[1], rep.Runs[2]

	if !a.SizeKnown || a.Lines() != 40 || a.Files != 1 || a.Dirs != 1 {
		t.Errorf("first review size = %d lines, %d files, %d dirs", a.Lines(), a.Files, a.Dirs)
	}
	if !a.InLedger || a.InputEstimate != 9000 || a.Split() || len(a.Limits) != 0 {
		t.Errorf("first review ledger join = %+v", a)
	}
	if a.Proposed != 1 || a.Kept != 1 {
		t.Errorf("an unverified review keeps what it proposed: %d proposed, %d kept", a.Proposed, a.Kept)
	}

	if b.Lines() != 1410 || b.Files != 3 || b.Dirs != 2 {
		t.Errorf("second review size = %d lines, %d files, %d dirs", b.Lines(), b.Files, b.Dirs)
	}
	if !b.Split() || b.Cohorts != 3 {
		t.Errorf("second review shape = %q with %d cohorts", b.Pipeline, b.Cohorts)
	}
	if b.Proposed != 2 || b.Kept != 1 {
		t.Errorf("second review: %d proposed, %d kept, want 2 and 1", b.Proposed, b.Kept)
	}
	if b.Misses != 1 || a.Misses != 0 {
		t.Errorf("the miss names the second head: got %d and %d", a.Misses, b.Misses)
	}
	want := []string{"stopped before done", "cost cap", "token cap", "size ceiling", "1 cohort call failed"}
	got := strings.Join(b.Limits, "\n")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("limits do not mention %q:\n%s", w, got)
		}
	}

	if stale.SizeKnown {
		t.Errorf("a session of a later change must not size an earlier review")
	}
	if stale.InLedger {
		t.Errorf("a review with no cost row near it matched one")
	}
}

func TestLookupCounts(t *testing.T) {
	b := collect(t).Runs[1]
	if l := b.Lookups; l.Made != 4 || l.Empty != 2 || l.Repeated != 1 || l.OnFinding != 1 {
		t.Errorf("lookups = %+v, want 4 made, 2 empty, 1 repeated, 1 on a finding's file", l)
	}
	s := b.Scout
	if !s.Ran || s.Calls != 3 || s.Failed != 1 || s.Repeated != 1 || !s.CapHit || s.Asked != 2 || s.FromFetch != 1 {
		t.Errorf("scout = %+v", s)
	}
}

func TestGroups(t *testing.T) {
	rep := collect(t)
	sizes := rep.BySize()
	if len(sizes) != 2 || sizes[0].Label != "up to 100" || sizes[1].Label != "up to 2,000" {
		t.Fatalf("size groups = %+v", sizes)
	}
	if sizes[1].HitLimit != 1 || sizes[1].Misses != 1 || sizes[1].MedianCost != 1.10 {
		t.Errorf("large group = %+v", sizes[1])
	}
	shapes := rep.ByShape()
	if len(shapes) != 2 || shapes[0].Label != "one call" || shapes[1].Label != "split in 3" {
		t.Fatalf("shape groups = %+v", shapes)
	}
}

func TestRender(t *testing.T) {
	out := collect(t).Render()
	for _, want := range []string{
		"3 saved reviews",
		"total cost          $1.30",
		"size unknown        1 review",
		"never reviewed      1 saved session",
		"1 of 3 reviews ran into a limit, and those cost $1.10 between them.",
		"1,410 lines in 3 files, split in 3",
		"2 of 4 (50%) came back empty",
		"1 of 4 (25%) repeated an earlier lookup",
		"pr-7 bbbbbbb 2026-11-10",
		"2 findings went to the lookups, and 1 ruling quoted",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	rep, err := Collect(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Render(); !strings.Contains(got, "No saved reviews under") {
		t.Errorf("empty directory rendered as:\n%s", got)
	}
}
