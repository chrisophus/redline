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
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	// MaxCompletionTokens is the current name for the output cap; the older
	// max_tokens is rejected by reasoning models.
	MaxCompletionTokens int64 `json:"max_completion_tokens"`
	// Tools carries the schema as a single function, and ToolChoice forces the
	// model to call it. This is asked for instead of response_format because a
	// gateway that serves one vendor's model over another's protocol does not
	// enforce response_format: on this repository's own Marketplace gateway
	// one review in four came back with the schema's array as a JSON string or
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
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			Refusal   string `json:"refusal"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
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
	Error *openAIError `json:"error"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// completeOpenAI sends the assembled request as one forced tool call and reads
// the reply. Non-streaming for the same reason the scout is: a tool call is
// read off a finished turn, and streaming its arguments back would buy nothing
// but reassembly.
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

	// The function is named for what it returns, so a debug log reads which
	// stage this is; the schema it carries is the same one Assemble priced.
	fn := "review"
	if res.rulesRatherThanReviews() {
		fn = "rulings"
	}
	body := openAIRequest{
		Model: opts.Model,
		Messages: []openAIMessage{
			{Role: "system", Content: res.System},
			{Role: "user", Content: res.Prompt},
		},
		MaxCompletionTokens: opts.MaxTokens,
		Tools: []openAITool{{
			Type: "function",
			Function: openAIFunction{
				Name:        fn,
				Description: "Return the structured object this schema defines. Call this and nothing else.",
				Parameters:  res.Schema,
			},
		}},
		ToolChoice: map[string]any{
			"type":     "function",
			"function": map[string]any{"name": fn},
		},
		ReasoningEffort: opts.Effort,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return completion{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return completion{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token := bearerToken(opts); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := openAIHTTPClient.Do(req)
	if err != nil {
		return completion{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return completion{}, openAIStatusError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return completion{}, err
	}
	return readOpenAIResponse(raw)
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

// readOpenAIResponse turns the reply into a completion. The structured body is
// the forced tool call's arguments; a gateway that ignored tool_choice and
// answered in content is still read, fenced or not, so a dropped constraint
// degrades to prose in the body rather than an empty review.
func readOpenAIResponse(raw []byte) (completion, error) {
	var r openAIResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return completion{}, fmt.Errorf("unreadable response: %w", err)
	}
	if r.Error != nil && strings.TrimSpace(r.Error.Message) != "" {
		return completion{}, fmt.Errorf("response error: %s", r.Error.Message)
	}
	var c completion
	if r.Usage != nil {
		// The vendor counts cached tokens inside prompt_tokens; Usage keeps
		// them apart so they price at the cached rate.
		cached := r.Usage.PromptTokensDetails.CachedTokens
		c.usage = Usage{
			InputTokens:     r.Usage.PromptTokens - cached,
			OutputTokens:    r.Usage.CompletionTokens,
			CacheReadTokens: cached,
		}
	}
	if len(r.Choices) == 0 {
		return c, errors.New("the response carried no choices")
	}
	ch := r.Choices[0]
	c.stopReason = ch.FinishReason
	for _, tc := range ch.Message.ToolCalls {
		if strings.TrimSpace(tc.Function.Arguments) != "" {
			c.text = tc.Function.Arguments
			break
		}
	}
	if c.text == "" {
		c.text = stripFences(ch.Message.Content)
	}
	switch {
	case strings.TrimSpace(ch.Message.Refusal) != "":
		c.refused = true
		c.detail = strings.TrimSpace(ch.Message.Refusal)
	case ch.FinishReason == "content_filter":
		c.refused = true
		c.detail = "content filter"
	case ch.FinishReason == "length":
		c.truncated = true
	}
	return c, nil
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
