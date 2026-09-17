package review

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// reply is one turn's calls as a Messages API stream.
func reply(calls ...[2]string) string {
	var b strings.Builder
	for i, c := range calls {
		b.WriteString(anthropicToolUse(i, "toolu_"+c[0]+string(rune('a'+i)), c[0], c[1]))
	}
	return anthropicSSE("tool_use", 100, 50, b.String())
}

const goodComment = `{"file":"internal/queue/q.go","line":10,"severity":"warning","confidence":"high",` +
	`"body":"process is called with a nil entry","question_kind":"diff","question_ask":"","question_subject":""}`

func loopOpts(api *exploreAPI) Options {
	return Options{API: APIAnthropic, BaseURL: api.srv.URL, APIKey: "k",
		Model: "claude-sonnet-5", MaxTokens: 8000, MaxCostUSD: 5, Cache: true, CacheTTL: CacheTTL5m}
}

// wireBlockFull is one content block of a request message, with the fields
// these tests read, cache breakpoint included.
type wireBlockFull struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	ToolUseID    string          `json:"tool_use_id"`
	Content      json.RawMessage `json:"content"`
	IsError      bool            `json:"is_error"`
	CacheControl json.RawMessage `json:"cache_control"`
}

func messagesOf(t *testing.T, raw []byte) []struct {
	Role    string          `json:"role"`
	Content []wireBlockFull `json:"content"`
} {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content []wireBlockFull `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

// A call that fails its schema costs only itself. It is answered with what is
// wrong, the model sends it again, and the pass keeps both what came before
// and the fixed call.
func TestARejectedCallIsSentBackAndTheRetryIsRecorded(t *testing.T) {
	bad := `{"file":"internal/queue/q.go","line":10,"confidence":"high","body":"x",` +
		`"question_kind":"diff","question_ask":"","question_subject":""}`
	api := serveSSE(t,
		reply([2]string{CallComment, bad}, [2]string{CallDone, `{}`}),
		reply([2]string{CallComment, goodComment}, [2]string{CallDone, `{}`}),
	)
	res := &Result{System: "s", Prompt: "p", Stage: StageFindings, FilesShown: 1}
	out, err := runOnce(context.Background(), exploreInput(), loopOpts(api), res)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Review.Comments) != 1 || out.Rejected != 1 || out.CallTurns != 2 || len(out.Stopped) != 0 {
		t.Fatalf("comments=%d rejected=%d turns=%d stopped=%v, want 1, 1, 2 and none",
			len(out.Review.Comments), out.Rejected, out.CallTurns, out.Stopped)
	}
	msgs := messagesOf(t, api.seen()[1])
	last := msgs[len(msgs)-1]
	var rejected, notDone bool
	for _, b := range last.Content {
		if b.Type != "tool_result" || !b.IsError {
			continue
		}
		rejected = rejected || strings.Contains(string(b.Content), "missing required severity")
		notDone = notDone || strings.Contains(string(b.Content), "Not done")
	}
	if !rejected || !notDone {
		t.Errorf("the second turn must answer the bad call with what is wrong and refuse the done beside it: %+v", last.Content)
	}
}

// A describing pass that calls done before it has set the overview is told so,
// rather than ending with the one thing the review cannot use missing.
func TestDoneIsRefusedUntilTheOverviewIsSet(t *testing.T) {
	api := serveSSE(t,
		reply([2]string{CallFile, `{"path":"internal/queue/q.go","summary":"drops the guard"}`}, [2]string{CallDone, `{}`}),
		reply([2]string{CallOverview, `{"overview":"Drops a nil guard."}`}, [2]string{CallDone, `{}`}),
	)
	res := &Result{System: "s", Prompt: "p", Stage: StageSynopsis}
	out, err := runOnce(context.Background(), exploreInput(), loopOpts(api), res)
	if err != nil {
		t.Fatal(err)
	}
	if out.Review.Overview != "Drops a nil guard." || len(out.Review.Files) != 1 {
		t.Fatalf("the pass must keep the file line and gain the overview: %+v", out.Review)
	}
	if len(api.seen()) != 2 {
		t.Errorf("the refused done must lead to one more turn, got %d request(s)", len(api.seen()))
	}
}

// A pass takes only its own calls. A describing pass that files a comment has
// done the judging pass's job without its instruction.
func TestAPassRefusesCallsThatBelongToAnotherPass(t *testing.T) {
	api := serveSSE(t,
		reply([2]string{CallOverview, `{"overview":"o"}`}, [2]string{CallComment, goodComment}, [2]string{CallDone, `{}`}),
		reply([2]string{CallDone, `{}`}),
	)
	res := &Result{System: "s", Prompt: "p", Stage: StageSynopsis}
	out, err := runOnce(context.Background(), exploreInput(), loopOpts(api), res)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rejected != 1 || len(out.Review.Comments) != 0 {
		t.Fatalf("rejected=%d comments=%d, want the comment refused and not recorded", out.Rejected, len(out.Review.Comments))
	}
	if !strings.Contains(string(api.seen()[1]), "this pass does not take add_comment") {
		t.Error("the refusal must say which calls this pass takes")
	}
}

// A model that resends what it already sent does not double the review.
func TestACallSentAgainDoesNotDuplicate(t *testing.T) {
	api := serveSSE(t,
		reply([2]string{CallComment, goodComment}),
		reply([2]string{CallComment, goodComment}, [2]string{CallDone, `{}`}),
	)
	res := &Result{System: "s", Prompt: "p", Stage: StageFindings}
	out, err := runOnce(context.Background(), exploreInput(), loopOpts(api), res)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Review.Comments) != 1 {
		t.Fatalf("comments = %d, want the repeat folded into the first", len(out.Review.Comments))
	}
}

// A pass that never calls done stops at the turn cap, keeps what it recorded,
// and says it stopped.
func TestTheTurnCapStopsAPassAndKeepsWhatItHad(t *testing.T) {
	api := serveSSE(t, reply([2]string{CallComment, goodComment}))
	opts := loopOpts(api)
	opts.CallTurns = 3
	res := &Result{System: "s", Prompt: "p", Stage: StageFindings}
	out, err := runOnce(context.Background(), exploreInput(), opts, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.seen()) != 3 || len(out.Review.Comments) != 1 {
		t.Fatalf("requests=%d comments=%d, want 3 and 1", len(api.seen()), len(out.Review.Comments))
	}
	if len(out.Stopped) != 1 || !strings.Contains(out.Stopped[0], StoppedTurnCap) {
		t.Errorf("stopped = %v, want the turn cap named", out.Stopped)
	}
}

// A turn that comes back broken after earlier turns recorded something keeps
// what they recorded, and says the pass stopped early.
func TestABrokenTurnKeepsWhatEarlierTurnsRecorded(t *testing.T) {
	api := serveSSE(t,
		reply([2]string{CallComment, goodComment}),
		"this is not an event stream",
	)
	res := &Result{System: "s", Prompt: "p", Stage: StageFindings}
	out, err := runOnce(context.Background(), exploreInput(), loopOpts(api), res)
	if err != nil {
		t.Fatalf("a pass with something recorded must not fail on a later turn: %v", err)
	}
	if len(out.Review.Comments) != 1 || len(out.Stopped) != 1 {
		t.Fatalf("comments=%d stopped=%v, want the comment kept and the early stop named", len(out.Review.Comments), out.Stopped)
	}
}

// A pass never costs more than it was priced at: each turn's output cap is what
// the pass's ceiling can still pay for, and a pass with no room left for a
// useful turn stops on the cost cap.
func TestAPassStopsAtThePriceItWasGiven(t *testing.T) {
	api := serveSSE(t, anthropicSSE("tool_use", 1000, 4000,
		anthropicToolUse(0, "toolu_1", CallComment, goodComment)))
	opts := loopOpts(api)
	opts.MaxTokens = 8000
	// Priced for a 1,000-token prompt and 5,000 output tokens: the first turn
	// spends 4,000 of them, which leaves no room for another useful turn under
	// a cap of 8,000.
	ceiling, _ := CeilingCost(opts.Model, 1000, 5000)
	res := &Result{System: "s", Prompt: "p", Stage: StageFindings, InputEstimate: 1000,
		CostCeilingUSD: ceiling, CostKnown: true}
	out, err := runOnce(context.Background(), exploreInput(), opts, res)
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		MaxTokens int64 `json:"max_tokens"`
	}
	if err := json.Unmarshal(api.seen()[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.MaxTokens > 5000 {
		t.Errorf("the first turn's cap is %d, more than the price allows", first.MaxTokens)
	}
	if len(out.Stopped) != 1 || !strings.Contains(out.Stopped[0], StoppedCostCap) {
		t.Errorf("stopped = %v, want the cost cap named", out.Stopped)
	}
	if len(out.Review.Comments) != 1 {
		t.Errorf("the comment the first turn recorded must be kept: %+v", out.Review.Comments)
	}
}

// Each turn resends the conversation, so the breakpoint moves to the newest
// turn and the prompt keeps its own: two marks, never more, whatever the turn.
func TestTheBreakpointMovesWithTheConversation(t *testing.T) {
	api := serveSSE(t,
		reply([2]string{CallComment, goodComment}),
		reply([2]string{CallComment, strings.Replace(goodComment, `"line":10`, `"line":11`, 1)}),
		reply([2]string{CallDone, `{}`}),
	)
	res := &Result{System: "s", Prompt: "the prompt", Stage: StageFindings}
	if _, err := runOnce(context.Background(), exploreInput(), loopOpts(api), res); err != nil {
		t.Fatal(err)
	}
	seen := api.seen()
	if len(seen) != 3 {
		t.Fatalf("want three turns, got %d", len(seen))
	}
	for i, raw := range seen {
		marks := 0
		var lastMarked bool
		msgs := messagesOf(t, raw)
		for mi, m := range msgs {
			for bi, b := range m.Content {
				if len(b.CacheControl) > 0 && string(b.CacheControl) != "null" {
					marks++
					lastMarked = mi == len(msgs)-1 && bi == len(m.Content)-1
				}
			}
		}
		want := 2
		if i == 0 {
			want = 1
		}
		if marks != want {
			t.Errorf("turn %d carries %d breakpoint(s), want %d", i+1, marks, want)
		}
		if i > 0 && !lastMarked {
			t.Errorf("turn %d does not mark the end of the conversation", i+1)
		}
	}
}

// The OpenAI wire runs the same loop: the assistant's calls go back as it sent
// them and each is answered by a tool message naming its call.
func TestTheOpenAIWireAnswersEachCall(t *testing.T) {
	var requests []openAIRequest
	turn := 0
	srv, _, _, _ := openAIServer(t, func(w http.ResponseWriter, req openAIRequest) {
		requests = append(requests, req)
		turn++
		body := `{"comments":[]}`
		if turn == 1 {
			_, _ = io.WriteString(w, oaEnvelope(t, map[string]any{"tool_calls": []map[string]any{{
				"id": "call_a", "type": "function",
				"function": map[string]any{"name": CallComment, "arguments": goodComment},
			}}}, "tool_calls", ""))
			return
		}
		_, _ = io.WriteString(w, toolReply(t, body, "tool_calls", ""))
	})
	res := &Result{System: "s", Prompt: "p", Stage: StageFindings}
	out, err := runOnce(context.Background(), exploreInput(), Options{
		API: APIOpenAI, BaseURL: srv.URL, APIKey: "k", Model: "gpt-5", MaxTokens: 4000,
	}, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Review.Comments) != 1 || len(requests) != 2 {
		t.Fatalf("comments=%d requests=%d, want 1 and 2", len(out.Review.Comments), len(requests))
	}
	msgs := requests[1].Messages
	if len(msgs) != 4 || msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 ||
		msgs[3].Role != "tool" || msgs[3].ToolCallID != "call_a" || !strings.HasPrefix(msgs[3].Content, "Recorded.") {
		t.Fatalf("the second request must resend the call and answer it: %+v", msgs)
	}
}

// The check names the field and what is wrong with it, because that text is
// what the model is sent to fix the call.
func TestValidateNamesTheFieldAndTheProblem(t *testing.T) {
	var in any
	_ = json.Unmarshal([]byte(`{"file":"a.go","line":"3","severity":"major","extra":1}`), &in)
	got := strings.Join(validate(commentCallSchema(), in, CallComment), "\n")
	for _, want := range []string{
		"add_comment.line: want an integer, got the string \"3\"",
		"add_comment.severity: major is not one of error, warning, info",
		"add_comment: missing required confidence",
		"add_comment: unexpected field extra",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A pass whose work is visibly complete ends on the reply that completed it:
// another request would only resend the conversation, thinking included, to
// collect done. A findings pass has no such test and still waits for done.
func TestACompletePassEndsWithoutWaitingForDone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stage  string
		expect passExpect
		first  string
		want   int
	}{
		{"a describing pass with every file described", StageSynopsis,
			passExpect{files: []string{"internal/queue/q.go"}},
			reply([2]string{CallOverview, `{"overview":"o"}`},
				[2]string{CallFile, `{"path":"internal/queue/q.go","summary":"s"}`}), 1},
		{"a describing pass still owed a file", StageSynopsis,
			passExpect{files: []string{"internal/queue/q.go", "b.go"}},
			reply([2]string{CallOverview, `{"overview":"o"}`},
				[2]string{CallFile, `{"path":"internal/queue/q.go","summary":"s"}`}), 2},
		{"a split's describing pass with no cohorts yet", StageSynopsis,
			passExpect{files: []string{"internal/queue/q.go"}, cohorts: true},
			reply([2]string{CallOverview, `{"overview":"o"}`},
				[2]string{CallFile, `{"path":"internal/queue/q.go","summary":"s"}`}), 2},
		{"a ruling on every finding", StageRuling,
			passExpect{findings: []string{"c1"}},
			reply([2]string{CallRule, `{"finding":"c1","analysis":"a","verdict":"kept","evidence":"e","why":"w"}`}), 1},
		{"a findings pass, which has no such test", StageFindings,
			passExpect{},
			reply([2]string{CallComment, goodComment}), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := serveSSE(t, tc.first, reply([2]string{CallDone, `{}`}))
			res := &Result{System: "s", Prompt: "p", Stage: tc.stage, expect: tc.expect}
			if _, err := runOnce(context.Background(), exploreInput(), loopOpts(api), res); err != nil {
				t.Fatal(err)
			}
			if got := len(api.seen()); got != tc.want {
				t.Errorf("%d request(s), want %d", got, tc.want)
			}
		})
	}
}
