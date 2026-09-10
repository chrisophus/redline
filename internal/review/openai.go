package review

import (
	"bufio"
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
// JSON body, the response is one event stream, and the parts a review needs
// from each are small enough to read by hand. Nothing here decides what the
// review sees; that is settled by Assemble before the wire is chosen.

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
	MaxCompletionTokens int64           `json:"max_completion_tokens"`
	Stream              bool            `json:"stream"`
	StreamOptions       map[string]bool `json:"stream_options"`
	// ResponseFormat pins the reply to the review schema. strict is what
	// makes the schema a constraint rather than a hint, and the schema
	// already meets its rules: every property required, no extras.
	ResponseFormat  map[string]any `json:"response_format"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

// openAIChunk is one streamed event. Only the fields a review reads are
// named; a proxy may add more and they are ignored.
type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"delta"`
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

// completeOpenAI sends the assembled request and reads the stream back.
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

	body := openAIRequest{
		Model: opts.Model,
		Messages: []openAIMessage{
			{Role: "system", Content: res.System},
			{Role: "user", Content: res.Prompt},
		},
		MaxCompletionTokens: opts.MaxTokens,
		Stream:              true,
		StreamOptions:       map[string]bool{"include_usage": true},
		ResponseFormat: map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "review",
				"strict": true,
				"schema": res.Schema,
			},
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
	req.Header.Set("Accept", "text/event-stream")
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
	return readOpenAIStream(resp.Body)
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

// readOpenAIStream accumulates the event stream into one completion. Usage
// rides on the last chunk when the endpoint sends it at all, and a stream
// that reports none leaves it zero for Run to estimate.
func readOpenAIStream(r io.Reader) (completion, error) {
	var (
		c       completion
		text    strings.Builder
		refusal strings.Builder
		done    bool
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return c, fmt.Errorf("unreadable stream event: %w", err)
		}
		if chunk.Error != nil {
			return c, fmt.Errorf("stream error: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			cached := chunk.Usage.PromptTokensDetails.CachedTokens
			// The vendor counts cached tokens inside prompt_tokens; Usage
			// keeps them apart so they price at the cached rate.
			c.usage = Usage{
				InputTokens:     chunk.Usage.PromptTokens - cached,
				OutputTokens:    chunk.Usage.CompletionTokens,
				CacheReadTokens: cached,
			}
		}
		for _, ch := range chunk.Choices {
			text.WriteString(ch.Delta.Content)
			refusal.WriteString(ch.Delta.Refusal)
			if ch.FinishReason != "" {
				c.stopReason = ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return c, err
	}
	if !done && c.stopReason == "" {
		return c, errors.New("the stream ended before the response did")
	}
	c.text = stripFences(text.String())
	switch {
	case refusal.Len() > 0:
		c.refused = true
		c.detail = strings.TrimSpace(refusal.String())
	case c.stopReason == "content_filter":
		c.refused = true
		c.detail = "content filter"
	case c.stopReason == "length":
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
