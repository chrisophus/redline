package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const synopsisBody = `{"overview":"Queue drops the nil guard.","files":[` +
	`{"path":"internal/queue/q.go","summary":"Stops rejecting a nil entry before processing it."}]}`

const findingsBody = `{"comments":[{"file":"internal/queue/q.go","line":10,"severity":"warning",` +
	`"confidence":"high","category":"review","relatedFindings":[],"body":"process is called with a nil entry",` +
	`"question":{"kind":"diff","ask":"","subject":""}}],"verdicts":[]}`

func synopsisOpts(api *exploreAPI) Options {
	return Options{
		API: APIAnthropic, BaseURL: api.srv.URL, APIKey: "k",
		Model: "claude-sonnet-5", MaxTokens: 4096, MaxCostUSD: 5,
		Synopsis: true,
	}
}

// The point of the stage: the walkthrough comes off the describing call and
// the findings off the judging one, and the reader is handed one review with
// both. If the merge is wrong the run has paid for two calls and delivered
// one's worth.
func TestTheWalkthroughComesFromItsOwnCall(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t1", StageSynopsis, synopsisBody)),
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t2", StageFindings, findingsBody)),
	)
	res, err := Run(context.Background(), exploreInput(), synopsisOpts(api))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Synopsis || res.SynopsisFailed != "" {
		t.Fatalf("the stage did not run: synopsis=%v failed=%q", res.Synopsis, res.SynopsisFailed)
	}
	if res.Review.Overview != "Queue drops the nil guard." {
		t.Errorf("overview = %q, want the describing call's", res.Review.Overview)
	}
	if got := res.Review.Files["internal/queue/q.go"]; !strings.Contains(got, "nil entry") {
		t.Errorf("file line = %q, want the describing call's", got)
	}
	if len(res.Review.Comments) != 1 {
		t.Fatalf("the judging call's comments must survive the merge: %+v", res.Review.Comments)
	}
}

// Two calls, and everything ahead of the tail is the same bytes on both, which
// is what lets the second read the first's cache entry. The stage is only
// affordable because of that.
func TestBothStagesSendOneSharedPrefixAndDifferentTails(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t1", StageSynopsis, synopsisBody)),
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t2", StageFindings, findingsBody)),
	)
	opts := synopsisOpts(api)
	opts.Cache = true
	opts.CacheTTL = CacheTTL5m
	if _, err := Run(context.Background(), exploreInput(), opts); err != nil {
		t.Fatal(err)
	}
	seen := api.seen()
	if len(seen) != 2 {
		t.Fatalf("the stage sends two calls, got %d", len(seen))
	}
	var reqs []wireRequest
	for i, raw := range seen {
		var req wireRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("request %d is not the shape expected: %v", i, err)
		}
		reqs = append(reqs, req)
	}
	if reqs[0].System[0].Text != reqs[1].System[0].Text {
		t.Error("the two stages send different system blocks, so nothing before the prompt caches")
	}
	first, second := reqs[0].Messages[0].Content, reqs[1].Messages[0].Content
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("each stage sends the shared prefix and its own tail: %d and %d block(s)", len(first), len(second))
	}
	if first[0].Text != second[0].Text {
		t.Error("the block the cache entry is keyed on differs between the stages")
	}
	if !hasCacheControl(first[0]) || !hasCacheControl(second[0]) {
		t.Error("the shared block carries no breakpoint on one of the stages")
	}
	if first[1].Text == second[1].Text {
		t.Fatal("both stages sent the same instruction, so one of them was asked for the wrong thing")
	}
	if !strings.Contains(first[1].Text, "Do not judge it") {
		t.Errorf("the describing call was not told to describe: %q", first[1].Text)
	}
	if !strings.Contains(second[1].Text, "already written") {
		t.Errorf("the judging call was not told the walkthrough exists: %q", second[1].Text)
	}
}

// A describing call that breaks must not cost the review. The run falls back
// to the contract that writes its own walkthrough, and says which happened:
// a thin walkthrough from a fallback and a thin walkthrough from the model are
// otherwise the same artifact.
func TestAFailedDescribingCallFallsBackToTheWholeReview(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t1", StageSynopsis, `{"overview":"","files":[]}`)),
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t2", StageReview, reviewBody)),
	)
	var said []string
	opts := synopsisOpts(api)
	opts.Progress = func(s string) { said = append(said, s) }
	res, err := Run(context.Background(), exploreInput(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synopsis {
		t.Error("a describing call that wrote no overview must not be recorded as the source of one")
	}
	if res.SynopsisFailed == "" {
		t.Error("the fallback has to say why, or a thin walkthrough reads as the model's")
	}
	if len(res.Review.Comments) != 1 {
		t.Fatalf("the fallback must still produce a review: %+v", res.Review)
	}
	var warned bool
	for _, s := range said {
		if strings.Contains(s, "did not produce a walkthrough") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the fallback was silent on the terminal: %q", said)
	}
	var req wireRequest
	if err := json.Unmarshal(api.seen()[1], &req); err != nil {
		t.Fatal(err)
	}
	if got := req.Messages[0].Content; len(got) != 1 {
		t.Fatalf("the fallback call must send the prefix alone, got %d block(s)", len(got))
	}
}

// The describing call's output is not the review's output. The ledger takes a
// median of what a judging call writes and prices every later review against
// it, so a walkthrough folded into that number inflates every estimate after
// it - the same reason the ruling's output is kept apart.
func TestTheDescribingCallsOutputIsCountedApart(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 100, 40, anthropicToolUse(0, "t1", StageSynopsis, synopsisBody)),
		anthropicSSE("tool_use", 200, 7, anthropicToolUse(0, "t2", StageFindings, findingsBody)),
	)
	res, err := Run(context.Background(), exploreInput(), synopsisOpts(api))
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.OutputTokens != 7 {
		t.Errorf("output = %d, want the judging call's 7", res.Usage.OutputTokens)
	}
	if res.SynopsisOutputTokens != 40 {
		t.Errorf("synopsis output = %d, want 40", res.SynopsisOutputTokens)
	}
	if res.Usage.InputTokens != 300 {
		t.Errorf("input = %d, want both calls' 300: the describing call was billed too", res.Usage.InputTokens)
	}
}

// The tripwire refuses before anything is sent, so it has to know the run is
// two calls. Refusing after the describing call has been paid for is the
// failure it exists to prevent.
func TestTheTripwireCountsTheDescribingCall(t *testing.T) {
	in := exploreInput()
	one, err := Assemble(in, Options{Model: "claude-sonnet-5", MaxTokens: 64_000}.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	off := synopsisCeilingCost(Options{Model: "claude-sonnet-5", MaxTokens: 64_000}, one)
	if off != 0 {
		t.Errorf("with the stage off the ceiling must count nothing, got %f", off)
	}
	on := synopsisCeilingCost(Options{Model: "claude-sonnet-5", MaxTokens: 64_000, Synopsis: true}, one)
	if on <= 0 {
		t.Fatal("with the stage on the ceiling must count the second call")
	}
}
