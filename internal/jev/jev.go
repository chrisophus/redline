// Package jev calls TypeSafe's System One endpoint, which serves the Jev
// models.
//
// Jev does not write text. A request carries a state and a set of yes/no
// questions about it, and the reply is one probability of yes per question.
// That makes it cheap enough to put in front of a decision a review would
// otherwise make by fixed rule, at $0.042 per million input tokens against
// dollars for a model that writes.
//
// The request and reply shapes are the ones docs.typesafe.ai/api gave on
// 2026-11-21.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is TypeSafe's endpoint and DefaultModel the alias its own
// examples use.
const (
	DefaultBaseURL = "https://api.typesafe.ai/v1"
	DefaultModel   = "jev-latest"
)

// PerMtok is the price per million input tokens for Jev 1.13 and 1.14 as
// docs.typesafe.ai/models listed it on 2026-11-21. Output tokens are free.
const PerMtok = 0.042

// KeyEnv is the environment variable the key is read from.
const KeyEnv = "TYPESAFE_API_KEY"

// attempts is how many times a request is sent when the endpoint says it is
// busy. The docs name 429 and 529 as the two statuses to retry.
const attempts = 4

// Client sends requests to one endpoint with one key.
type Client struct {
	// BaseURL is the endpoint without the /systemone path.
	BaseURL string
	Model   string
	Key     string
	HTTP    *http.Client
	// wait is the pause before a retry, replaced in tests.
	wait func(attempt int, retryAfter string) time.Duration
}

// New returns a client for the key, with TypeSafe's endpoint and model where
// the arguments are empty.
func New(baseURL, model, key string) (*Client, error) {
	if key == "" {
		return nil, errors.New(KeyEnv + " is not set")
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	if model == "" {
		model = DefaultModel
	}
	return &Client{BaseURL: base, Model: model, Key: key,
		HTTP: &http.Client{Timeout: 2 * time.Minute}}, nil
}

// Noul is one yes/no question. Instructions is the question, and True and
// False say what counts as each answer. The criteria are optional, and a
// question whose line between yes and no is subtle answers better with them.
//
// With is material only this question needs, sent beside the question under
// WithName. TypeSafe's guidance is to put an item there when the same
// question is asked about many items, and not in the state under an id the
// model has to look up: a question then sees its own item and nothing that
// belongs to another question.
type Noul struct {
	Instructions string
	WithName     string
	With         any
	True, False  string
}

// Usage is what one request was billed for.
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// Add returns the sum of two usages.
func (u Usage) Add(o Usage) Usage {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	return u
}

// CostUSD is what the usage cost at the listed price.
func (u Usage) CostUSD() float64 { return float64(u.InputTokens) * PerMtok / 1e6 }

// The wire types are structs and hand-built objects, not maps, because a map
// is written with its keys sorted. That put "false" before "true" and the
// attached item before the question it belongs to, and the same pairs of
// comments then scored up to 0.2 lower than they had with the question first.
type wireQuestion struct {
	Type string `json:"type"`
	// Instructions is the question as a string, or an object holding the
	// question and then what it comes with.
	Instructions json.RawMessage `json:"instructions"`
	Criteria     *wireCriteria   `json:"criteria,omitempty"`
}

type wireCriteria struct {
	True  string `json:"true"`
	False string `json:"false"`
}

// instructions writes the question, and what it comes with after it.
func (q Noul) instructions() (json.RawMessage, error) {
	ask, err := json.Marshal(q.Instructions)
	if err != nil || q.WithName == "" {
		return ask, err
	}
	name, err := json.Marshal(q.WithName)
	if err != nil {
		return nil, err
	}
	with, err := json.Marshal(q.With)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(`{"question":` + string(ask) + `,` + string(name) + `:` + string(with) + `}`), nil
}

// Ask sends every question over one state in one request and returns the
// probability of yes for each, under the name the question was given. The
// names are not sent to the model. A question the reply left out is missing
// from the result, and no answer at all is an error.
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Noul) (map[string]float64, Usage, error) {
	qs := make(map[string]wireQuestion, len(questions))
	for name, q := range questions {
		ins, err := q.instructions()
		if err != nil {
			return nil, Usage{}, fmt.Errorf("encoding question %s: %w", name, err)
		}
		w := wireQuestion{Type: "noul", Instructions: ins}
		if q.True != "" || q.False != "" {
			w.Criteria = &wireCriteria{True: q.True, False: q.False}
		}
		qs[name] = w
	}
	body, err := json.Marshal(map[string]any{"model": c.Model, "state": state, "questions": qs})
	if err != nil {
		return nil, Usage{}, fmt.Errorf("encoding the request: %w", err)
	}
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, body, &out); err != nil {
		return nil, Usage{}, fmt.Errorf("%s: %w", c.Model, err)
	}
	usage := Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}
	got := make(map[string]float64, len(out.Answers))
	for name, a := range out.Answers {
		if a.Noul != nil {
			got[name] = *a.Noul
		}
	}
	if len(got) == 0 {
		return nil, usage, errors.New("the reply carried no answer to any question")
	}
	return got, usage, nil
}

// post sends one request, again when the endpoint says it is busy, and
// decodes a 200 reply into out.
func (c *Client) post(ctx context.Context, body []byte, out any) error {
	var last error
	for attempt := range attempts {
		status, retryAfter, raw, err := c.send(ctx, body)
		if err != nil {
			return err
		}
		if status == http.StatusOK {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("the reply was not JSON: %w", err)
			}
			return nil
		}
		last = fmt.Errorf("%d: %s", status, clip(strings.TrimSpace(string(raw)), 300))
		if status != http.StatusTooManyRequests && status != 529 {
			return last
		}
		if attempt == attempts-1 {
			// Nothing is sent after this, so there is nothing to wait for.
			break
		}
		pause := c.pause(attempt, retryAfter)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
	return last
}

func (c *Client) send(ctx context.Context, body []byte) (status int, retryAfter string, raw []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/systemone", bytes.NewReader(body))
	if err != nil {
		return 0, "", nil, fmt.Errorf("building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, "", nil, fmt.Errorf("calling %s: %w", c.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, "", nil, fmt.Errorf("reading the reply: %w", err)
	}
	return resp.StatusCode, resp.Header.Get("Retry-After"), raw, nil
}

// pause is how long to wait before sending again: what the endpoint asked
// for when it said, and otherwise one, two, then four seconds.
func (c *Client) pause(attempt int, retryAfter string) time.Duration {
	if c.wait != nil {
		return c.wait(attempt, retryAfter)
	}
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s > 0 && s <= 60 {
		return time.Duration(s) * time.Second
	}
	return time.Second << attempt
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
