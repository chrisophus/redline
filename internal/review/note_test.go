package review

import (
	"context"
	"strings"
	"testing"
)

const testNote = "Check what the refresh button clears."

// An empty note sends exactly the request a run without notes sent: the same
// prompt, the same tail and the same estimate. Whitespace is no note.
func TestAnEmptyNoteChangesNothingAboutTheRequest(t *testing.T) {
	base, err := Assemble(exploreInput(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := exploreInput()
	in.Note = "  \n "
	blank, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if base.Prompt != blank.Prompt || base.Tail != blank.Tail || base.InputEstimate != blank.InputEstimate {
		t.Errorf("a blank note changed the request: tail %q, estimate %d against %d",
			blank.Tail, blank.InputEstimate, base.InputEstimate)
	}
	if base.Tail != judgingTail+describingTail {
		t.Errorf("a run with no note sends the judging and describing instructions alone in its tail, got %q", base.Tail)
	}
}

// The note rides behind the breakpoint on the one-call shape, so the prompt
// block every other call resends is unchanged, and it is priced.
func TestTheNoteGoesAfterThePromptAndIsPriced(t *testing.T) {
	base, err := Assemble(exploreInput(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := exploreInput()
	in.Note = testNote
	res, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Prompt != base.Prompt {
		t.Error("the note must not move the prompt block the cache is keyed on")
	}
	if !strings.HasSuffix(strings.TrimSpace(res.Tail), testNote) {
		t.Errorf("the note goes last, in the tail: %q", res.Tail)
	}
	if res.InputEstimate <= base.InputEstimate {
		t.Error("the note is sent, so it has to be priced")
	}
}

// Every judging call carries the note and nothing else does: the describing
// call is not judging, and the ruling weighs findings against code.
func TestOnlyTheJudgingCallsCarryTheNote(t *testing.T) {
	in := exploreInput()
	in.Note = testNote
	opts := Options{Cohorts: 3}.withDefaults()
	res, err := Assemble(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	carries := func(r *Result) bool { return strings.Contains(r.Prompt+r.Tail, testNote) }
	one := Cohort{Name: "queue", Summary: "s", Files: []string{"internal/queue/q.go"}}
	for name, r := range map[string]*Result{
		"the judging call after a walkthrough": res.judgingRequest(true),
		"the judging call without one":         res.judgingRequest(false),
		"a cohort call":                        res.cohortRequest(opts, one, []Cohort{one}, 0, 1),
	} {
		if !carries(r) {
			t.Errorf("%s does not carry the note", name)
		}
	}
	for name, r := range map[string]*Result{
		"the describing call":   res.synopsisRequest(opts, in),
		"the split's describer": res.cohortsRequest(opts, in),
		"the ruling":            res.ruleRequest(in, opts, nil, nil),
	} {
		if carries(r) {
			t.Errorf("%s carries the note, and it is not a judging call", name)
		}
	}
}

// On the wire: the describing call goes out without the note and the judging
// call with it, both reading the same cached prompt block.
func TestTheNoteReachesTheJudgingCallOnTheWire(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, flatCalls(StageSynopsis, synopsisBody)),
		anthropicSSE("tool_use", 10, 5, flatCalls(StageFindings, findingsBody)),
	)
	in := exploreInput()
	in.Note = testNote
	if _, err := Run(context.Background(), in, synopsisOpts(api)); err != nil {
		t.Fatal(err)
	}
	seen := api.seen()
	if len(seen) != 2 {
		t.Fatalf("two calls, got %d", len(seen))
	}
	if strings.Contains(string(seen[0]), testNote) {
		t.Error("the describing call must not carry the note")
	}
	if !strings.Contains(string(seen[1]), testNote) {
		t.Error("the judging call must carry the note")
	}
}
