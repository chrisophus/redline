package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The one-shot review carries a cache breakpoint on its system block and on
// its user turn, which is what lets the ruling that follows read the whole
// review prompt from the cache rather than pay for it again.
func TestTheReviewRequestCarriesCacheBreakpoints(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, "{}")))
	res := &Result{System: "the system prompt", Prompt: "the whole prompt", Schema: outputSchema()}
	if _, err := completeAnthropic(context.Background(), Options{BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100}, res); err != nil {
		t.Fatal(err)
	}
	req := requestOf(t, api)
	if !hasCacheControl(req.System[0]) {
		t.Errorf("the system block carries no cache breakpoint: %+v", req.System)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 1 || !hasCacheControl(blocks[0]) {
		t.Errorf("the user turn is not one cached block: %+v", blocks)
	}
}

// The ruling names the review's prompt as its cache prefix. On the wire that
// is two blocks: the prefix, byte-identical to the review's and marked, and
// the ruling's own tail after it, unmarked.
func TestTheRulingMarksTheReviewsPrefixOnTheWire(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, "{}")))
	res := &Result{
		System: "the system prompt", CachePrefix: "the review prompt",
		Prompt: "the review prompt\n## The findings to rule on\n", Schema: ruleSchema(),
	}
	if _, err := completeAnthropic(context.Background(), Options{BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100}, res); err != nil {
		t.Fatal(err)
	}
	blocks := requestOf(t, api).Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("the ruling's user turn is %d block(s), want the prefix and the tail", len(blocks))
	}
	if blocks[0].Text != "the review prompt" || !hasCacheControl(blocks[0]) {
		t.Errorf("the first block is not the marked review prompt: %+v", blocks[0])
	}
	if hasCacheControl(blocks[1]) || !strings.Contains(blocks[1].Text, "findings to rule on") {
		t.Errorf("the tail should follow the breakpoint unmarked: %+v", blocks[1])
	}
}

// Explore mode resends the conversation every turn, so its system block and
// opening turn carry the same breakpoints the one-shot call does.
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
