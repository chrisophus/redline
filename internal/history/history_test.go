package history

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/jev"
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

// fakeAsker splits anything over 1,000 lines and stops any lookup that an
// earlier lookup in the prompt already made. It counts what it was asked.
type fakeAsker struct {
	mu    sync.Mutex
	asked int
	fail  bool
	// probs makes it answer with a probability, as a decision model does.
	probs bool
}

func (f *fakeAsker) Ask(_ context.Context, q Question) (Answer, error) {
	f.mu.Lock()
	f.asked++
	f.mu.Unlock()
	ans := Answer{Said: q.No, Usage: review.Usage{InputTokens: 100, OutputTokens: 1}}
	if f.fail {
		return ans, errors.New("401 bad key")
	}
	if q.Yes == SaidSplit {
		if strings.Contains(q.State, "1,305 lines added") {
			ans.Said = q.Yes
		}
	} else if _, next, _ := strings.Cut(q.State, "wants to make next:"); strings.Contains(next, `"Reset"`) &&
		strings.Contains(q.State, "nothing found") {
		ans.Said = q.Yes
	}
	if f.probs {
		ans.HasP, ans.P = true, 0.2
		if ans.Said == q.Yes {
			ans.P = 0.8
		}
	}
	return ans, nil
}

func TestAskRecordsAnswersAgainstOutcomes(t *testing.T) {
	rep := collect(t)
	f := &fakeAsker{}
	if err := rep.Ask(context.Background(), f, model, 0, nil); err != nil {
		t.Fatal(err)
	}
	// Two reviews have a known change, and one of them made four lookups.
	if f.asked != 6 || rep.Replay.Asks != 6 || rep.Replay.Usage.InputTokens != 600 {
		t.Fatalf("asked %d, recorded %+v", f.asked, rep.Replay)
	}
	a, b, stale := rep.Runs[0], rep.Runs[1], rep.Runs[2]
	if a.Asked == nil || a.Asked.Shape != SaidSingle || b.Asked == nil || b.Asked.Shape != SaidSplit {
		t.Errorf("shape answers = %+v and %+v", a.Asked, b.Asked)
	}
	if stale.Asked != nil {
		t.Errorf("a review whose change is unknown was asked about")
	}
	var said []string
	for _, l := range b.Each {
		said = append(said, l.Said)
	}
	if got := strings.Join(said, " "); got != "look stop look look" {
		t.Errorf("lookup answers = %q", got)
	}
	out := rep.Render()
	for _, want := range []string{
		"What claude-sonnet-5 would have decided",
		"6 questions answered, 600 input tokens",
		"Reviews that were split: 1. It would have sent 0 of them in one call.",
		"Stopping where it said would have skipped 1 of 2 (50%) empty or repeated lookups",
		"and 0 of 1 (0%) lookups on a file",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestAskLimitSendsSplitQuestionsFirst(t *testing.T) {
	rep := collect(t)
	f := &fakeAsker{}
	if err := rep.Ask(context.Background(), f, model, 3, nil); err != nil {
		t.Fatal(err)
	}
	if f.asked != 3 || rep.Replay.Skipped != 3 {
		t.Errorf("asked %d, skipped %d, want 3 and 3", f.asked, rep.Replay.Skipped)
	}
	if rep.Runs[0].Asked == nil || rep.Runs[1].Asked == nil {
		t.Errorf("the limit cut a split question before a lookup question")
	}
}

func TestAskFailsWhenNothingAnswers(t *testing.T) {
	rep := collect(t)
	err := rep.Ask(context.Background(), &fakeAsker{fail: true}, model, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "401 bad key") {
		t.Errorf("every question failing gave %v", err)
	}
}

// The split question must not say how the review went, or the model would be
// answering with the outcome in front of it.
func TestShapePromptHoldsOnlyWhatWasKnownBefore(t *testing.T) {
	b := collect(t).Runs[1]
	p := shapeQuestion(b).prompt()
	for _, leak := range []string{"judging", "cost cap", "token cap", "cohort", "1.10", "off by one"} {
		if strings.Contains(p, leak) {
			t.Errorf("split question mentions %q:\n%s", leak, p)
		}
	}
	if !strings.Contains(p, "b/y.go  +400 -0") {
		t.Errorf("split question does not list the files:\n%s", p)
	}
}

func TestChoose(t *testing.T) {
	pick := []string{SaidLook, SaidStop}
	for reply, want := range map[string]string{
		"stop": SaidStop, " `Look`.\n": SaidLook, "I would stop here.": SaidStop,
		"**stop**": SaidStop,
	} {
		if got, err := choose(reply, pick); err != nil || got != want {
			t.Errorf("choose(%q) = %q, %v, want %q", reply, got, err, want)
		}
	}
	for _, reply := range []string{"", "maybe", "look, or stop if it repeats"} {
		if got, err := choose(reply, pick); err == nil {
			t.Errorf("choose(%q) = %q, want an error", reply, got)
		}
	}
}

func TestOpenAIAsker(t *testing.T) {
	var got struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/chat/completions" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"split"}}],` +
			`"usage":{"prompt_tokens":42,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	a, err := NewAsker(Endpoint{API: review.APIOpenAI, Model: "m", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	q := Question{System: "sys", State: "the files", Ask: "Split it?", Yes: SaidSplit, No: SaidSingle}
	ans, err := a.Ask(context.Background(), q)
	if err != nil || ans.Said != SaidSplit || ans.HasP || ans.Usage.InputTokens != 42 {
		t.Errorf("Ask = %+v, %v", ans, err)
	}
	if got.Model != "m" || len(got.Messages) != 2 || !strings.Contains(got.Messages[1].Content, "Split it?") {
		t.Errorf("request body = %+v", got)
	}
	if _, err := NewAsker(Endpoint{API: review.APIOpenAI}); err == nil {
		t.Errorf("the vendor endpoint with no key was accepted")
	}
}

// The Jev wire sends the state and one noul question apart, and reads the
// probability of yes back from under the question's id.
func TestJevAsker(t *testing.T) {
	var got struct {
		Model     string `json:"model"`
		State     string `json:"state"`
		Questions map[string]struct {
			Type         string `json:"type"`
			Instructions string `json:"instructions"`
		} `json:"questions"`
	}
	reply := `{"model":"jev-1.14.0","answers":{"answer":{"type":"noul","noul":0.91}},` +
		`"usage":{"input_tokens":360,"output_tokens":39}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/systemone" {
			http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	a, err := NewAsker(Endpoint{API: APIJev, Model: DefaultJevModel, BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	q := Question{State: "the lookups", Ask: "Stop?", Yes: SaidStop, No: SaidLook}
	ans, err := a.Ask(context.Background(), q)
	if err != nil || ans.Said != SaidStop || !ans.HasP || ans.P != 0.91 || ans.Usage.InputTokens != 360 {
		t.Errorf("Ask = %+v, %v", ans, err)
	}
	asked := got.Questions["answer"]
	if got.Model != DefaultJevModel || got.State != "the lookups" || asked.Type != "noul" || asked.Instructions != "Stop?" {
		t.Errorf("request body = %+v", got)
	}
	reply = `{"answers":{"answer":{"type":"noul","noul":0.2}},"usage":{"input_tokens":1}}`
	if ans, err := a.Ask(context.Background(), q); err != nil || ans.Said != SaidLook {
		t.Errorf("a low probability read as %+v, %v", ans, err)
	}
	reply = `{"answers":{}}`
	if _, err := a.Ask(context.Background(), q); err == nil {
		t.Errorf("a reply with no answer was accepted")
	}
	if _, err := NewAsker(Endpoint{API: APIJev}); err == nil {
		t.Errorf("jev with no key was accepted")
	}
	if cost, ok := askCost("jev-latest", review.Usage{InputTokens: 1_000_000, OutputTokens: 500}); !ok || cost != jev.PerMtok {
		t.Errorf("a million Jev input tokens priced at %v, %v", cost, ok)
	}
}

func TestProbabilitiesGiveAThresholdTable(t *testing.T) {
	rep := collect(t)
	if err := rep.Ask(context.Background(), &fakeAsker{probs: true}, "jev-latest", 0, nil); err != nil {
		t.Fatal(err)
	}
	if p := rep.Runs[1].Asked.SplitP; p == nil || *p != 0.8 {
		t.Errorf("split probability = %v", p)
	}
	out := rep.Render()
	for _, want := range []string{"Stopping only when its probability of stop is at least:", "0.7       1 ", "0.9       0 ", "$0.0000"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	plain := collect(t)
	if err := plain.Ask(context.Background(), &fakeAsker{}, model, 0, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Render(), "probability of stop") {
		t.Errorf("a model that gave no probabilities got a threshold table")
	}
}

// A finding on a.go was counted against a lookup of data.go, because the
// path was looked for anywhere in the lookup's input.
func TestNamesFileNeedsTheWholePath(t *testing.T) {
	cases := []struct {
		input, file string
		want        bool
	}{
		{`{"path":"b/y.go","start":1}`, "b/y.go", true},
		{`{"path":"data.go"}`, "a.go", false},
		{`{"path":"vendor/b/y.go"}`, "b/y.go", false},
		{`{"path":"b/y.go.orig"}`, "b/y.go", false},
		{`{"pattern":"Reset","glob":"b/y.go"}`, "b/y.go", true},
		{`{"path":"data.go"} then a.go:12`, "a.go", true},
		{`{"path":"./b/y.go"}`, "b/y.go", true},
		{`{"path":"../b/y.go"}`, "b/y.go", false},
		{`{"path":"a.go"}`, "", false},
	}
	for _, c := range cases {
		if got := namesFile(c.input, c.file); got != c.want {
			t.Errorf("namesFile(%s, %q) = %v, want %v", c.input, c.file, got, c.want)
		}
	}
}

// A diff is cut by bytes, and the cut must not land inside a character.
func TestCutEndsOnAWholeCharacter(t *testing.T) {
	s := "ab" + "\u00e9" + "cd" // the e-acute is two bytes, at 2 and 3
	for n, want := range map[int]string{0: "", 2: "ab", 3: "ab", 4: "ab\u00e9", 99: s} {
		got := cut(s, n)
		if got != want || !utf8.ValidString(got) {
			t.Errorf("cut(%q, %d) = %q, want %q", s, n, got, want)
		}
	}
}

// Two reviews on one model that finish within the window of each other each
// get the row nearest to them, whichever is read first.
func TestMatchLedgerTakesTheClosestPairsFirst(t *testing.T) {
	at := func(sec int) time.Time { return day.Add(time.Duration(sec) * time.Second) }
	ledger := []review.Entry{{At: at(11), Model: model}, {At: at(1), Model: model}, {At: at(5), Model: "other"}}
	// The first trace is 9s from row 0 and 1s from row 1. The second is 1s
	// from row 0 and 11s from row 1.
	traces := []*postmortem.Trace{{Wrote: at(2), Model: model}, {Wrote: at(12), Model: model},
		{Wrote: at(500), Model: model}}
	got := matchLedger(ledger, traces)
	if len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Fatalf("got %v", got)
	}
	// Read the other way round, the pairing is the same.
	got = matchLedger(ledger, []*postmortem.Trace{traces[1], traces[0]})
	if got[0] != 0 || got[1] != 1 {
		t.Fatalf("reversed: got %v", got)
	}
}
