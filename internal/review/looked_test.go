package review

import (
	"context"
	"strings"
	"testing"
)

// A --look pass's searches and reads reach Result the same way get_context's
// fetches do: as a count, not only as whatever the model narrates about them.
func TestLookedReachesTheResult(t *testing.T) {
	api := serveSSE(t,
		reply(
			[2]string{CallGrep, `{"pattern":"Insert","glob":""}`},
			[2]string{CallRead, `{"path":"internal/queue/q.go","start_line":1,"end_line":5}`},
		),
		reply([2]string{CallComment, goodComment}, [2]string{CallDone, `{}`}),
	)
	look := &fakeLooker{answer: "store.go:3-5\n3\tfunc Insert() error {\n"}
	opts := loopOpts(api)
	opts.Look = look
	res := &Result{System: "s", Prompt: "p", Stage: StageFindings, FilesShown: 1}
	out, err := runOnce(context.Background(), exploreInput(), opts, res)
	if err != nil {
		t.Fatal(err)
	}
	if out.Looked != 2 {
		t.Errorf("looked = %d, want 2 (one grep, one read)", out.Looked)
	}
	if len(look.greps) != 1 || len(look.reads) != 1 {
		t.Errorf("greps=%v reads=%v, want one of each reaching the tree", look.greps, look.reads)
	}
}

// foldCalls and clone carry Looked the way they carry Fetched: a result that
// stands for several passes sums it, and a fresh request off an old one
// starts it at zero.
func TestLookedFoldsAndClonesLikeFetched(t *testing.T) {
	r := &Result{Looked: 3}
	other := &Result{Looked: 4}
	r.foldCalls(other)
	if r.Looked != 7 {
		t.Errorf("folded looked = %d, want 7", r.Looked)
	}
	c := r.clone()
	if c.Looked != 0 {
		t.Errorf("cloned looked = %d, want 0", c.Looked)
	}
}

// Without a debug line naming what grep and read_lines were actually asked,
// the only evidence of what a --look pass did is the model's own narration
// of it, which is not evidence at all.
func TestLookupCallsAreLoggedUnderDebug(t *testing.T) {
	api := serveSSE(t,
		reply(
			[2]string{CallGrep, `{"pattern":"Insert","glob":"internal/"}`},
			[2]string{CallRead, `{"path":"internal/queue/q.go","start_line":1,"end_line":5}`},
		),
		reply([2]string{CallComment, goodComment}, [2]string{CallDone, `{}`}),
	)
	opts := loopOpts(api)
	opts.Look = &fakeLooker{answer: "ok"}
	var lines []string
	opts.Debug = func(s string) { lines = append(lines, s) }
	res := &Result{System: "s", Prompt: "p", Stage: StageFindings, FilesShown: 1}
	if _, err := runOnce(context.Background(), exploreInput(), opts, res); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "grep") || !strings.Contains(joined, `"pattern":"Insert"`) {
		t.Errorf("debug output does not name the grep call's own input: %s", joined)
	}
	if !strings.Contains(joined, "read_lines") || !strings.Contains(joined, `"path":"internal/queue/q.go"`) {
		t.Errorf("debug output does not name the read_lines call's own input: %s", joined)
	}
}
