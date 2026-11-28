package review

import (
	"context"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/gitx"
)

// diffOf is a diff of n added lines, long enough to cost real tokens.
func diffOf(path string, n int) string {
	var b strings.Builder
	b.WriteString("@@ -1,1 +1," + "1 @@\n")
	for i := 0; i < n; i++ {
		b.WriteString("+// " + path + " changes this line of the change\n")
	}
	return b.String()
}

func heldInput() Input {
	file := func(path string, n int) change.File {
		return change.File{Path: path, Status: "modified", Added: n, Diff: diffOf(path, n)}
	}
	// Modified tests are shown by default here, so all four diffs are in the
	// prompt until the ceiling forces some out.
	return Input{ShowModifiedTests: true, Change: &change.Set{Files: []change.File{
		file("a_test.go", 60),
		file("small.go", 20),
		file("big.go", 60),
		file("b_test.go", 30),
	}}}
}

// A ceiling that holds the file list and the two non-test diffs and nothing
// more.
func heldCeiling(t *testing.T, in Input, opts Options) int {
	t.Helper()
	opts = opts.withDefaults()
	all := in
	all.held = map[string]bool{}
	for _, f := range in.Change.Files {
		all.held[f.Path] = true
	}
	tight := opts
	tight.DeferContext, tight.heldDiffs = true, true
	base := all.fixedFor(tight).fixed
	room := 0
	for _, f := range in.Change.Files {
		if !change.IsTest(f.Path) {
			room += envelope.EstimateTokens(fileSection(f, nil))
		}
	}
	return base + room + 20
}

func TestOverTheCeilingDiffsAreHeldBackAndTheReviewStillRuns(t *testing.T) {
	in := heldInput()
	api := serveSSE(t,
		reply([2]string{CallDiff, `{"path":"a_test.go"}`}),
		reply([2]string{CallOverview, `{"overview":"Touches four files."}`},
			[2]string{CallFile, `{"path":"big.go","summary":"adds lines"}`},
			[2]string{CallFile, `{"path":"small.go","summary":"adds lines"}`},
			[2]string{CallDone, `{}`}),
	)
	opts := loopOpts(api)
	opts.Ceiling = heldCeiling(t, in, opts)

	out, err := Run(context.Background(), in, opts)
	if err != nil {
		t.Fatalf("a change over the ceiling must be reviewed, got %v", err)
	}
	if out.OverCeiling {
		t.Error("the held-back request fits the ceiling")
	}
	for _, shown := range []string{"### big.go", "### small.go"} {
		if !strings.Contains(out.Prompt, shown) {
			t.Errorf("non-test diff missing from the prompt: %s", shown)
		}
	}
	for _, held := range []string{"### a_test.go", "### b_test.go"} {
		if strings.Contains(out.Prompt, held) {
			t.Errorf("test diff should be held back: %s", held)
		}
	}
	for _, line := range []string{"- a_test.go (+60 -0, test)", "- b_test.go (+30 -0, test)"} {
		if !strings.Contains(out.Prompt, line) {
			t.Errorf("held-back list should carry %q", line)
		}
	}
	if strings.Contains(out.Prompt, "- big.go (+") {
		t.Error("a file whose diff is shown needs no line in the held-back list")
	}
	if !strings.Contains(out.System, "Use the tools to investigate") {
		t.Error("context should be deferred for the run")
	}

	if h := out.Held; h == nil || h.Total != 4 || len(h.Held) != 2 ||
		len(h.Read) != 1 || h.Read[0] != "a_test.go" {
		t.Fatalf("held accounting = %+v, want 2 of 4 held and a_test.go read", h)
	}
	if got := out.Held.Unread(); len(got) != 1 || got[0] != "b_test.go" {
		t.Errorf("unread = %v, want b_test.go", got)
	}

	// The diff came back in the conversation, rendered as the prompt would.
	second := string(api.seen()[1])
	if !strings.Contains(second, "a_test.go changes this line") {
		t.Error("the get_diff answer should ride in the second request")
	}
	if !strings.Contains(string(api.seen()[0]), `"name":"get_diff"`) {
		t.Error("get_diff should be on the catalogue")
	}
}

// A change under the ceiling builds the prompt it always did.
func TestUnderTheCeilingNothingIsHeldBack(t *testing.T) {
	in := heldInput()
	res, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Held != nil || res.diffs != nil || contains(res.looks(), CallDiff) {
		t.Error("nothing should be held back under the ceiling")
	}
	if strings.Contains(res.Prompt, "held back") {
		t.Error("no held-back list under the ceiling")
	}
	for _, tool := range callTools(true, true, false, nil) {
		if tool.Name == CallDiff {
			t.Error("get_diff must not be offered when no diffs are held back")
		}
	}
}

// With every diff held back the file list and fixed parts still do not fit:
// refused, with the message that names what the diff is made of.
func TestRefusedWhenEvenTheFileListIsOver(t *testing.T) {
	_, err := Run(context.Background(), heldInput(), Options{Ceiling: 1000, MaxCostUSD: 100})
	if err == nil || !strings.Contains(err.Error(), "against a 1000-token ceiling") {
		t.Fatalf("want the ceiling refusal, got %v", err)
	}
}

// Files the description names come before larger ones.
func TestNamedFilesAreShownBeforeLargerOnes(t *testing.T) {
	in := heldInput()
	in.Change.Files = append(in.Change.Files[:0:0], in.Change.Files...)
	in.Change.Commits = nil
	in.Change.Target = nil
	in.Change.Commits = append(in.Change.Commits, gitx.Commit{Subject: "Tidy small.go"})
	opts := Options{}.withDefaults()
	// Room for one non-test diff only.
	all := in
	all.held = map[string]bool{}
	for _, f := range in.Change.Files {
		all.held[f.Path] = true
	}
	tight := opts
	tight.DeferContext, tight.heldDiffs = true, true
	base := all.fixedFor(tight).fixed
	opts.Ceiling = base + envelope.EstimateTokens(fileSection(in.Change.Files[2], nil)) + 10
	got, _ := holdBack(in, opts)
	if got.held["small.go"] || !got.held["big.go"] {
		t.Errorf("held = %v, want small.go shown (named) and big.go held", got.held)
	}
}

func TestGetDiffAnswers(t *testing.T) {
	diffs := map[string]string{"x.go": "### x.go\n\ndiff\n"}
	if a, p := fetchDiff(diffs, []byte(`{"path":"x.go"}`)); p != "x.go" || !strings.Contains(a, "diff") {
		t.Errorf("held path should be served, got %q %q", a, p)
	}
	if a, p := fetchDiff(diffs, []byte(`{"path":"y.go"}`)); p != "" || !strings.Contains(a, "no held-back diff") {
		t.Errorf("unknown path should say so, got %q %q", a, p)
	}
	col := newCollector(StageReview, passExpect{}, nil, nil, false)
	_, _, rejected := col.take([]toolCall{{ID: "1", Name: CallDiff, Input: []byte(`{"path":"x.go"}`)}})
	if rejected != 1 {
		t.Error("get_diff must be refused when no diffs are held back")
	}
	col = newCollector(StageReview, passExpect{}, nil, nil, false).withDiffs(diffs)
	res, _, rejected := col.take([]toolCall{{ID: "1", Name: CallDiff, Input: []byte(`{"path":"x.go"}`)}})
	if rejected != 0 || len(res) != 1 || col.looked != 1 || len(col.diffsRead) != 1 {
		t.Errorf("get_diff should count as a lookup: rejected=%d looked=%d read=%v", rejected, col.looked, col.diffsRead)
	}
}
