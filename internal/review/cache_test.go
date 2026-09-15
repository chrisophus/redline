package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

// The breakpoint goes at the end of the shared prefix and nowhere else, and
// the block it sits on is byte-identical between the two stages. That pair of
// facts is the whole mechanism: the review writes an entry at the end of that
// block and the ruling, which sends the same block and then its own, reads it
// back instead of paying for it again.
func TestTheSharedPrefixCarriesTheOnlyBreakpoint(t *testing.T) {
	opts := Options{
		BaseURL: "", APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
		Cache: true, CacheTTL: CacheTTL5m,
	}
	prefix := "the whole prompt, identical on both calls"
	prefixes := map[string]string{}
	for _, tc := range []struct {
		stage  string
		res    *Result
		blocks int
	}{
		{StageReview, &Result{System: "the system prompt", Prompt: prefix, Stage: StageReview}, 1},
		{StageRuling, &Result{
			System: "the system prompt",
			Prompt: prefix,
			Tail:   "\n## The findings to rule on\n",
			Stage:  StageRuling,
		}, 2},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, "{}")))
			o := opts
			o.BaseURL = api.srv.URL
			if _, err := completeAnthropic(context.Background(), o, tc.res); err != nil {
				t.Fatal(err)
			}
			req := requestOf(t, api)
			if hasCacheControl(req.System[0]) {
				t.Errorf("the breakpoint is on the system block, which caches a few thousand tokens of the ones that matter: %+v", req.System)
			}
			blocks := req.Messages[0].Content
			if len(blocks) != tc.blocks {
				t.Fatalf("the user turn is %d block(s), want %d", len(blocks), tc.blocks)
			}
			if !hasCacheControl(blocks[0]) {
				t.Fatalf("the shared prefix carries no breakpoint, so nothing is ever written: %+v", blocks[0])
			}
			for i, b := range blocks[1:] {
				if hasCacheControl(b) {
					t.Errorf("user block %d is marked too, which writes an entry the next call cannot match: %+v", i+1, b)
				}
			}
			prefixes[tc.stage] = blocks[0].Text
		})
	}
	if prefixes[StageReview] != prefixes[StageRuling] {
		t.Errorf("the two stages send different bytes in the block the entry is keyed on:\n%q\n%q",
			prefixes[StageReview], prefixes[StageRuling])
	}
}

// --no-cache is what a caller measuring against the uncached behaviour gets,
// and it has to be the old request exactly: no marks anywhere, the tail still
// its own block.
func TestWithoutTheFlagNothingIsMarked(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, "{}")))
	res := &Result{System: "the system prompt", Prompt: "the whole prompt", Tail: "\nrule on these\n", Stage: StageRuling}
	if _, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
	}, res); err != nil {
		t.Fatal(err)
	}
	req := requestOf(t, api)
	if hasCacheControl(req.System[0]) {
		t.Errorf("the system block is marked with the cache off: %+v", req.System)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("the user turn is %d block(s), want the prefix and the tail", len(blocks))
	}
	for i, b := range blocks {
		if hasCacheControl(b) {
			t.Errorf("user block %d is marked with the cache off: %+v", i, b)
		}
	}
	if !strings.Contains(blocks[0].Text, res.Prompt) || !strings.Contains(blocks[1].Text, res.Tail) {
		t.Errorf("the prompt and its tail did not reach the wire whole: %+v", blocks)
	}
}

// The hour costs 2x base input to write against the five minutes' 1.25x, so
// which one was asked for has to be what goes out.
func TestTheTTLAskedForIsTheTTLSent(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, "{}")))
	if _, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
		Cache: true, CacheTTL: CacheTTL1h,
	}, &Result{System: "s", Prompt: "p", Stage: StageReview}); err != nil {
		t.Fatal(err)
	}
	got := string(requestOf(t, api).Messages[0].Content[0].CacheControl)
	if !strings.Contains(got, `"ttl":"1h"`) {
		t.Errorf("the breakpoint went out as %s, which is not the hour that was paid for", got)
	}
}

// Two exclusions, each because the write could never be read. Decided in one
// place so a caller cannot reach around it.
func TestAWriteNobodyCanReadIsNotMade(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want bool
	}{
		{"the one-shot pair, which is what it is for", Options{Cache: true, API: APIAnthropic, Samples: 1}, true},
		{"a wire with no breakpoint to place", Options{Cache: true, API: APIOpenAI, Samples: 1}, false},
		{"samples, whose first primes the entry the rest read", Options{Cache: true, API: APIAnthropic, Samples: 3}, true},
		{"turned off", Options{API: APIAnthropic, Samples: 1}, false},
	} {
		if got := tc.opts.cacheOn(); got != tc.want {
			t.Errorf("%s: cacheOn = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A ruling that asked for the cache and read none of it paid the write
// premium on stage one and full rate here, which is worse than never having
// asked. Nothing else in the run would say so: it works, it is only dearer.
func TestARulingThatReadsNoCacheSaysSo(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0,
		`{"rulings":[{"finding":"c1","verdict":"withdrawn","evidence":"e","why":"w"}]}`)))
	in := Input{Report: priors()}
	one, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	one.Review = findings.Review{Comments: []findings.ReviewComment{
		comment("a.go", "x", findings.Question{Kind: findings.QuestionPrecedent, Subject: "Foo"}),
	}}
	var said []string
	opts := Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
		Verify: true, Cache: true, CacheTTL: CacheTTL5m,
		Progress: func(s string) { said = append(said, s) },
		Answer: func(_ context.Context, _ []Question) (*envelope.Envelope, error) {
			return &envelope.Envelope{Notes: []string{"looked, found nothing"}}, nil
		},
	}
	if _, err := Verify(context.Background(), in, opts.withDefaults(), one); err != nil {
		t.Fatal(err)
	}
	// The fake reports no cache read, which is exactly the case: an endpoint
	// that served nothing from cache after a breakpoint was sent.
	var warned bool
	for _, s := range said {
		if strings.Contains(s, "read no cached prefix") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the ruling read nothing back and the run said nothing about it: %q", said)
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
