package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The OpenAI backend speaks the chat completions protocol over plain HTTP.
//
// It exists for the proxy case. A team that routes every model call through
// one gateway, for billing, audit, or because the vendor's API is not
// reachable from where the review runs, exposes that gateway as an
// OpenAI-compatible endpoint whatever sits behind it. So this is written to
// the protocol rather than to the vendor, with no SDK: the request is one
// JSON body, the response is one JSON body, and the parts a review needs from
// each are small enough to read by hand. Nothing here decides what the review
// sees; that is settled by Assemble before the wire is chosen.

// DefaultOpenAIModel is the model used when --api openai names none.
// A proxy may alias this to anything; the ledger records what was asked for.
const DefaultOpenAIModel = "gpt-5"

// defaultOpenAIBaseURL is the vendor's own endpoint, used when no BaseURL
// is given. A proxy replaces it.
const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// openAIHTTPClient is swapped by tests. The default bounds a request at ten
// minutes. A review runs two to five, so this leaves margin, while a proxy
// that accepts the connection and then stalls fails the command instead of
// hanging it forever, which http.DefaultClient with no timeout allowed.
var openAIHTTPClient = &http.Client{Timeout: 10 * time.Minute}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is what an assistant turn called, resent as it came back, and
	// ToolCallID says which call a tool message answers.
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	// MaxCompletionTokens is the current name for the output cap; the older
	// max_tokens is rejected by reasoning models.
	MaxCompletionTokens int64 `json:"max_completion_tokens"`
	// Tools carries every call as a function, and ToolChoice requires the
	// model to call one. This is asked for instead of response_format because a
	// gateway that serves one vendor's model over another's protocol does not
	// enforce response_format: on the internal gateway this repository is run
	// through, one review in four came back with the schema's array as a JSON string or
	// an object, and the checking pass failed open on the parse. Function
	// calling is honoured where the schema is not, because the same gateway
	// drives the scout's tool loop reliably.
	Tools           []openAITool `json:"tools,omitempty"`
	ToolChoice      any          `json:"tool_choice,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
}

// openAIResponse is the non-streamed reply. Only the fields a review reads are
// named; a proxy may add more and they are ignored. The structured body is the
// forced tool call's arguments; content is read only when a proxy ignored the
// tool call and answered in prose instead.
type openAIResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			Refusal   string           `json:"refusal"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens            int64 `json:"prompt_tokens"`
		CompletionTokens        int64 `json:"completion_tokens"`
		CompletionTokensDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *openAIError `json:"error"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// completeOpenAI runs one pass over chat completions. Each turn is one
// non-streamed request: a tool call is read off a finished turn, and streaming
// its arguments back would buy nothing but reassembly.
func completeOpenAI(ctx context.Context, opts Options, res *Result) (completion, error) {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = defaultOpenAIBaseURL
	}
	if opts.APIKey == "" && base == defaultOpenAIBaseURL {
		// A proxy may need no key. The vendor's own endpoint always does,
		// and a 401 after the whole prompt has been uploaded is a worse way
		// to find out.
		return completion{}, errors.New("OPENAI_API_KEY is not set")
	}
	// Never required: forcing tool_choice is incompatible with thinking on
	// the Anthropic API a proxy may be forwarding this to, and the calls
	// block already says which calls answer the pass either way.
	conv := &openAIConversation{
		opts:  opts,
		url:   base + "/chat/completions",
		stage: res.stage(),
		began: time.Now(),
		req: openAIRequest{
			Model: opts.Model,
			Messages: []openAIMessage{
				{Role: "system", Content: res.System},
				// One string, because this protocol's user turn is one string
				// and there is no breakpoint here to keep the parts apart for.
				{Role: "user", Content: res.Prompt + callsBlock(res.stage(), res.recaps(), res.pulls(), res.looks()) + res.Tail},
			},
			Tools:           openAITools(res.describes(), res.recaps(), res.pulls(), res.looks()),
			ToolChoice:      "auto",
			ReasoningEffort: opts.Effort,
		},
	}
	return converse(ctx, opts, res, conv)
}

// openAIConversation is one pass's conversation over chat completions.
type openAIConversation struct {
	opts  Options
	url   string
	stage string
	req   openAIRequest
	last  openAIMessage
	// began is when the pass started, for the heartbeat.
	began time.Time
}

func (o *openAIConversation) send(ctx context.Context, maxTokens int64, turn int) (turnReply, error) {
	o.req.MaxCompletionTokens = maxTokens
	buf, err := json.Marshal(o.req)
	if err != nil {
		return turnReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(buf))
	if err != nil {
		return turnReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token := bearerToken(o.opts); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Nothing comes back until the whole reply is ready, which on a
	// reasoning model is minutes. The streaming wire narrates its deltas;
	// this one can only say that it is still waiting, and does.
	stop := waitHeartbeat(o.opts, fmt.Sprintf("%s turn %d", o.stage, turn), o.began)
	defer stop()
	resp, err := openAIHTTPClient.Do(req)
	if err != nil {
		return turnReply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return turnReply{}, openAIStatusError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return turnReply{}, err
	}
	r, msg, err := readOpenAIResponse(raw)
	o.last = msg
	return r, err
}

func (o *openAIConversation) answer(_ turnReply, results []callResult) {
	o.req.Messages = append(o.req.Messages, o.last)
	for _, r := range results {
		content := r.content
		if r.isError {
			// This protocol has no error flag on a tool message, so the
			// rejection says so in its text, which already starts "Not".
			content = "Error. " + content
		}
		o.req.Messages = append(o.req.Messages, openAIMessage{Role: "tool", ToolCallID: r.id, Content: content})
	}
}

func (o *openAIConversation) nudge(_ turnReply, text string) {
	o.req.Messages = append(o.req.Messages, o.last, openAIMessage{Role: "user", Content: text})
}

// bearerToken is what follows "Bearer" in the Authorization header. A
// gateway that meters by caller wants the user in the token beside the key,
// as user=<user>&key=<key>; with no user named, the key goes as it is.
func bearerToken(opts Options) string {
	if opts.APIUser == "" {
		return opts.APIKey
	}
	return "user=" + opts.APIUser + "&key=" + opts.APIKey
}

// openAIStatusError turns a non-200 into an error that quotes the server's
// own message when it sent one, since that is where a proxy says what it
// did not like about the request.
func openAIStatusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Error *openAIError `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != nil && e.Error.Message != "" {
		return fmt.Errorf("%s: %s", resp.Status, e.Error.Message)
	}
	if msg := strings.TrimSpace(string(raw)); msg != "" {
		return fmt.Errorf("%s: %s", resp.Status, msg)
	}
	return errors.New(resp.Status)
}

// readOpenAIResponse turns one reply into a turn, and returns the assistant
// message as it goes back on the next request.
func readOpenAIResponse(raw []byte) (turnReply, openAIMessage, error) {
	var r openAIResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return turnReply{}, openAIMessage{}, fmt.Errorf("unreadable response: %w", err)
	}
	if r.Error != nil && strings.TrimSpace(r.Error.Message) != "" {
		return turnReply{}, openAIMessage{}, fmt.Errorf("response error: %s", r.Error.Message)
	}
	t := turnReply{model: r.Model}
	if r.Usage != nil {
		// The vendor counts cached tokens inside prompt_tokens; Usage keeps
		// them apart so they price at the cached rate.
		cached := r.Usage.PromptTokensDetails.CachedTokens
		t.usage = Usage{
			InputTokens:     r.Usage.PromptTokens - cached,
			OutputTokens:    r.Usage.CompletionTokens,
			ThinkingTokens:  r.Usage.CompletionTokensDetails.ReasoningTokens,
			CacheReadTokens: cached,
		}
	}
	if len(r.Choices) == 0 {
		return t, openAIMessage{}, errors.New("the response carried no choices")
	}
	ch := r.Choices[0]
	t.stopReason = ch.FinishReason
	msg := openAIMessage{Role: "assistant", Content: ch.Message.Content, ToolCalls: ch.Message.ToolCalls}
	for i, tc := range ch.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			// A gateway that drops call ids still has to be answered one
			// call at a time, so each gets a stable one here.
			id = fmt.Sprintf("call_%d", i)
			msg.ToolCalls[i].ID = id
		}
		if msg.ToolCalls[i].Type == "" {
			msg.ToolCalls[i].Type = "function"
		}
		t.calls = append(t.calls, toolCall{ID: id, Name: tc.Function.Name, Input: json.RawMessage(tc.Function.Arguments)})
	}
	t.text = stripFences(ch.Message.Content)
	switch {
	case strings.TrimSpace(ch.Message.Refusal) != "":
		t.refused = true
		t.detail = strings.TrimSpace(ch.Message.Refusal)
	case ch.FinishReason == "content_filter":
		t.refused = true
		t.detail = "content filter"
	case ch.FinishReason == "length":
		t.truncated = true
	}
	return t, msg, nil
}

// stripFences removes a Markdown code fence around a JSON body. A model held
// to a schema does not add one; a proxy that quietly drops the schema
// constraint sometimes lets one through, and the review inside is still
// worth reading.
func stripFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	t = strings.TrimPrefix(t, "```")
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	}
	t = strings.TrimSuffix(strings.TrimSpace(t), "```")
	return strings.TrimSpace(t)
}
