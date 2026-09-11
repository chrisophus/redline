package scout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/review"
)

// openAIBaseURL is the vendor's own endpoint, used when no BaseURL is given. A
// proxy replaces it, and is how a gateway that speaks the chat-completions
// protocol is reached.
const openAIBaseURL = "https://api.openai.com/v1"

// scoutOpenAIClient is swapped by tests. The run's context already bounds it at
// runTimeout; the client timeout is a backstop for a connection that ignores
// cancellation.
var scoutOpenAIClient = &http.Client{Timeout: runTimeout}

// The OpenAI chat-completions protocol, function-calling half. Only the fields
// the scout uses are named; a proxy may add more and they are ignored.
type oaTool struct {
	Type     string `json:"type"`
	Function oaFunc `json:"function"`
}

type oaFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type oaToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function oaCallFn `json:"function"`
}

type oaCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaRequest struct {
	Model    string      `json:"model"`
	Messages []oaMessage `json:"messages"`
	Tools    []oaTool    `json:"tools,omitempty"`
	// MaxCompletionTokens is the current name for the output cap; the older
	// max_tokens is rejected by reasoning models.
	MaxCompletionTokens int64  `json:"max_completion_tokens"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
}

type oaResponse struct {
	Choices []struct {
		Message struct {
			Content   string       `json:"content"`
			ToolCalls []oaToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// openAITools renders the toolset as OpenAI function definitions from the same
// tool specs the Anthropic path builds its own params from, so a model on
// either wire is offered the same tools with the same schemas.
func (ts *toolset) openAITools() []oaTool {
	offered := ts.offered()
	out := make([]oaTool, 0, len(offered))
	for _, t := range offered {
		params := map[string]any{"type": "object"}
		if t.schema.Properties != nil {
			params["properties"] = t.schema.Properties
		}
		if len(t.schema.Required) > 0 {
			params["required"] = t.schema.Required
		}
		out = append(out, oaTool{
			Type:     "function",
			Function: oaFunc{Name: t.name, Description: t.description, Parameters: params},
		})
	}
	return out
}

// driveOpenAI runs the scout loop over the OpenAI chat-completions protocol
// with function calling. It is the Anthropic loop with a different transport:
// the same tools, the same governor, the same rule that the model records a
// location and this program reads the bytes. A tool call comes back as one
// entry in the assistant turn's tool_calls, is dispatched exactly as the
// Anthropic block is, and its result goes back as a tool-role message keyed by
// the call id.
func driveOpenAI(ctx context.Context, opts Options, ts *toolset) (Spend, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = openAIBaseURL
	}
	if opts.APIKey == "" && base == openAIBaseURL {
		// A proxy may need no key; the vendor's own endpoint always does, and
		// a 401 after the first turn is a worse way to learn it.
		return Spend{}, fmt.Errorf("OPENAI_API_KEY is not set")
	}

	tools := ts.openAITools()
	messages := []oaMessage{
		{Role: "system", Content: promptFor(opts, ts.Names())},
		{Role: "user", Content: briefFor(opts)},
	}

	var spend Spend
	for turn := range opts.MaxTurns {
		// The last turn of the budget files rather than searches. A loop that
		// simply stops at the limit throws away everything the turns before it
		// paid to read, because nothing reaches the review except through
		// record. Not on the first turn: a search that has read nothing has
		// nothing to file.
		if turn > 0 && turn == opts.MaxTurns-1 && !ts.done {
			ts.closing = true
			tools = ts.openAITools()
			// The system prompt names the tools, so it is rendered again from
			// what is left. A prompt that still lists grep while the request
			// carries record alone asks for a call that cannot be made.
			messages[0].Content = promptFor(opts, ts.Names())
			messages = append(messages, oaMessage{Role: "user", Content: closingBrief})
			ts.notes = append(ts.notes, fmt.Sprintf(
				"the search for context stopped at its %d-turn limit; there may be context it had not reached", opts.MaxTurns))
		}
		if stop, reason := overBudgetOpenAI(opts, spend, messages, tools); stop {
			spend.CapHit = true
			ts.notes = append(ts.notes, reason)
			break
		}
		resp, err := postOpenAI(ctx, opts, base, oaRequest{
			Model:               opts.Model,
			Messages:            messages,
			Tools:               tools,
			MaxCompletionTokens: opts.MaxTokens,
			ReasoningEffort:     opts.Effort,
		})
		if err != nil {
			if turn == 0 {
				return spend, fmt.Errorf("%s: %w", opts.Model, err)
			}
			ts.notes = append(ts.notes, fmt.Sprintf(
				"the search for context stopped early after %d turn(s): %v; what is below is what it had found by then", turn, err))
			break
		}
		spend.Turns++
		if resp.Usage != nil {
			// The vendor counts cached tokens inside prompt_tokens; Usage keeps
			// them apart so they price at the cached rate.
			cached := resp.Usage.PromptTokensDetails.CachedTokens
			spend.Usage.InputTokens += resp.Usage.PromptTokens - cached
			spend.Usage.OutputTokens += resp.Usage.CompletionTokens
			spend.Usage.CacheReadTokens += cached
		}
		spend.CostUSD, spend.CostKnown = spend.Usage.Cost(opts.Model)
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf("scout turn %d: %d record(s), %s so far",
				spend.Turns, len(ts.records), review.FormatCost(spend.CostUSD, spend.CostKnown)))
		}

		if len(resp.Choices) == 0 {
			ts.notes = append(ts.notes, "the search for context came back with no content")
			break
		}
		choice := resp.Choices[0]
		// The assistant turn goes back verbatim, tool calls included, or the
		// tool results that follow have nothing to attach to.
		messages = append(messages, oaMessage{
			Role: "assistant", Content: choice.Message.Content, ToolCalls: choice.Message.ToolCalls,
		})
		if len(choice.Message.ToolCalls) == 0 {
			if !ts.done {
				ts.notes = append(ts.notes, "the search for context ended without a summary of what it could not find")
			}
			break
		}
		// Every call in the turn gets a result, even after done, or the next
		// request is malformed. dispatch is the same one the Anthropic path
		// runs; the OpenAI protocol carries no failure flag, so an error rides
		// back as the content the model reads and corrects from.
		for _, tc := range choice.Message.ToolCalls {
			result, _ := ts.dispatch(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			messages = append(messages, oaMessage{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
		if ts.done {
			break
		}
	}
	spend.CostUSD, spend.CostKnown = spend.Usage.Cost(opts.Model)
	if opts.Capture != nil {
		b, _ := json.MarshalIndent(messages, "", "  ")
		opts.Capture("scout.transcript.json", b)
	}
	return spend, nil
}

// postOpenAI sends one turn and reads the whole response. Unlike the review's
// own OpenAI call it does not stream: a tool loop reads the tool calls off a
// finished turn, and streaming them back would buy nothing but reassembly.
func postOpenAI(ctx context.Context, opts Options, base string, body oaRequest) (*oaResponse, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := oaBearer(opts); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := scoutOpenAIClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e oaResponse
		if json.Unmarshal(raw, &e) == nil && e.Error != nil && e.Error.Message != "" {
			return nil, fmt.Errorf("%s: %s", resp.Status, e.Error.Message)
		}
		if msg := strings.TrimSpace(string(raw)); msg != "" {
			return nil, fmt.Errorf("%s: %s", resp.Status, msg)
		}
		return nil, fmt.Errorf("%s", resp.Status)
	}
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("%s", out.Error.Message)
	}
	return &out, nil
}

// oaBearer is what follows "Bearer" in the Authorization header, the same shape
// the reviewer's own OpenAI wire uses: a gateway that meters by caller wants
// the user beside the key, as user=<user>&key=<key>; with no user, the key
// goes as it is.
func oaBearer(opts Options) string {
	if opts.APIUser == "" {
		return opts.APIKey
	}
	return "user=" + opts.APIUser + "&key=" + opts.APIKey
}

// overBudgetOpenAI is the cost governor for the OpenAI loop, the same check the
// Anthropic path makes: price the turn about to be sent and stop before it
// takes the run past its allowance.
func overBudgetOpenAI(opts Options, spend Spend, messages []oaMessage, tools []oaTool) (bool, string) {
	next := estimateInputOpenAI(messages, tools)
	ceiling, ok := review.CeilingCost(opts.Model, next, opts.MaxTokens)
	if !ok {
		// An unpriced model cannot be governed by cost. Turns still bound it.
		return false, ""
	}
	if spend.CostUSD+ceiling <= opts.MaxCostUSD {
		return false, ""
	}
	return true, fmt.Sprintf(
		"the search for context stopped at its cost cap of %s after %d turn(s); there may be context it had not reached",
		review.FormatCost(opts.MaxCostUSD, true), spend.Turns)
}

// estimateInputOpenAI sizes the next request from the conversation so far, the
// same crude character count the Anthropic path budgets with and for the same
// reason: an exact count would cost a call to get.
func estimateInputOpenAI(messages []oaMessage, tools []oaTool) int {
	n := 0
	for _, m := range messages {
		n += envelope.EstimateTokens(m.Content)
		for _, tc := range m.ToolCalls {
			n += envelope.EstimateTokens(tc.Function.Arguments)
		}
	}
	for _, t := range tools {
		n += envelope.EstimateTokens(t.Function.Name + " " + t.Function.Description)
	}
	return n
}
