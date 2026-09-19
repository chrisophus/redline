package review

import (
	"encoding/json"
	"strings"
	"testing"
)

// fakeLooker answers with what it was asked, so a test can tell a call that
// reached the tree from one that was rejected before it got there.
type fakeLooker struct {
	greps  []string
	reads  []string
	answer string
	err    error
}

func (f *fakeLooker) Grep(pattern, glob string) (string, error) {
	f.greps = append(f.greps, pattern+"|"+glob)
	return f.answer, f.err
}

func (f *fakeLooker) ReadLines(path string, start, end int) (string, error) {
	f.reads = append(f.reads, path)
	return f.answer, f.err
}

// Both tools are off unless the run was given somewhere to look. A catalogue
// that offered them anyway would be a pass told it can check a claim and then
// answered "no lookup is available".
func TestTheLookupToolsAreOffWithoutALooker(t *testing.T) {
	names := func(looks bool) []string {
		var out []string
		for _, tl := range callTools(false, looks) {
			out = append(out, tl.Name)
		}
		return out
	}
	off := strings.Join(names(false), ",")
	if strings.Contains(off, CallGrep) || strings.Contains(off, CallRead) {
		t.Errorf("tools = %s, want neither lookup", off)
	}
	on := strings.Join(names(true), ",")
	if !strings.Contains(on, CallGrep) || !strings.Contains(on, CallRead) {
		t.Errorf("tools = %s, want both lookups", on)
	}
}

// The catalogue is fixed for a run and the permission is per pass. Checking a
// claim is what a judging pass does; a describing pass has nothing to check,
// and the ruling was assembled around the answers of a lookup pass of its own.
func TestOnlyTheJudgingPassesMayLookThingsUp(t *testing.T) {
	for _, tc := range []struct {
		stage string
		want  bool
	}{
		{StageFindings, true},
		{StageReview, true},
		{StageSynopsis, false},
		{StageRuling, false},
	} {
		got := strings.Join(callsFor(tc.stage, false, true), ",")
		if has := strings.Contains(got, CallGrep); has != tc.want {
			t.Errorf("%s may call %s: got %v, want %v (calls = %s)", tc.stage, CallGrep, has, tc.want, got)
		}
	}
}

// A lookup answers with what the tree said and records nothing, so it neither
// completes a pass nor counts toward one -- the same bargain get_context makes.
func TestALookupAnswersWithTheTreeAndRecordsNothing(t *testing.T) {
	look := &fakeLooker{answer: "store.go:3-5\n3\tfunc Insert() error {\n"}
	c := newCollector(StageFindings, passExpect{}, nil, look)
	input, err := json.Marshal(map[string]any{"path": "store.go", "start_line": 3, "end_line": 5})
	if err != nil {
		t.Fatal(err)
	}
	results, done, rejected := c.take([]toolCall{{ID: "t1", Name: CallRead, Input: input}})
	if rejected != 0 || done {
		t.Fatalf("rejected=%d done=%v; a read is neither refused nor an ending", rejected, done)
	}
	if len(results) != 1 || results[0].isError {
		t.Fatalf("results = %+v, want the span back", results)
	}
	if !strings.Contains(results[0].content, "func Insert") {
		t.Errorf("content = %q, want what the tree said", results[0].content)
	}
	if len(look.reads) != 1 || look.reads[0] != "store.go" {
		t.Errorf("reads = %v, want the path as asked", look.reads)
	}
	if c.looked != 1 {
		t.Errorf("looked = %d, want the lookup counted for the trace", c.looked)
	}
}

// A failed lookup is an answer, not a rejection. "Not recorded" reads as a call
// this pass may not make, which is a different and wrong correction.
func TestAFailedLookupComesBackAsTheAnswer(t *testing.T) {
	look := &fakeLooker{err: errBadPattern{}}
	c := newCollector(StageFindings, passExpect{}, nil, look)
	input, err := json.Marshal(map[string]any{"pattern": "("})
	if err != nil {
		t.Fatal(err)
	}
	results, _, rejected := c.take([]toolCall{{ID: "t1", Name: CallGrep, Input: input}})
	if rejected != 0 {
		t.Errorf("rejected = %d, want the error carried as the answer", rejected)
	}
	if len(results) != 1 || results[0].isError {
		t.Fatalf("results = %+v, want a plain answer", results)
	}
	if !strings.Contains(results[0].content, "bad pattern") {
		t.Errorf("content = %q, want the reason the pass can act on", results[0].content)
	}
}

type errBadPattern struct{}

func (errBadPattern) Error() string { return "bad pattern: missing closing )" }

// A search that matched nothing says so. A pass handed an empty string cannot
// tell "nothing matches" -- which is an answer -- from a search that failed.
func TestAnEmptySearchSaysSo(t *testing.T) {
	c := newCollector(StageFindings, passExpect{}, nil, &fakeLooker{answer: ""})
	input, err := json.Marshal(map[string]any{"pattern": "nothing"})
	if err != nil {
		t.Fatal(err)
	}
	results, _, _ := c.take([]toolCall{{ID: "t1", Name: CallGrep, Input: input}})
	if len(results) != 1 || !strings.Contains(results[0].content, "no match") {
		t.Fatalf("results = %+v, want an explicit no-match", results)
	}
}
