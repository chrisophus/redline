package history

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/jev"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/run"
)

// judged is a .redline directory with one verified review of a pull request.
// It wrote three findings: one kept on a line that shows the problem, one
// kept on a line that does not, and one the ruling withdrew. Its session
// resolved three context entries: one a ruling quoted, one a finding named,
// and one nothing used.
func judged(t *testing.T) *Report {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, sessionsDir, "pr-7", headB)
	s := set(headB, change.File{Path: "b/y.go", Added: 40, Removed: 2, Diff: "+ users[0].Name"})
	tr := trace(headB, day)
	tr.Verified = true
	tr.Findings = []postmortem.Finding{
		{ID: "c1", File: "b/y.go", Line: 12, Body: "users[0] is read when the list can be empty",
			Confidence: findings.ConfidenceLow,
			Ruling:     findings.Ruling{Verdict: findings.VerifiedKept, Evidence: "func loadUsers() []User { return nil }"}},
		{ID: "c2", File: "b/y.go", Line: 30, Body: "the retry never backs off",
			Ruling: findings.Ruling{Verdict: findings.VerifiedKept, Evidence: "import \"time\""}},
		{ID: "c3", File: "b/y.go", Line: 3, Body: "FormatHeader drops the trailing newline",
			Ruling: findings.Ruling{Verdict: findings.VerifiedWithdrawn, Evidence: "return h + \"\\n\""}},
	}
	res := &run.Result{Report: findings.Report{BaseSHA: base}, Change: s,
		Envelopes: []*envelope.Envelope{{Expansions: []envelope.Expansion{
			{Role: "callee", File: "b/load.go", Symbol: "loadUsers", Content: "func loadUsers() []User { return nil }"},
			{Role: "caller", File: "b/fmt.go", Symbol: "FormatHeader", Content: "func FormatHeader(h string) string"},
			{Role: "type", File: "b/user.go", Symbol: "User", Content: "type User struct{ Name string }"},
		}}}}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	if err := postmortem.Write(dir, tr); err != nil {
		t.Fatal(err)
	}
	rep, err := Collect(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 1 {
		t.Fatalf("fixture has %d reviews, want 1", len(rep.Runs))
	}
	return rep
}

// fakeJudge scores by a word in what it is shown, and records what it saw.
type fakeJudge struct {
	said    []jev.Said
	entries []jev.Entry
	fail    bool
}

func (f *fakeJudge) Describes(_ context.Context, d jev.Defect, said []jev.Said) ([]float64, error) {
	if f.fail {
		return nil, errors.New("401 bad key")
	}
	f.said = said
	out := make([]float64, len(said))
	for i, s := range said {
		out[i] = 0.1
		for word, p := range map[string]float64{"empty": 0.9, "newline": 0.7} {
			if strings.Contains(d.Summary, word) && strings.Contains(s.Text, word) {
				out[i] = p
			}
		}
	}
	return out, nil
}

func (f *fakeJudge) Supports(_ context.Context, c jev.Claim) (float64, error) {
	if strings.Contains(c.Evidence, "import") {
		return 0.05, nil
	}
	return 0.95, nil
}

func (f *fakeJudge) Needed(_ context.Context, _ []jev.Changed, entries []jev.Entry) ([]float64, error) {
	f.entries = entries
	out := make([]float64, len(entries))
	for i, e := range entries {
		out[i] = map[string]float64{"loadUsers": 0.9, "FormatHeader": 0.3, "User": 0.4}[e.Symbol]
	}
	return out, nil
}

func TestReplayMissesSaysWhatBecameOfEachMiss(t *testing.T) {
	rep := judged(t)
	f := &fakeJudge{}
	rep.ReplayMisses(context.Background(), f, []miss.Entry{
		{PR: 7, Head: headB[:10], File: "b/y.go", Line: 14, Summary: "index into an empty list"},
		{PR: 7, Head: headB[:10], File: "b/y.go", Line: 3, Summary: "header loses its newline"},
		{PR: 7, Head: headB[:10], File: "z.go", Line: 1, Summary: "leaks a goroutine"},
		{PR: 9, Head: headA[:10], File: "q.go", Line: 1, Summary: "from a review nobody kept"},
	}, nil)
	var got []string
	for _, s := range rep.Misses.Seen {
		got = append(got, s.Verdict+" "+s.Finding)
	}
	// The first is two lines from the finding that describes it, which a
	// match on file and line would not have counted.
	want := "found c1|ruled out c3|not proposed c1|no saved review "
	if strings.Join(got, "|") != want {
		t.Errorf("verdicts = %q, want %q", strings.Join(got, "|"), want)
	}
	if len(f.said) != 3 || f.said[0].Text == "" {
		t.Errorf("the judge was shown %+v", f.said)
	}
	out := rep.Render()
	for _, want := range []string{
		"4 misses recorded.",
		"1 of 4 (25%) had a finding that described it and that the ruling withdrew",
		"found: closest finding c1 scored 0.90, confidence low",
		"1 of 4 (25%) have no saved review of their head",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestReplayMissesKeepsGoingWhenTheJudgeFails(t *testing.T) {
	rep := judged(t)
	rep.ReplayMisses(context.Background(), &fakeJudge{fail: true},
		[]miss.Entry{{PR: 7, Head: headB[:10], File: "b/y.go", Line: 14, Summary: "x"}}, nil)
	if rep.Misses.Failed == "" || rep.Misses.Seen[0].Verdict != MissNotProposed {
		t.Errorf("a failed judge gave %+v", rep.Misses)
	}
}

func TestReplayEvidenceFindsAKeptFindingOnAWeakLine(t *testing.T) {
	rep := judged(t)
	rep.ReplayEvidence(context.Background(), &fakeJudge{}, nil)
	if n := len(rep.Evidence.Seen); n != 3 {
		t.Fatalf("checked %d findings, want 3", n)
	}
	out := rep.Render()
	for _, want := range []string{
		"kept          2 findings, median score 0.05, 1 of 2 (50%) under 0.50",
		"withdrawn     1 finding, median score 0.95",
		"pr-7 bbbbbbb c2  0.05",
		"finding:  the retry never backs off",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "c1  0.95") {
		t.Errorf("a kept finding with strong evidence was listed as weak:\n%s", out)
	}
}

func TestReplayContextComparesScoresWithWhatWasUsed(t *testing.T) {
	rep := judged(t)
	f := &fakeJudge{}
	rep.ReplayContext(context.Background(), f, nil)
	c := rep.Context
	// loadUsers was quoted by a ruling and FormatHeader named by a finding.
	// User was not used. loadUsers scored above it and FormatHeader below.
	if c.Reviews != 1 || c.Entries != 3 || c.Used != 2 || c.Pairs != 2 || c.Right != 1 || c.KeptInTopHalf != 1 {
		t.Errorf("context totals = %+v", c)
	}
	if c.MeanUsed < 0.59 || c.MeanUsed > 0.61 || c.MeanOther != 0.4 {
		t.Errorf("means = %v and %v", c.MeanUsed, c.MeanOther)
	}
	if len(f.entries) != 3 {
		t.Errorf("the judge was shown %d entries", len(f.entries))
	}
	out := rep.Render()
	for _, want := range []string{
		"1 review had 3 context entries, and a finding used 2 of them.",
		"in 50% of 2 pairs",
		"would have kept 1 of 2 (50%) used entries",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

// A review whose entries were all unused has nothing to compare, and asking
// about it would spend money to learn nothing.
func TestReplayContextSkipsAReviewWithNothingToCompare(t *testing.T) {
	rep := judged(t)
	tr, err := postmortem.Load(rep.Runs[0].Dir)
	if err != nil {
		t.Fatal(err)
	}
	tr.Findings = nil
	if err := postmortem.Write(rep.Runs[0].Dir, tr); err != nil {
		t.Fatal(err)
	}
	f := &fakeJudge{}
	rep.ReplayContext(context.Background(), f, nil)
	if f.entries != nil || rep.Context.Reviews != 0 || rep.Context.Skipped != 1 {
		t.Errorf("asked about %d entries, totals %+v", len(f.entries), rep.Context)
	}
}
