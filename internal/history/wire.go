package history

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

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/chrisophus/redline/internal/review"
)

// askMaxTokens bounds one answer. The answer is a word, and the room above
// that is for a model that thinks before it writes one.
const askMaxTokens = 1024

// Endpoint says which model answers the questions and how to reach it. The
// fields mean what they mean on `redline review`.
type Endpoint struct {
	API     string
	Model   string
	BaseURL string
	APIKey  string
	APIUser string
}

// APIJev is TypeSafe's System One endpoint, which serves the Jev models. It
// is not one of review's wires: Jev answers typed questions and cannot write
// a review.
const APIJev = "jev"

// DefaultJevModel is the alias TypeSafe's own examples use.
const DefaultJevModel = "jev-latest"

// jevBaseURL is TypeSafe's endpoint, and jevPerMtok its price per million
// input tokens as docs.typesafe.ai/models listed it for Jev 1.13 and 1.14 on
// 2026-11-21. Output tokens are free.
const (
	jevBaseURL = "https://api.typesafe.ai/v1"
	jevPerMtok = 0.042
)

// askCost prices what the questions used. Jev is not in review's price
// table, since nothing reviews with it.
func askCost(model string, u review.Usage) (float64, bool) {
	if strings.HasPrefix(strings.ToLower(model), "jev-") {
		return float64(u.InputTokens) * jevPerMtok / 1e6, true
	}
	return u.Cost(model)
}

// NewAsker returns the asker for an endpoint. API is anthropic, openai (any
// endpoint that speaks chat completions) or jev.
func NewAsker(e Endpoint) (Asker, error) {
	switch e.API {
	case APIJev:
		if e.APIKey == "" {
			return nil, errors.New("TYPESAFE_API_KEY is not set")
		}
		base := strings.TrimRight(e.BaseURL, "/")
		if base == "" {
			base = jevBaseURL
		}
		return &jevAsker{url: base + "/systemone", model: e.Model, token: e.APIKey,
			client: &http.Client{Timeout: 2 * time.Minute}}, nil
	case "", review.APIAnthropic:
		var opts []option.RequestOption
		if e.APIKey != "" {
			opts = append(opts, option.WithAPIKey(e.APIKey))
		}
		if e.BaseURL != "" {
			opts = append(opts, option.WithBaseURL(e.BaseURL))
		}
		return &anthropicAsker{client: anthropic.NewClient(opts...), model: e.Model}, nil
	case review.APIOpenAI:
		base := strings.TrimRight(e.BaseURL, "/")
		if base == "" {
			base = "https://api.openai.com/v1"
			if e.APIKey == "" {
				return nil, errors.New("OPENAI_API_KEY is not set")
			}
		}
		token := e.APIKey
		if e.APIUser != "" {
			token = "user=" + e.APIUser + "&key=" + e.APIKey
		}
		return &openAIAsker{url: base + "/chat/completions", model: e.Model, token: token,
			client: &http.Client{Timeout: 2 * time.Minute}}, nil
	}
	return nil, fmt.Errorf("--api %q: use anthropic, openai or jev", e.API)
}

type anthropicAsker struct {
	client anthropic.Client
	model  string
}

func (a *anthropicAsker) Ask(ctx context.Context, q Question) (Answer, error) {
	msg, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: askMaxTokens,
		System:    []anthropic.TextBlockParam{{Text: q.System}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(q.prompt()))},
	})
	if err != nil {
		return Answer{}, err
	}
	usage := review.Usage{
		InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens,
		CacheReadTokens: msg.Usage.CacheReadInputTokens, CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	said, err := choose(text.String(), []string{q.Yes, q.No})
	return Answer{Said: said, Usage: usage}, err
}

type openAIAsker struct {
	client *http.Client
	url    string
	model  string
	token  string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (o *openAIAsker) Ask(ctx context.Context, q Question) (Answer, error) {
	var out struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	err := postJSON(ctx, o.client, o.url, o.token, map[string]any{
		"model": o.model,
		"messages": []chatMessage{
			{Role: "system", Content: q.System},
			{Role: "user", Content: q.prompt()},
		},
	}, &out)
	if err != nil {
		return Answer{}, err
	}
	ans := Answer{Usage: review.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}}
	if len(out.Choices) == 0 {
		return ans, errors.New("reply carried no choices")
	}
	ans.Said, err = choose(out.Choices[0].Message.Content, []string{q.Yes, q.No})
	return ans, err
}

// jevAsker asks TypeSafe's System One endpoint. The question goes as one
// noul, which is its yes/no type, and the answer is the probability of yes.
// The request and response shapes are the ones docs.typesafe.ai/primitives/noul
// gives.
type jevAsker struct {
	client *http.Client
	url    string
	model  string
	token  string
}

// jevQuestionID names the one question in a request. The id is not sent to
// the model; the answer comes back under it.
const jevQuestionID = "answer"

func (j *jevAsker) Ask(ctx context.Context, q Question) (Answer, error) {
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	err := postJSON(ctx, j.client, j.url, j.token, map[string]any{
		"model": j.model,
		"state": q.State,
		"questions": map[string]any{
			jevQuestionID: map[string]string{"type": "noul", "instructions": q.Ask},
		},
	}, &out)
	if err != nil {
		return Answer{}, err
	}
	ans := Answer{Usage: review.Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}}
	p := out.Answers[jevQuestionID].Noul
	if p == nil {
		return ans, errors.New("reply carried no answer to the question")
	}
	ans.P, ans.HasP, ans.Said = *p, true, q.No
	if *p >= 0.5 {
		ans.Said = q.Yes
	}
	return ans, nil
}

// postJSON sends body as JSON with a bearer token and decodes a 200 reply
// into out. Any other status is an error that quotes what the server said.
func postJSON(ctx context.Context, client *http.Client, url, token string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, truncate(strings.TrimSpace(string(raw)), 300))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("reply was not JSON: %w", err)
	}
	return nil
}
