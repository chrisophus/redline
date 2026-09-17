package review

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A stream that opens and then sends nothing at all - not a single ping - is
// the failure a run can spend twenty minutes hung on with no error and no
// heartbeat line, because the heartbeat only ever reports what a stream
// event told it. It must be named, and named as itself: distinct from a
// broken pipe, a refusal, or the model running out of room.
func TestAStalledStreamNamesItselfDistinctFromAnyOtherFailure(t *testing.T) {
	old := idleTimeout
	idleTimeout = 20 * time.Millisecond
	defer func() { idleTimeout = old }()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-block
	}))
	defer func() { close(block); srv.Close() }()

	_, err := completeAnthropic(context.Background(), Options{
		BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
	}, &Result{System: "s", Prompt: "p", Stage: StageReview})
	if err == nil || !strings.Contains(err.Error(), "not the model reasoning") {
		t.Fatalf("err = %v, want the stalled-connection message", err)
	}
}

// A stream that keeps sending real events must never trip the same watchdog,
// however long the whole call runs: the timeout bounds the gap between
// events, not the call.
func TestAStreamThatKeepsTalkingNeverStalls(t *testing.T) {
	old := idleTimeout
	idleTimeout = 20 * time.Millisecond
	defer func() { idleTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if f != nil {
				f.Flush()
			}
		}
		write("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\"," +
			"\"role\":\"assistant\",\"model\":\"claude-sonnet-5\",\"content\":[],\"stop_reason\":null," +
			"\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
		// Each ping arrives well inside the shortened idle window, over
		// several times that window's length in total, so a call this long
		// only survives it if the timer is reset on every event rather than
		// armed once for the whole stream. The real API sends exactly this
		// during a long reasoning stretch with nothing else to say yet.
		for i := 0; i < 10; i++ {
			time.Sleep(idleTimeout / 2)
			write("event: ping\ndata: {\"type\":\"ping\"}\n\n")
		}
		write(anthropicText(0, "{}"))
		write("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}," +
			"\"usage\":{\"output_tokens\":5}}\n\n")
		write("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	_, err := completeAnthropic(context.Background(), Options{
		BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
	}, &Result{System: "s", Prompt: "p", Stage: StageReview})
	if err != nil {
		t.Fatalf("a stream that keeps talking must not be called stalled: %v", err)
	}
}
