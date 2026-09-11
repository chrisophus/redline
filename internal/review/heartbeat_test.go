package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// These cover the failure that produced them: a review at xhigh effort spent
// ten and a half minutes streaming reasoning, hit the 64,000-token output cap
// before writing a word of the review, and exited with an empty body. Nothing
// was printed while it ran, and the only thing on disk afterwards was a
// zero-byte response file that a refusal would have produced too.

// The message a person reads has to name the thing they can change. Telling
// someone whose cap is already at the model's ceiling to raise the cap costs
// them another ten minutes and another empty answer.
func TestReasoningThatSpendsTheWholeCapSaysSoAndNamesEffort(t *testing.T) {
	api := serveSSE(t, anthropicSSE("max_tokens", 1000, 4096,
		anthropicThinking(0, strings.Repeat("weighing the retry path. ", 400))))
	opts := oneShotOpts(api)
	opts.Effort = "xhigh"

	_, err := Run(context.Background(), exploreInput(), opts)
	if err == nil {
		t.Fatal("a call that produced no content must be an error")
	}
	for _, want := range []string{"reasoning", "--effort", `"xhigh"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name %s so the caller can act on it: %v", want, err)
		}
	}
}

// The other half of the same stop reason: a review that started and ran out
// of room does want a bigger cap, and must still be told so.
func TestATruncatedReviewStillAsksForABiggerCap(t *testing.T) {
	api := serveSSE(t, anthropicSSE("max_tokens", 1000, 4096,
		anthropicText(0, `{"overview":"trunc`)))

	_, err := Run(context.Background(), exploreInput(), oneShotOpts(api))
	if err == nil {
		t.Fatal("a truncated review must be an error")
	}
	if !strings.Contains(err.Error(), "--max-tokens") {
		t.Errorf("a half-written review wants a bigger cap: %v", err)
	}
	if strings.Contains(err.Error(), "reasoning") {
		t.Errorf("the model did start the review, so reasoning is not the diagnosis: %v", err)
	}
}

// A failed call is the one whose record is worth having. Before this, the
// capture ran only after the error check, and wrote the bare body — so every
// failure that mattered left either nothing or an empty file.
func TestAFailedCallIsCapturedWithEnoughToDiagnoseIt(t *testing.T) {
	api := serveSSE(t, anthropicSSE("max_tokens", 1000, 4096,
		anthropicThinking(0, strings.Repeat("still weighing it. ", 300))))
	opts := oneShotOpts(api)
	captured := map[string][]byte{}
	opts.Capture = func(name string, data []byte) { captured[name] = data }

	if _, err := Run(context.Background(), exploreInput(), opts); err == nil {
		t.Fatal("the call must fail for this to be the failing path")
	}
	data, ok := captured["review.response.json"]
	if !ok {
		t.Fatal("a failed call captured no response at all")
	}
	var got struct {
		StopReason string `json:"stopReason"`
		Truncated  bool   `json:"truncated"`
		BodyBytes  int    `json:"bodyBytes"`
		Usage      Usage  `json:"usage"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("the capture must be readable JSON: %v\n%s", err, data)
	}
	if got.StopReason != "max_tokens" {
		t.Errorf("stopReason = %q, want max_tokens: without it a truncation and a refusal read alike",
			got.StopReason)
	}
	if !got.Truncated {
		t.Error("truncated must be recorded; it is the whole diagnosis")
	}
	if got.BodyBytes != 0 {
		t.Errorf("bodyBytes = %d, want 0", got.BodyBytes)
	}
	if got.Usage.OutputTokens != 4096 {
		t.Errorf("outputTokens = %d, want 4096: the call was paid for and the record must say so",
			got.Usage.OutputTokens)
	}
}

// The capture is read with jq or not at all, so a body that is JSON is nested
// as JSON rather than escaped into a string.
func TestACapturedReviewBodyStaysJSON(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 1000, 60, anthropicText(0, exploreReviewJSON)))
	opts := oneShotOpts(api)
	captured := map[string][]byte{}
	opts.Capture = func(name string, data []byte) { captured[name] = data }

	if _, err := Run(context.Background(), exploreInput(), opts); err != nil {
		t.Fatalf("review: %v", err)
	}
	var got struct {
		StopReason string `json:"stopReason"`
		Body       struct {
			Overview string `json:"overview"`
		} `json:"body"`
	}
	if err := json.Unmarshal(captured["review.response.json"], &got); err != nil {
		t.Fatalf("the capture must be readable JSON: %v", err)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("stopReason = %q, want end_turn", got.StopReason)
	}
	if got.Body.Overview != "Removes a nil guard." {
		t.Errorf("the body must nest as JSON, got overview %q", got.Body.Overview)
	}
}

// The silence was the bug as much as the truncation was: Progress existed and
// this path never called it, so a working call and a hung one looked the same
// for ten minutes.
func TestAStreamingCallReportsWhileItRuns(t *testing.T) {
	everyEvent(t)
	api := serveSSE(t, anthropicSSE("end_turn", 1000, 60,
		anthropicThinking(0, strings.Repeat("weighing it. ", 100)),
		anthropicText(1, exploreReviewJSON)))
	opts := oneShotOpts(api)
	var b beats
	opts.Progress = b.record

	if _, err := Run(context.Background(), exploreInput(), opts); err != nil {
		t.Fatalf("review: %v", err)
	}
	var progress []string
	for _, line := range b.all() {
		if strings.HasPrefix(line, "review: ") && strings.Contains(line, "reasoning ~") {
			progress = append(progress, line)
		}
	}
	if len(progress) == 0 {
		t.Fatalf("a streamed call reported nothing while it streamed: %q", b.all())
	}
	if !strings.Contains(progress[len(progress)-1], "answer ~") {
		t.Errorf("once the answer starts the report must say so, got %q", progress[len(progress)-1])
	}
}

// The line worth printing is not that it is still going but that it is about
// to fail: most of the cap spent on reasoning with no answer begun.
func TestTheHeartbeatWarnsBeforeReasoningEatsTheCap(t *testing.T) {
	everyEvent(t)
	// 8000 characters is over 3400 estimated tokens, past three quarters of
	// the 4096 cap these options carry.
	api := serveSSE(t, anthropicSSE("max_tokens", 1000, 4096,
		anthropicThinking(0, strings.Repeat("x", 8000))))
	opts := oneShotOpts(api)
	var b beats
	opts.Progress = b.record

	if _, err := Run(context.Background(), exploreInput(), opts); err == nil {
		t.Fatal("this call hits the cap")
	}
	var warnings int
	for _, line := range b.all() {
		if strings.Contains(line, "without starting the answer") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("want exactly one cap warning, got %d: %q", warnings, b.all())
	}
}

// A call with nowhere to report must still run. Nil Progress is the default
// for every caller that is not the command.
func TestAStreamWithNoProgressFuncStillWorks(t *testing.T) {
	everyEvent(t)
	api := serveSSE(t, anthropicSSE("end_turn", 1000, 60, anthropicText(0, exploreReviewJSON)))
	opts := oneShotOpts(api)
	opts.Progress = nil

	if _, err := Run(context.Background(), exploreInput(), opts); err != nil {
		t.Fatalf("review: %v", err)
	}
}

// The report is about the wait a person is sitting through, so the elapsed
// time has to come from when the stream opened.
func TestTheHeartbeatReportsElapsedFromTheStart(t *testing.T) {
	h := newHeartbeat(Options{Progress: func(string) {}, MaxTokens: 4096}, "review")
	h.start = time.Now().Add(-90 * time.Second)
	if got := h.line(time.Now()); !strings.Contains(got, "1m30s elapsed") {
		t.Errorf("line = %q, want it to report 1m30s elapsed", got)
	}
}

// anthropicThinking is a reasoning block delivered in one delta. Extended
// thinking arrives this way, and it is the shape that can consume the whole
// output cap without ever producing a text block.
func anthropicThinking(index int, s string) string {
	raw, _ := json.Marshal(s)
	return fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,"+
		"\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n"+
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,"+
		"\"delta\":{\"type\":\"thinking_delta\",\"thinking\":%s}}\n\n"+
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n",
		index, index, raw, index)
}

// oneShotOpts is the explore fixture's options pointed at the single call.
func oneShotOpts(api *exploreAPI) Options {
	opts := exploreOpts(api)
	opts.Mode = ModeOneShot
	return opts
}

// beats collects what a run reported while it was running.
type beats struct {
	mu    sync.Mutex
	lines []string
}

func (b *beats) record(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, s)
}

func (b *beats) all() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
}

// everyEvent makes the heartbeat report on each stream event, so a test can
// drive a ten-minute call's worth of behaviour in milliseconds.
func everyEvent(t *testing.T) {
	t.Helper()
	prev := heartbeatInterval
	heartbeatInterval = 0
	t.Cleanup(func() { heartbeatInterval = prev })
}
