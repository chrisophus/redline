package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Neither one-shot stage marks a prefix for the cache. A schema is part of
// the cached prefix and the ruling's differs from the review's, so a write
// here is a quarter above base input for an entry no later call can read.
func TestTheOneShotCallMarksNothingForTheCache(t *testing.T) {
	for _, tc := range []struct {
		name   string
		res    *Result
		blocks int
	}{
		{"review", &Result{System: "the system prompt", Prompt: "the whole prompt", Schema: outputSchema()}, 1},
		{"ruling", &Result{
			System: "the system prompt",
			Prompt: "the review prompt\n## The findings to rule on\n",
			Schema: ruleSchema(),
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, "{}")))
			if _, err := completeAnthropic(context.Background(), Options{
				BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
			}, tc.res); err != nil {
				t.Fatal(err)
			}
			req := requestOf(t, api)
			if hasCacheControl(req.System[0]) {
				t.Errorf("the system block is marked for a cache nothing reads: %+v", req.System)
			}
			blocks := req.Messages[0].Content
			if len(blocks) != tc.blocks {
				t.Fatalf("the user turn is %d block(s), want %d", len(blocks), tc.blocks)
			}
			for i, b := range blocks {
				if hasCacheControl(b) {
					t.Errorf("user block %d is marked for a cache nothing reads: %+v", i, b)
				}
			}
			if !strings.Contains(blocks[0].Text, tc.res.Prompt) {
				t.Errorf("the prompt did not reach the wire whole: %+v", blocks[0])
			}
		})
	}
}

// Explore mode resends one growing conversation under one schema, so a later
// turn reads what an earlier one wrote. This is where a breakpoint pays, and
// the only place in this package that still carries one.
func TestExploreCarriesCacheBreakpoints(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, "{}")))
	// The reply is not a review, and that is fine: the request was captured
	// before the reply was read.
	_, _ = Run(context.Background(), exploreInput(), exploreOpts(api))
	req := requestOf(t, api)
	if len(req.System) == 0 || !hasCacheControl(req.System[0]) {
		t.Errorf("explore's system block carries no cache breakpoint: %+v", req.System)
	}
	if len(req.Messages) == 0 || len(req.Messages[0].Content) == 0 || !hasCacheControl(req.Messages[0].Content[0]) {
		t.Errorf("explore's opening turn carries no cache breakpoint: %+v", req.Messages)
	}
}

type wireBlock struct {
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control"`
}

type wireRequest struct {
	System   []wireBlock `json:"system"`
	Messages []struct {
		Content []wireBlock `json:"content"`
	} `json:"messages"`
}

func hasCacheControl(b wireBlock) bool { return len(b.CacheControl) > 0 }

func requestOf(t *testing.T, api *exploreAPI) wireRequest {
	t.Helper()
	seen := api.seen()
	if len(seen) == 0 {
		t.Fatal("no request reached the server")
	}
	var req wireRequest
	if err := json.Unmarshal(seen[0], &req); err != nil {
		t.Fatalf("the request is not the shape expected: %v\n%s", err, seen[0])
	}
	return req
}
