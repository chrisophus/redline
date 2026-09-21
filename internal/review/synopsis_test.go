package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
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
		anthropicSSE("tool_use", 10, 5, flatCalls(StageSynopsis, synopsisBody)),
		anthropicSSE("tool_use", 10, 5, flatCalls(StageFindings, findingsBody)),
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

// ReuseSynopsis stands in for the describing call entirely: one call goes
// out, not two, and the walkthrough on the result is the one handed in, not
// one the run paid to write.
func TestReuseSynopsisSkipsTheDescribingCall(t *testing.T) {
	api := serveSSE(t, anthropicSSE("tool_use", 10, 5, flatCalls(StageFindings, findingsBody)))
	opts := synopsisOpts(api)
	opts.Synopsis = false
	opts.ReuseSynopsis = &findings.Review{
		Overview: "a reused walkthrough",
		Files:    map[string]string{"internal/queue/q.go": "a reused file summary"},
	}
	res, err := Run(context.Background(), exploreInput(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.seen()) != 1 {
		t.Fatalf("a reused walkthrough must not send a describing call, got %d call(s)", len(api.seen()))
	}
	if !res.Synopsis || !res.SynopsisReused {
		t.Fatalf("synopsis=%v synopsisReused=%v, want both true", res.Synopsis, res.SynopsisReused)
	}
	if res.Review.Overview != "a reused walkthrough" {
		t.Errorf("overview = %q, want the one handed in", res.Review.Overview)
	}
	if res.Review.Files["internal/queue/q.go"] != "a reused file summary" {
		t.Errorf("file line = %q, want the one handed in", res.Review.Files["internal/queue/q.go"])
	}
	if len(res.Review.Comments) != 1 {
		t.Fatalf("the judging call's comments must still land: %+v", res.Review.Comments)
	}
	if res.SynopsisOutputTokens != 0 {
		t.Errorf("synopsisOutputTokens = %d, want 0: nothing was written for it", res.SynopsisOutputTokens)
	}
}

// Two calls, and everything ahead of the tail is the same bytes on both, which
// is what lets the second read the first's cache entry. The stage is only
// affordable because of that.
func TestBothStagesSendOneSharedPrefixAndDifferentTails(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, flatCalls(StageSynopsis, synopsisBody)),
		anthropicSSE("tool_use", 10, 5, flatCalls(StageFindings, findingsBody)),
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
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("each pass sends the shared prefix, its calls block and its own instruction: %d and %d block(s)", len(first), len(second))
	}
	// The shared prefix is material only: the describing pass must not read
	// the judging instruction, nor the findings pass the describing one.
	if strings.Contains(first[0].Text, "## Finding defects") || strings.Contains(first[2].Text, "## Finding defects") {
		t.Error("the describing pass reads the judging instruction")
	}
	if !strings.Contains(second[2].Text, "## Finding defects") {
		t.Error("the findings pass does not read the judging instruction")
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
}

// A describing call that fails stops the run.
//
// It used to carry on and write the findings without a walkthrough, on the
// argument that a degraded review beats a lost one. That argument is for a
// call nobody can retry. This one can, and carrying on spent the judging
// call's price - the larger of the two - on a review the caller did not ask
// for, in a shape they would have to run again anyway.
//
// What the stop must not lose is the money already spent. The describing call
// was billed whether or not it answered, so its usage comes back with the
// error for the ledger to record.
func TestAFailedDescribingCallStopsTheRun(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("refusal", 10, 0),
		anthropicSSE("tool_use", 10, 5, flatCalls(StageReview, exploreReviewJSON)),
	)
	opts := synopsisOpts(api)
	opts.Cache, opts.CacheTTL = true, CacheTTL5m
	res, err := Run(context.Background(), exploreInput(), opts)
	if err == nil {
		t.Fatal("a describing call that produced no walkthrough must stop the run")
	}
	for _, want := range []string{"did not produce a walkthrough", "run again", "--no-synopsis"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error is missing %q: %v", want, err)
		}
	}
	// The judging call must not have been sent: it is the expensive half and
	// the whole point of stopping.
	if res != nil && len(res.Review.Comments) > 0 {
		t.Errorf("the judging call was sent anyway: %+v", res.Review)
	}
	// And the describing call's cost survives, or the ledger loses a paid call.
	if res == nil || res.Usage.InputTokens == 0 {
		t.Errorf("the describing call's usage must come back for the ledger: %+v", res)
	}
}

// The describing call's output is not the review's output. The ledger takes a
// median of what a judging call writes and prices every later review against
// it, so a walkthrough folded into that number inflates every estimate after
// it - the same reason the ruling's output is kept apart.
func TestTheDescribingCallsOutputIsCountedApart(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 100, 40, flatCalls(StageSynopsis, synopsisBody)),
		anthropicSSE("tool_use", 200, 7, flatCalls(StageFindings, findingsBody)),
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
	off := synopsisCeilingCost(Options{Model: "claude-sonnet-5", MaxTokens: 64_000}, in, one)
	if off != 0 {
		t.Errorf("with the stage off the ceiling must count nothing, got %f", off)
	}
	on := synopsisCeilingCost(Options{Model: "claude-sonnet-5", MaxTokens: 64_000, Synopsis: true}, in, one)
	if on <= 0 {
		t.Fatal("with the stage on the ceiling must count the second call")
	}
}

// The describing call writes a line per file, and the change section names
// held-back test files with their line counts and no diff, which reads as a
// file to write a line about: the first fixture sweep came back with ten such
// lines and every invented line in the run was one. Prose did not stop it -
// the instruction already said "none of the files held back" - so the tail
// carries the roster.
func TestTheDescribingTurnNamesTheFilesItMayDescribe(t *testing.T) {
	in := exploreInput()
	in.Change.Files = append(in.Change.Files, change.File{
		Path: "internal/queue/q_test.go", Added: 40,
	})
	tail := synopsisTail(in)
	if !strings.Contains(tail, "- internal/queue/q.go\n") {
		t.Errorf("the roster must name the file whose diff was sent:\n%s", tail)
	}
	if strings.Contains(tail, "q_test.go") {
		t.Errorf("the roster must not name a file whose diff was held back:\n%s", tail)
	}
	// The same set the score reads, which is the point of taking it from the
	// producer's own predicate: a roster and a measurement that disagree
	// would report the model wrong for obeying the instruction.
	shown := in.ShownFiles()
	if len(shown) != 1 || !shown["internal/queue/q.go"] {
		t.Fatalf("the shown set and the roster are built from one predicate: %v", shown)
	}
}
