package review

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
)

type thinkingWire struct {
	ToolChoice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tool_choice"`
	Thinking map[string]any `json:"thinking"`
	Messages []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

func sentThinkingWire(t *testing.T) thinkingWire {
	t.Helper()
	api := serveSSE(t, anthropicSSE("tool_use", 10, 5, anthropicText(0, "{}")))
	if _, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 2000,
	}, &Result{System: "s", Prompt: "p", Stage: StageReview}); err != nil {
		t.Fatal(err)
	}
	var got thinkingWire
	if err := json.Unmarshal(api.seen()[0], &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The tool choice is never pinned, on Sonnet 5 or any other model: forcing it
// is incompatible with thinking on this API. Every call leaves the choice to
// the model, says in words which calls answer it, and asks for adaptive
// thinking with its summary on the stream — the model thinks by default once
// the choice is not pinned, so asking is the only way to see it happen rather
// than pay for it invisibly.
func TestEveryCallOffersTheToolAndAsksForAdaptiveThinking(t *testing.T) {
	got := sentThinkingWire(t)
	if got.ToolChoice.Type != "auto" {
		t.Errorf("tool_choice %q, want auto: the choice is never pinned", got.ToolChoice.Type)
	}
	if got.Thinking["type"] != "adaptive" || got.Thinking["display"] != "summarized" {
		t.Errorf("thinking %v, want adaptive with a summarized display", got.Thinking)
	}
	var said bool
	for _, m := range got.Messages {
		for _, c := range m.Content {
			said = said || strings.Contains(c.Text, "Answer by calling the tools")
		}
	}
	if !said {
		t.Error("an unpinned call must be told in words which tool answers it")
	}
}

// The count arrives on the stream's last message_delta and is the only
// evidence of whether the model thought, since Sonnet 5 hides the text.
func TestThinkingTokensAreReadFromTheStream(t *testing.T) {
	sse := strings.Replace(anthropicSSE("tool_use", 10, 50, anthropicText(0, "{}")),
		`"usage":{"output_tokens":50}}`, `"usage":{"output_tokens":50,"output_tokens_details":{"thinking_tokens":37}}}`, 1)
	api := serveSSE(t, sse)
	c, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
	}, &Result{System: "s", Prompt: "p", Stage: StageReview})
	if err != nil {
		t.Fatal(err)
	}
	if c.usage.ThinkingTokens != 37 || c.usage.OutputTokens != 50 {
		t.Errorf("usage %+v, want 37 thinking tokens inside 50 output", c.usage)
	}
}

func TestTheOpenAIWireNeverPinsTheChoice(t *testing.T) {
	var choice any
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		choice = req.ToolChoice
		_, _ = w.Write([]byte(toolReply(t, `{"overview":"x","files":[],"comments":[],"verdicts":[]}`, "tool_calls",
			`{"prompt_tokens":10,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":12}}`)))
	})
	c, err := completeOpenAI(context.Background(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
	}, &Result{System: "s", Prompt: "p", Stage: StageReview})
	if err != nil {
		t.Fatal(err)
	}
	if choice != "auto" {
		t.Errorf("tool_choice %v, want auto", choice)
	}
	if c.usage.ThinkingTokens != 12 {
		t.Errorf("usage %+v, want the 12 reasoning tokens counted as thinking", c.usage)
	}
}

// Every part is sized, and the parts account for the whole request the
// one-line total prices plus the tools it leaves out.
func TestTheRequestIsSizedByPart(t *testing.T) {
	res, err := Assemble(exploreInput(), Options{Model: "claude-sonnet-5", API: APIAnthropic})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	sum := 0
	for _, p := range res.Parts {
		byName[p.Name] = p.Tokens
		sum += p.Tokens
	}
	if byName["diff"] == 0 || byName["system"] == 0 || byName["tools"] == 0 {
		t.Fatalf("parts %+v, want the diff, the system block and the tools sized", res.Parts)
	}
	want := res.InputEstimate + byName["tools"]
	if math.Abs(float64(sum-want)) > 0.02*float64(want)+10 {
		t.Errorf("parts sum to %d, want about %d (the estimate plus the tools)", sum, want)
	}
	line := res.PartsLine()
	if !strings.Contains(line, "diff ") || !strings.Contains(line, "room") {
		t.Errorf("parts line %q does not name the diff and the context's room", line)
	}
}

// --debug keeps the thinking summary the stream carried, beside the answer it
// led to, and keeps nothing under that key when there was none.
func TestTheCaptureKeepsTheThinkingSummary(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 1000, 60,
		anthropicThinking(0, "the guard was added after a panic in production"),
		anthropicText(1, exploreReviewJSON)))
	opts := oneShotOpts(api)
	captured := map[string][]byte{}
	opts.Capture = func(name string, data []byte) { captured[name] = data }
	if _, err := Run(context.Background(), exploreInput(), opts); err != nil {
		t.Fatalf("review: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(captured["review.response.json"], &got); err != nil {
		t.Fatal(err)
	}
	if s, _ := got["thinking"].(string); !strings.Contains(s, "added after a panic") {
		t.Errorf("thinking = %q, want the summary the stream carried", got["thinking"])
	}

	plain := serveSSE(t, anthropicSSE("end_turn", 1000, 60, anthropicText(0, exploreReviewJSON)))
	opts = oneShotOpts(plain)
	captured = map[string][]byte{}
	opts.Capture = func(name string, data []byte) { captured[name] = data }
	if _, err := Run(context.Background(), exploreInput(), opts); err != nil {
		t.Fatalf("review: %v", err)
	}
	got = nil
	if err := json.Unmarshal(captured["review.response.json"], &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["thinking"]; ok {
		t.Errorf("a response with no thinking captured a thinking key: %v", got["thinking"])
	}
}

// callsBlock's text goes out as its own content block, on the wire but not
// otherwise recorded: "calls" in the capture names which tools answer the
// pass, not the words that tell the model so. A reader asking whether a
// deferred-context pass was actually told to read before writing has to see
// those words, not reconstruct them from source.
func TestTheCaptureKeepsTheInstructionsSentWithTheCall(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 1000, 60, anthropicText(0, exploreReviewJSON)))
	opts := oneShotOpts(api)
	opts.DeferContext = true
	captured := map[string][]byte{}
	opts.Capture = func(name string, data []byte) { captured[name] = data }
	if _, err := Run(context.Background(), exploreInput(), opts); err != nil {
		t.Fatalf("review: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(captured["review.request.json"], &got); err != nil {
		t.Fatal(err)
	}
	instructions, _ := got["instructions"].(string)
	if want := callsBlock(StageReview, true); instructions != want {
		t.Errorf("instructions = %q, want %q", instructions, want)
	}
	if !strings.Contains(instructions, "Read the context you need with get_context") {
		t.Error("the deferred-context directive must be visible in what was captured, not only in source")
	}
}

// A call that ran out of room while it was still thinking is the one whose
// reasoning says why, so its capture keeps what it had.
func TestACallCutOffWhileThinkingKeepsItsReasoning(t *testing.T) {
	api := serveSSE(t, anthropicSSE("max_tokens", 1000, 4096,
		anthropicThinking(0, "still weighing whether the retry path can return nil")))
	opts := oneShotOpts(api)
	captured := map[string][]byte{}
	opts.Capture = func(name string, data []byte) { captured[name] = data }
	if _, err := Run(context.Background(), exploreInput(), opts); err == nil {
		t.Fatal("the call must fail for this to be the failing path")
	}
	var got map[string]any
	if err := json.Unmarshal(captured["review.response.json"], &got); err != nil {
		t.Fatal(err)
	}
	if s, _ := got["thinking"].(string); !strings.Contains(s, "retry path can return nil") {
		t.Errorf("thinking = %q, want the reasoning the cut-off call had written", got["thinking"])
	}
}

// The reasoning is kept on every run, not only under --debug, labelled by the
// pass it came from, and a pass that thought across several turns keeps all of
// them. A pass that sent no thinking adds nothing.
func TestEveryRunKeepsItsThinking(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 1000, 60,
			anthropicThinking(0, "the guard was added after a panic in production"),
			anthropicToolUse(1, "toolu_1", CallOverview, `{"overview":"Removes a nil guard."}`),
			anthropicToolUse(2, "toolu_1b", CallFile, `{"path":"internal/queue/q.go","summary":"drops the guard"}`)),
		anthropicSSE("tool_use", 1000, 60,
			anthropicThinking(0, "nothing else to say, so done"),
			anthropicToolUse(1, "toolu_2", CallDone, `{}`)),
	)
	opts := oneShotOpts(api)
	opts.Capture = nil
	res, err := Run(context.Background(), exploreInput(), opts)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(res.Thinking) != 1 || res.Thinking[0].Pass != StageReview {
		t.Fatalf("thinking = %+v, want one entry for the review pass", res.Thinking)
	}
	text := res.Thinking[0].Text
	if !strings.Contains(text, "added after a panic") || !strings.Contains(text, "so done") {
		t.Errorf("both turns' thinking must be kept, got %q", text)
	}
}
