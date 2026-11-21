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

// NewAsker returns the asker for an endpoint. API is anthropic or openai,
// where openai is any endpoint that speaks chat completions.
func NewAsker(e Endpoint) (Asker, error) {
	switch e.API {
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
	return nil, fmt.Errorf("--api %q: use anthropic or openai", e.API)
}

type anthropicAsker struct {
	client anthropic.Client
	model  string
}

func (a *anthropicAsker) Ask(ctx context.Context, system, prompt string, choices []string) (string, review.Usage, error) {
	msg, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: askMaxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))},
	})
	if err != nil {
		return "", review.Usage{}, err
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
	said, err := choose(text.String(), choices)
	return said, usage, err
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

func (o *openAIAsker) Ask(ctx context.Context, system, prompt string, choices []string) (string, review.Usage, error) {
	body, err := json.Marshal(map[string]any{
		"model": o.model,
		"messages": []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", review.Usage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(body))
	if err != nil {
		return "", review.Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.token != "" {
		req.Header.Set("Authorization", "Bearer "+o.token)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return "", review.Usage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", review.Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", review.Usage{}, fmt.Errorf("%s: %s", resp.Status, truncate(strings.TrimSpace(string(raw)), 300))
	}
	var out struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", review.Usage{}, fmt.Errorf("reply was not JSON: %w", err)
	}
	usage := review.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	if len(out.Choices) == 0 {
		return "", usage, errors.New("reply carried no choices")
	}
	said, err := choose(out.Choices[0].Message.Content, choices)
	return said, usage, err
}
