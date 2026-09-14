package review

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// anthropicSignedThinking is a thinking block with its signature, streamed the way
// the endpoint sends one. Turn 2 has to resend both as they arrived.
func anthropicSignedThinking(index int, thinking, signature string) string {
	th, _ := json.Marshal(thinking)
	sig, _ := json.Marshal(signature)
	return fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,"+
		"\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n"+
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,"+
		"\"delta\":{\"type\":\"thinking_delta\",\"thinking\":%s}}\n\n"+
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,"+
		"\"delta\":{\"type\":\"signature_delta\",\"signature\":%s}}\n\n"+
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n",
		index, index, th, index, sig, index)
}

func stepwiseInput(t *testing.T) Input {
	t.Helper()
	in := exploreInput()
	in.Report = priors()
	if in.priorsSection() == "" {
		t.Fatal("the fixture needs prior findings, or nothing below can tell turn 1 from turn 2")
	}
	return in
}

func stepwiseOpts(api *exploreAPI) Options {
	return Options{
		API: APIAnthropic, BaseURL: api.srv.URL, APIKey: "k",
		Model: "claude-sonnet-5", MaxTokens: 4096, MaxCostUSD: 5,
		Pipeline: PipelineStepwise, Cache: true, CacheTTL: CacheTTL5m,
	}
}

// stepwiseWire is a request as the endpoint receives it, with the parts the
// cache is keyed on kept as raw bytes so they can be compared as bytes.
type stepwiseWire struct {
	System     json.RawMessage `json:"system"`
	Tools      json.RawMessage `json:"tools"`
	ToolChoice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tool_choice"`
	Messages []json.RawMessage `json:"messages"`
}

type stepwiseMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

func decodeStepwise(t *testing.T, raw []byte) (stepwiseWire, []stepwiseMessage) {
	t.Helper()
	var req stepwiseWire
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("the request is not the shape expected: %v\n%s", err, raw)
	}
	msgs := make([]stepwiseMessage, len(req.Messages))
	for i, m := range req.Messages {
		if err := json.Unmarshal(m, &msgs[i]); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	return req, msgs
}

// The shape of the conversation, on the wire. Turn 1 is the change and the
// diff and nothing else. Turn 2 resends turn 1's user turn byte for byte,
// then the assistant turn exactly as the model wrote it, thinking and its
// signature included, then a user turn that answers the tool call and carries
// the rest of the packet. Everything the cache is keyed on ahead of the
// breakpoint is identical across the two calls, or turn 2 pays for turn 1
// twice.
func TestTheStepwiseConversationDisclosesThePacketInOrder(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 100, 40,
			anthropicSignedThinking(0, "the guard is gone", "sig-1"),
			anthropicToolUse(1, "toolu_1", StageSynopsis, synopsisBody)),
		anthropicSSE("tool_use", 200, 7, anthropicToolUse(0, "toolu_2", StageFindings, findingsBody)),
	)
	in := stepwiseInput(t)
	opts := stepwiseOpts(api)
	captured := map[string][]byte{}
	opts.Capture = func(name string, data []byte) { captured[name] = data }
	res, err := Run(context.Background(), in, opts)
	if err != nil {
		t.Fatal(err)
	}
	seen := api.seen()
	if len(seen) != 2 {
		t.Fatalf("a stepwise run is two calls, got %d", len(seen))
	}
	first, one := decodeStepwise(t, seen[0])
	second, two := decodeStepwise(t, seen[1])
	envText := in.Envelopes[0].Expansions[0].Content

	// Turn 1.
	if first.ToolChoice.Name != StageSynopsis || second.ToolChoice.Name != StageFindings {
		t.Errorf("the turns forced %q and %q, want %s then %s",
			first.ToolChoice.Name, second.ToolChoice.Name, StageSynopsis, StageFindings)
	}
	if len(one) != 1 || one[0].Role != "user" || len(one[0].Content) != 1 {
		t.Fatalf("turn 1 is one user turn of one block: %+v", one)
	}
	opening, _ := one[0].Content[0]["text"].(string)
	if !strings.Contains(opening, in.changeSection()) || !strings.Contains(opening, in.diffSection()) {
		t.Errorf("turn 1 must carry the change description and the diff:\n%s", opening)
	}
	if strings.Contains(opening, in.priorsSection()) || strings.Contains(opening, strings.TrimSpace(envText)) {
		t.Errorf("turn 1 was shown material it is meant to describe without:\n%s", opening)
	}
	if !strings.Contains(opening, "Do not judge it yet") {
		t.Errorf("turn 1 was not told to describe:\n%s", opening)
	}
	if _, ok := one[0].Content[0]["cache_control"]; !ok {
		t.Error("the end of turn 1's user content carries no breakpoint, so turn 2 pays for it again")
	}

	// Turn 2.
	if string(first.System) != string(second.System) {
		t.Error("the two turns send different system blocks")
	}
	if string(first.Tools) != string(second.Tools) {
		t.Error("the two turns send different tool arrays, which invalidates everything cached behind them")
	}
	if len(second.Messages) != 3 {
		t.Fatalf("turn 2 is turn 1, the answer and a new user turn, got %d message(s)", len(second.Messages))
	}
	if string(second.Messages[0]) != string(first.Messages[0]) {
		t.Errorf("turn 2 resent turn 1's user turn with different bytes:\n%s\n%s",
			first.Messages[0], second.Messages[0])
	}
	answer := two[1]
	if answer.Role != "assistant" || len(answer.Content) != 2 {
		t.Fatalf("the assistant turn must come back whole: %+v", answer)
	}
	if th := answer.Content[0]; th["type"] != "thinking" || th["thinking"] != "the guard is gone" || th["signature"] != "sig-1" {
		t.Errorf("the thinking block must be resent as the model produced it: %+v", th)
	}
	call := answer.Content[1]
	if call["type"] != "tool_use" || call["id"] != "toolu_1" || call["name"] != StageSynopsis {
		t.Errorf("the tool call must be resent as the model made it: %+v", call)
	}
	var wrote any
	if err := json.Unmarshal([]byte(synopsisBody), &wrote); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(call["input"], wrote) {
		t.Errorf("the tool call's input changed on the way back: %v", call["input"])
	}
	next := two[2]
	if next.Role != "user" || len(next.Content) != 3 {
		t.Fatalf("turn 2's user turn is the result, the rest of the packet and the instruction: %+v", next)
	}
	if r := next.Content[0]; r["type"] != "tool_result" || r["tool_use_id"] != "toolu_1" {
		t.Errorf("turn 2 must open by answering turn 1's call: %+v", r)
	}
	rest, _ := next.Content[1]["text"].(string)
	if !strings.Contains(rest, in.priorsSection()) || !strings.Contains(rest, strings.TrimSpace(envText)) {
		t.Errorf("turn 2 must carry the prior findings and the context:\n%s", rest)
	}
	if tail, _ := next.Content[2]["text"].(string); !strings.Contains(tail, "already written") {
		t.Errorf("turn 2 was not given the judging instruction: %q", tail)
	}
	for i, b := range next.Content {
		if _, ok := b["cache_control"]; ok {
			t.Errorf("turn 2's block %d is marked, which writes an entry nothing reads", i)
		}
	}

	// The result.
	if res.Pipeline != PipelineStepwise || res.FellBack != "" || !res.Synopsis {
		t.Errorf("the run did not stay stepwise: pipeline=%q fellBack=%q synopsis=%v",
			res.Pipeline, res.FellBack, res.Synopsis)
	}
	if res.Review.Overview != "Queue drops the nil guard." || len(res.Review.Files) != 1 {
		t.Errorf("the walkthrough must come from turn 1: %+v", res.Review)
	}
	if len(res.Review.Comments) != 1 {
		t.Errorf("the comments must come from turn 2: %+v", res.Review.Comments)
	}
	if res.Turns != 2 || res.ServedModel != "claude-sonnet-5" {
		t.Errorf("turns=%d served=%q", res.Turns, res.ServedModel)
	}
	if res.Usage.InputTokens != 300 || res.Usage.OutputTokens != 7 || res.SynopsisOutputTokens != 40 {
		t.Errorf("usage = %+v, synopsis output %d; want both turns' input, turn 2's output, and turn 1's apart",
			res.Usage, res.SynopsisOutputTokens)
	}
	c, p := strings.Index(res.Prompt, in.changeSection()), strings.Index(res.Prompt, in.priorsSection())
	if c < 0 || p < 0 || c > p || res.Tail != findingsPrompt {
		t.Errorf("the trace must be turn 1's text, then turn 2's, then the instruction:\n%s\n---\n%s", res.Prompt, res.Tail)
	}

	// The captures.
	if _, ok := captured["stepwise-turn1-"+StageSynopsis+".request.json"]; !ok {
		t.Errorf("turn 1 was not captured under its own name: %v", keys(captured))
	}
	req2, ok := captured["stepwise-turn2-"+StageFindings+".request.json"]
	if !ok {
		t.Fatalf("turn 2 was not captured under its own name: %v", keys(captured))
	}
	if !strings.Contains(string(req2), `"history"`) {
		t.Error("the captured turn 2 leaves out the turns it resent")
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Turn 1 never costs the review. A turn that refuses or writes no overview
// leaves the run on the one-shot contract, billed for both calls, and says
// why, so the row is not read as a stepwise run.
func TestAFailedFirstTurnFallsBackToOneCall(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first string
	}{
		{"a refusal", anthropicSSE("refusal", 100, 0)},
		{"no overview", anthropicSSE("tool_use", 100, 5,
			anthropicToolUse(0, "toolu_1", StageSynopsis, `{"overview":"","files":[]}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := serveSSE(t, tc.first,
				anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t2", StageReview, reviewBody)))
			var said []string
			opts := stepwiseOpts(api)
			opts.Progress = func(s string) { said = append(said, s) }
			res, err := Run(context.Background(), stepwiseInput(t), opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Pipeline != PipelineOneShot || res.FellBack == "" || res.SynopsisFailed == "" || res.Synopsis {
				t.Errorf("the fallback must be recorded: pipeline=%q fellBack=%q synopsisFailed=%q synopsis=%v",
					res.Pipeline, res.FellBack, res.SynopsisFailed, res.Synopsis)
			}
			if len(res.Review.Comments) != 1 || res.Review.Overview == "" {
				t.Errorf("the fallback must produce a whole review: %+v", res.Review)
			}
			if res.Usage.InputTokens != 110 {
				t.Errorf("input = %d, want both calls' 110: turn 1 was billed", res.Usage.InputTokens)
			}
			seen := api.seen()
			if len(seen) != 2 {
				t.Fatalf("the fallback is one call after turn 1, got %d", len(seen))
			}
			req, _ := decodeStepwise(t, seen[1])
			if len(req.Messages) != 1 || req.ToolChoice.Name != StageReview {
				t.Errorf("the fallback call is one user turn under the review contract: %d message(s), tool %q",
					len(req.Messages), req.ToolChoice.Name)
			}
			if !slices.ContainsFunc(said, func(s string) bool { return strings.Contains(s, "did not produce a walkthrough") }) {
				t.Errorf("the fallback was silent: %q", said)
			}
		})
	}
}

// Nothing is sent for a combination the conversation cannot run under. The
// OpenAI case is the one that matters: an option that quietly behaved
// differently on that wire is how Brief came apart between the two.
func TestStepwiseRefusesWhatItCannotRunUnder(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 1, 1))
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"the openai wire", Options{API: APIOpenAI}, "only built on the Anthropic wire"},
		{"samples", Options{Samples: 3}, "--samples"},
		{"brief", Options{Brief: true}, "--brief"},
		{"explore", Options{Mode: ModeExplore}, "--mode explore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.opts
			o.BaseURL, o.APIKey, o.MaxCostUSD = api.srv.URL, "k", 5
			o.Pipeline = PipelineStepwise
			_, err := Run(context.Background(), exploreInput(), o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
	if n := len(api.seen()); n != 0 {
		t.Errorf("a refused run sent %d request(s)", n)
	}
	if _, _, err := RunBatch(context.Background(), []Input{exploreInput()}, Options{Pipeline: PipelineStepwise}); err == nil {
		t.Error("a stepwise run cannot be batched, and the batch path must say so")
	}
}

// The tripwire refuses before turn 1 is sent, so it has to price turn 2 too.
// Refusing after turn 1 has been paid for is the failure it exists to prevent.
func TestTheTripwirePricesBothTurns(t *testing.T) {
	in := exploreInput()
	base := Options{Model: "claude-sonnet-5", MaxTokens: 64_000}.withDefaults()
	flat, err := Assemble(in, base)
	if err != nil {
		t.Fatal(err)
	}
	if got := stepwiseCeilingCost(base, in, flat); got != 0 {
		t.Errorf("a one-shot run must add nothing, got %f", got)
	}
	step := base
	step.Pipeline = PipelineStepwise
	res, err := Assemble(in, step)
	if err != nil {
		t.Fatal(err)
	}
	first := res.stepwiseDescribeRequest(step, in).CostCeilingUSD
	extra := stepwiseCeilingCost(step, in, res)
	if extra <= first {
		t.Fatalf("the ceiling priced turn 1 alone: %f against turn 1's %f", extra, first)
	}
	cached := step
	cached.Cache = true
	if got := stepwiseCeilingCost(cached, in, res); got >= extra {
		t.Errorf("with the breakpoint on, turn 2 reads turn 1 at the cached rate: %f, uncached %f", got, extra)
	}

	limit := res.CostCeilingUSD + extra/2
	refused := step
	refused.DryRun, refused.MaxCostUSD = true, limit
	if _, err := Run(context.Background(), in, refused); err == nil || !strings.Contains(err.Error(), "stepwise") {
		t.Errorf("a stepwise run over the limit must be refused and say what it is: %v", err)
	}
	oneshot := base
	oneshot.DryRun, oneshot.MaxCostUSD = true, limit
	if _, err := Run(context.Background(), in, oneshot); err != nil {
		t.Errorf("the one-shot run under the same limit must pass, or the test above proves nothing: %v", err)
	}
}

// The findings contract has no file lines, so a clean answer and a reply that
// never started differ only in whether anything was dropped as a stub.
func TestAFindingsReplyOfStubsIsRefused(t *testing.T) {
	stubbed := &Result{Stage: StageFindings, FilesShown: 1, Pipeline: PipelineStepwise}
	err := stubbed.absorb(Options{}.withDefaults(), StageFindings, completion{
		text: cohortFindings("internal/queue/q.go", "placeholder"), fromTool: true, stopReason: "tool_use",
	})
	if err == nil {
		t.Error("a findings reply whose every comment was a stub must not read as a clean change")
	}
	clean := &Result{Stage: StageFindings, FilesShown: 1, Pipeline: PipelineStepwise}
	if err := clean.absorb(Options{}.withDefaults(), StageFindings, completion{
		text: `{"comments":[],"verdicts":[]}`, fromTool: true, stopReason: "tool_use",
	}); err != nil {
		t.Errorf("zero comments is the answer on a clean change and has to stay reachable: %v", err)
	}
}

// Turn 1, turn 2 and the fallback all name a contract in the one array, so it
// is the same bytes on all three.
func TestTheStepwiseCatalogueCarriesEveryTurnsContract(t *testing.T) {
	var names []string
	for _, tool := range stageTools(Options{Pipeline: PipelineStepwise}) {
		names = append(names, tool.Name)
	}
	for _, want := range []string{StageSynopsis, StageFindings, StageReview} {
		if !slices.Contains(names, want) {
			t.Errorf("the stepwise catalogue is missing %s: %v", want, names)
		}
	}
}
