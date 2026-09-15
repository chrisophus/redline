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

func sentThinkingWire(t *testing.T, thinking bool) thinkingWire {
	t.Helper()
	api := serveSSE(t, anthropicSSE("tool_use", 10, 5, anthropicText(0, "{}")))
	if _, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100, Thinking: thinking,
	}, &Result{System: "s", Prompt: "p", Stage: StageReview}); err != nil {
		t.Fatal(err)
	}
	var got thinkingWire
	if err := json.Unmarshal(api.seen()[0], &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// A call pinned to one tool does not think on Sonnet 5, so a call asked to
// think offers the tool, says in words which one to call, and asks for
// adaptive thinking with its summary on the stream.
func TestThinkingOffersTheToolAndAsksForAdaptiveThinking(t *testing.T) {
	got := sentThinkingWire(t, true)
	if got.ToolChoice.Type != "auto" {
		t.Errorf("tool_choice %q, want auto: a pinned call does not think", got.ToolChoice.Type)
	}
	if got.Thinking["type"] != "adaptive" || got.Thinking["display"] != "summarized" {
		t.Errorf("thinking %v, want adaptive with a summarized display", got.Thinking)
	}
	var said bool
	for _, m := range got.Messages {
		for _, c := range m.Content {
			said = said || strings.Contains(c.Text, "calling the review tool")
		}
	}
	if !said {
		t.Error("an unpinned call must be told in words which tool answers it")
	}
}

func TestWithoutThinkingTheStageIsPinned(t *testing.T) {
	got := sentThinkingWire(t, false)
	if got.ToolChoice.Type != "tool" || got.ToolChoice.Name != StageReview {
		t.Errorf("tool_choice %+v, want the review tool pinned", got.ToolChoice)
	}
	if got.Thinking != nil {
		t.Errorf("thinking %v sent on a call that did not ask for it", got.Thinking)
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

func TestThinkingOnTheOpenAIWireOffersTheFunction(t *testing.T) {
	var choice any
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		choice = req.ToolChoice
		_, _ = w.Write([]byte(toolReply(t, `{"overview":"x","files":[],"comments":[],"verdicts":[]}`, "tool_calls",
			`{"prompt_tokens":10,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":12}}`)))
	})
	c, err := completeOpenAI(context.Background(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100, Thinking: true,
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
