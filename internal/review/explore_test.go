package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

// The explore loop had unit tests for the catalogue and the cost cap, and
// none that drove a request, which is how it shipped calling the blocking
// endpoint: the API refuses a non-streaming request that may run past ten
// minutes, so the mode failed on turn one against a real change and nothing
// in the suite noticed. These drive it against a scripted SSE server, because
// the transport is the part that was wrong.

// anthropicSSE renders one streamed assistant turn. The events are the ones
// Message.Accumulate reads: the input count arrives with message_start, which
// is what lets a broken stream still report what it cost.
func anthropicSSE(stopReason string, inTokens, outTokens int, blocks ...string) string {
	var b strings.Builder
	ev := func(name, data string) {
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", name, data)
	}
	ev("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_1","type":"message",`+
		`"role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,`+
		`"usage":{"input_tokens":%d,"output_tokens":0}}}`, inTokens))
	for i, blk := range blocks {
		b.WriteString(blk)
		_ = i
	}
	ev("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},`+
		`"usage":{"output_tokens":%d}}`, stopReason, outTokens))
	ev("message_stop", `{"type":"message_stop"}`)
	return b.String()
}

// anthropicText is a text block delivered in one delta.
func anthropicText(index int, s string) string {
	raw, _ := json.Marshal(s)
	return fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,"+
		"\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,"+
		"\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n"+
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n",
		index, index, raw, index)
}

// anthropicToolUse is a tool call, whose arguments arrive as partial JSON the way
// the real stream sends them.
func anthropicToolUse(index int, id, name, argsJSON string) string {
	raw, _ := json.Marshal(argsJSON)
	return fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,"+
		"\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":%q,\"input\":{}}}\n\n"+
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,"+
		"\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%s}}\n\n"+
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n",
		index, id, name, index, raw, index)
}

type exploreAPI struct {
	srv *httptest.Server
	mu  sync.Mutex
	// turns counts requests; requests keeps their bodies. Both are written
	// from concurrent handlers once --samples fires several calls at once.
	turns    int
	requests [][]byte
}

func (a *exploreAPI) seen() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([][]byte, len(a.requests))
	copy(out, a.requests)
	return out
}

// serveSSE replies with each script entry in turn, streamed. A raw entry that
// is not valid SSE is sent as-is, which is how the broken-stream case is
// driven.
func serveSSE(t *testing.T, script ...string) *exploreAPI {
	t.Helper()
	api := &exploreAPI{}
	api.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		api.mu.Lock()
		api.requests = append(api.requests, body)
		n := api.turns
		api.turns++
		api.mu.Unlock()
		if n >= len(script) {
			n = len(script) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, script[n])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(api.srv.Close)
	return api
}

func exploreInput() Input {
	return Input{
		Report: &findings.Report{},
		Change: &change.Set{Files: []change.File{{
			Path:  "internal/queue/q.go",
			Diff:  "@@ -10,3 +10,1 @@\n-\tif e == nil {\n-\t\treturn nil\n-\t}\n+\tprocess(e)\n",
			Added: 1,
		}}},
		Envelopes: []*envelope.Envelope{{
			SchemaVersion: envelope.SchemaVersion,
			Provider:      envelope.Provider{Name: "gorefactor", Language: "go"},
			Expansions: []envelope.Expansion{{
				Role:    envelope.RoleHistory,
				File:    "internal/queue/q.go",
				Content: "commit abc\n\n    skip empty entries: a nil from the retry path panicked in production\n",
			}},
		}},
	}
}

func exploreOpts(api *exploreAPI) Options {
	return Options{
		Model: "claude-sonnet-5", Mode: ModeExplore, API: APIAnthropic,
		BaseURL: api.srv.URL, APIKey: "test",
		MaxTokens: 4096, MaxCostUSD: 5, MaxTurns: 5,
	}
}

const exploreReviewJSON = `{"overview":"Removes a nil guard.","files":[{"path":"internal/queue/q.go",` +
	`"summary":"drops the guard"}],"comments":[{"file":"internal/queue/q.go","line":10,` +
	`"severity":"warning","confidence":"high","body":"The guard this removes was added because a nil ` +
	`from the retry path panicked in production."}]}`

// The loop's whole point: read the catalogue, ask for one entry, then review
// with what came back. Two turns, one fetch, a review at the end.
func TestExploreFetchesThenWritesTheReview(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 500, 20, anthropicToolUse(0, "tu_1", fetchToolName, `{"ids":["e0"]}`)),
		anthropicSSE("end_turn", 700, 60, anthropicText(0, exploreReviewJSON)),
	)
	res, err := Run(context.Background(), exploreInput(), exploreOpts(api))
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if res.Turns != 2 {
		t.Errorf("turns = %d, want 2", res.Turns)
	}
	if res.Fetched != 1 {
		t.Errorf("fetched = %d, want 1", res.Fetched)
	}
	if len(res.Review.Comments) != 1 {
		t.Fatalf("comments = %d, want 1: %+v", len(res.Review.Comments), res.Review)
	}
	if res.Usage.InputTokens != 1200 || res.Usage.OutputTokens != 80 {
		t.Errorf("usage = %+v, want the two turns summed", res.Usage)
	}
	// The catalogue goes out with no content, and the content only arrives
	// because it was asked for. A loop that shipped the bodies up front would
	// pass every other assertion here.
	first := string(api.seen()[0])
	if strings.Contains(first, "panicked in production") {
		t.Error("the first turn already carried the expansion body; the catalogue is supposed to be a list")
	}
	if !strings.Contains(first, "  e0  ") {
		t.Errorf("the first turn carries no catalogue id:\n%s", first)
	}
	second := string(api.seen()[1])
	if !strings.Contains(second, "panicked in production") {
		t.Errorf("the fetched content never reached the second turn:\n%s", second)
	}
}

// A stream that stops mid-event must fail rather than hang, and must not
// report a review it never received.
//
// Worth knowing how it fails: a truncated event reads as a clean EOF at the
// HTTP level, so the SDK yields an empty message rather than a transport
// error, and the loop reports the empty response. That is the honest end of
// the two, but it does mean a mid-stream disconnect and a model that returned
// nothing are the same message here.
func TestExploreSurfacesABrokenStream(t *testing.T) {
	api := serveSSE(t, "event: message_start\ndata: {\"type\":\"message_start\",\"messa")
	res, err := Run(context.Background(), exploreInput(), exploreOpts(api))
	if err == nil {
		t.Fatal("a truncated stream must be an error, not an empty review")
	}
	if len(res.Review.Comments) != 0 {
		t.Errorf("a failed turn produced comments: %+v", res.Review)
	}
	if res == nil {
		t.Fatal("the result is needed even on failure: it carries what the call cost")
	}
}

// Reaching the cap stops the fetching rather than the review: the tools are
// withdrawn, the model is told, and it writes the review from what it has.
func TestExploreStopsFetchingAtTheCostCap(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 900_000, 500, anthropicToolUse(0, "tu_1", fetchToolName, `{"ids":["e0"]}`)),
		anthropicSSE("end_turn", 900_000, 500, anthropicText(0, exploreReviewJSON)),
	)
	opts := exploreOpts(api)
	opts.MaxCostUSD = 2.0
	res, err := Run(context.Background(), exploreInput(), opts)
	if err != nil {
		t.Fatalf("hitting the cap must still produce a review: %v", err)
	}
	if !res.CapHit {
		t.Errorf("the cap was not recorded, so the report cannot say the context was cut short")
	}
	if len(res.Review.Comments) != 1 {
		t.Errorf("the review was lost when the cap was hit: %+v", res.Review)
	}
	seen := api.seen()
	last := string(seen[len(seen)-1])
	if !strings.Contains(last, "budget for this review is spent") {
		t.Errorf("the model was not told to stop fetching:\n%s", last)
	}
	if strings.Contains(last, `"tools"`) {
		t.Error("the tools were still offered after the cap")
	}
}
