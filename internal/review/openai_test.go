package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
)

// sse renders a chat completions stream: the content in a few chunks, then
// the finish reason, then usage when given, then the terminator.
func sse(content, finish string, usage string) string {
	var b strings.Builder
	half := len(content) / 2
	for _, part := range []string{content[:half], content[half:]} {
		raw, _ := json.Marshal(part)
		fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n", raw)
	}
	fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\n", finish)
	if usage != "" {
		fmt.Fprintf(&b, "data: {\"choices\":[],\"usage\":%s}\n\n", usage)
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

const reviewBody = `{"overview":"Adds a thing.","files":[{"path":"a.go","summary":"adds x"}],` +
	`"comments":[{"file":"a.go","line":1,"severity":"warning","confidence":"high",` +
	`"category":"review","relatedFindings":[],"body":"x is unused"}]}`

// openAIServer answers like a chat completions endpoint and keeps the last
// request it saw. Handler writes the response for a request.
func openAIServer(t *testing.T, handle func(w http.ResponseWriter, req openAIRequest)) (*httptest.Server, *atomic.Int32, *http.Header, *string) {
	t.Helper()
	var hits atomic.Int32
	var hdr http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		hdr = r.Header.Clone()
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		var req openAIRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("request body is not the request struct: %v", err)
		}
		handle(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &hdr, &path
}

func smallInput() Input {
	return Input{Report: &findings.Report{}, Change: &change.Set{Files: []change.File{
		{Path: "a.go", Status: "modified", Added: 1, Diff: "+x := 1"},
	}}}
}

func TestOpenAISendsTheSameReviewOverTheOtherWire(t *testing.T) {
	var got openAIRequest
	srv, hits, hdr, path := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		got = req
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(reviewBody, "stop",
			`{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":300}}`))
	})
	res, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL + "/v1/", APIKey: "sk-test", Effort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("one shot is one call, got %d", hits.Load())
	}
	if *path != "/v1/chat/completions" {
		t.Fatalf("a trailing slash on the base must not double up: %s", *path)
	}
	if hdr.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("the key must ride as a bearer token, got %q", hdr.Get("Authorization"))
	}
	if got.Model != DefaultOpenAIModel {
		t.Fatalf("model = %q; openai must not default to a Claude model", got.Model)
	}
	if !got.Stream || !got.StreamOptions["include_usage"] {
		t.Fatal("the stream must ask for usage, or the ledger has nothing to record")
	}
	if got.MaxCompletionTokens != DefaultMaxTokens {
		t.Fatalf("max_completion_tokens = %d", got.MaxCompletionTokens)
	}
	if got.ReasoningEffort != "low" {
		t.Fatalf("effort must pass through, got %q", got.ReasoningEffort)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[0].Content != res.System ||
		got.Messages[1].Role != "user" || got.Messages[1].Content != res.Prompt {
		t.Fatal("the system block and the prompt must be sent exactly as assembled and priced")
	}
	js, _ := got.ResponseFormat["json_schema"].(map[string]any)
	if got.ResponseFormat["type"] != "json_schema" || js == nil || js["strict"] != true {
		t.Fatalf("the reply must be pinned to the schema, got %v", got.ResponseFormat)
	}
	if _, ok := js["schema"].(map[string]any); !ok {
		t.Fatal("the schema itself must be sent")
	}

	if res.API != APIOpenAI || res.Turns != 1 || res.StopReason != "stop" {
		t.Fatalf("result: api=%q turns=%d stop=%q", res.API, res.Turns, res.StopReason)
	}
	if res.Usage.InputTokens != 700 || res.Usage.CacheReadTokens != 300 || res.Usage.OutputTokens != 50 {
		t.Fatalf("cached tokens are counted inside prompt_tokens and must be split out: %+v", res.Usage)
	}
	if res.UsageEstimated {
		t.Fatal("usage the endpoint counted must not be marked as estimated")
	}
	if !res.CostKnown {
		t.Fatal("the default openai model must have a rate")
	}
	if len(res.Review.Comments) != 1 || res.Review.Comments[0].Body != "x is unused" {
		t.Fatalf("the review must come back through the same parser: %+v", res.Review)
	}
}

func TestOpenAITruncationIsReportedNotParsed(t *testing.T) {
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		_, _ = io.WriteString(w, sse(`{"overview":"cut off`, "length",
			`{"prompt_tokens":10,"completion_tokens":32000}`))
	})
	res, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k",
	})
	if err == nil || !strings.Contains(err.Error(), "--max-tokens") {
		t.Fatalf("a truncated response must name the cap, got %v", err)
	}
	if res.Usage.OutputTokens != 32000 {
		t.Fatal("a truncated call was still paid for and must record its usage")
	}
}

func TestOpenAIRefusalIsNotAnEmptyReview(t *testing.T) {
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"refusal\":\"I cannot help with that.\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	})
	_, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k",
	})
	if err == nil || !strings.Contains(err.Error(), "declined") || !strings.Contains(err.Error(), "I cannot help") {
		t.Fatalf("a refusal must be reported as one, with the reason, got %v", err)
	}
}

func TestOpenAIServerErrorQuotesTheServer(t *testing.T) {
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`)
	})
	_, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "bad",
	})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("the proxy's own message is the diagnosis and must be quoted, got %v", err)
	}
}

func TestOpenAIEstimatesUsageWhenTheProxyReportsNone(t *testing.T) {
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		_, _ = io.WriteString(w, sse(reviewBody, "stop", ""))
	})
	res, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.UsageEstimated {
		t.Fatal("a call the endpoint did not count must be marked as estimated")
	}
	if res.Usage.InputTokens != int64(res.InputEstimate) || res.Usage.InputTokens == 0 {
		t.Fatalf("a paid call must still carry usage for the ledger, got %+v", res.Usage)
	}
	if res.Usage.OutputTokens == 0 {
		t.Fatal("the output was received and can be estimated")
	}
}

func TestOpenAIToleratesAFencedReply(t *testing.T) {
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		_, _ = io.WriteString(w, sse("```json\n"+reviewBody+"\n```", "stop",
			`{"prompt_tokens":10,"completion_tokens":5}`))
	})
	res, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k",
	})
	if err != nil {
		t.Fatalf("a proxy that drops the schema constraint may fence the JSON; the review inside still parses: %v", err)
	}
	if res.Review.Overview != "Adds a thing." {
		t.Fatalf("overview = %q", res.Review.Overview)
	}
}

func TestOpenAIProxyMayNeedNoKey(t *testing.T) {
	srv, _, hdr, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		_, _ = io.WriteString(w, sse(reviewBody, "stop", `{"prompt_tokens":10,"completion_tokens":5}`))
	})
	if _, err := Run(context.Background(), smallInput(), Options{API: APIOpenAI, BaseURL: srv.URL}); err != nil {
		t.Fatalf("a proxy that authenticates some other way must be reachable without a key: %v", err)
	}
	if hdr.Get("Authorization") != "" {
		t.Fatal("no key means no Authorization header, not an empty bearer")
	}
}

func TestOpenAIUserRidesInTheBearerWhenNamed(t *testing.T) {
	srv, _, hdr, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		_, _ = io.WriteString(w, sse(reviewBody, "stop", `{"prompt_tokens":10,"completion_tokens":5}`))
	})
	_, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k1", APIUser: "chris",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := hdr.Get("Authorization"); got != "Bearer user=chris&key=k1" {
		t.Fatalf("a gateway that meters by caller wants the user beside the key, got %q", got)
	}
}

func TestOpenAIVendorEndpointNeedsAKeyBeforeUploading(t *testing.T) {
	_, err := Run(context.Background(), smallInput(), Options{API: APIOpenAI})
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("the vendor's endpoint always needs a key; say which before sending anything, got %v", err)
	}
}

func TestOpenAIRefusesExploreModeBeforeCalling(t *testing.T) {
	srv, hits, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {})
	_, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k", Mode: ModeExplore,
	})
	if err == nil || !strings.Contains(err.Error(), "explore") {
		t.Fatalf("explore is Anthropic-only and must say so, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("nothing must be sent")
	}
}

func TestUnknownAPIIsRefused(t *testing.T) {
	_, err := Run(context.Background(), smallInput(), Options{API: "bedrock"})
	if err == nil || !strings.Contains(err.Error(), "bedrock") {
		t.Fatalf("an unknown api must be named in the refusal, got %v", err)
	}
}

func TestAPIDefaultsToAnthropicWithItsOwnModel(t *testing.T) {
	got := (Options{}).withDefaults()
	if got.API != APIAnthropic || got.Model != DefaultModel {
		t.Fatalf("api=%q model=%q", got.API, got.Model)
	}
	if got := (Options{API: APIOpenAI, Model: "my-proxy-alias"}).withDefaults(); got.Model != "my-proxy-alias" {
		t.Fatal("a named model must win over the api default")
	}
}

func TestOpenAIDefaultModelHasARate(t *testing.T) {
	if _, ok := LookupPricing(DefaultOpenAIModel); !ok {
		t.Fatal("the default openai model must have a rate, or every run prints an unknown cost")
	}
	mini, _ := LookupPricing("gpt-5-mini")
	full, _ := LookupPricing("gpt-5")
	if mini.InPerM >= full.InPerM {
		t.Fatal("longest prefix must win, or gpt-5-mini prices as gpt-5")
	}
}

func TestStreamThatEndsEarlyIsAnError(t *testing.T) {
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\"},\"finish_reason\":null}]}\n\n")
	})
	_, err := Run(context.Background(), smallInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k",
	})
	if err == nil || !strings.Contains(err.Error(), "ended before") {
		t.Fatalf("a stream cut off with no finish reason is not a review, got %v", err)
	}
}

func TestStripFences(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                   `{"a":1}`,
		"```json\n{\"a\":1}\n```":   `{"a":1}`,
		"```\n{\"a\":1}\n```\n":     `{"a":1}`,
		"  \n```json\n{\"a\":1}```": `{"a":1}`,
	}
	for in, want := range cases {
		if got := stripFences(in); got != want {
			t.Errorf("stripFences(%q) = %q, want %q", in, got, want)
		}
	}
}
