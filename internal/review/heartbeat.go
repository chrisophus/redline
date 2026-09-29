package review

import (
	"fmt"
	"sync"
	"time"

	"github.com/chrisophus/redline/internal/envelope"
)

// heartbeatInterval is how often a stream that is still running says so.
// Short enough that someone watching sees movement within a few breaths,
// long enough that a ten-minute call is forty lines rather than four hundred.
//
// A var so the tests can drive a whole stream past it without sleeping;
// nothing in the product writes to it.
var heartbeatInterval = 15 * time.Second

// reasoningWarnNum/Den is how much of the output cap may go to reasoning
// before the heartbeat says what is about to happen rather than only what is
// happening. Three quarters spent with the review not yet started is not a
// call that is going slowly; it is a call that is going to hit the cap and
// return nothing, and the only useful moment to learn that is while it is
// still running.
const (
	reasoningWarnNum = 3
	reasoningWarnDen = 4
)

// heartbeat narrates one streamed call while it streams.
//
// The failure it exists for: a review at high effort spent ten and a half
// minutes emitting reasoning, hit the 64,000-token cap before writing a word
// of the review, and returned nothing. Nothing was printed for the whole of
// it, because the only progress reporting in the producer ran between stages
// and this was one stage. A run that was working and a run that had hung
// looked identical from outside, and the difference cost a dollar to find
// out.
//
// Tokens here are estimated from the characters that went past, not read
// from the API: the counts on the wire arrive with message_delta at the end
// of the turn, which is exactly too late to be worth reporting. The estimate
// is crude and labelled as one; it is the difference between "still
// reasoning" and "nearly out of room", which is all it is asked for.
type heartbeat struct {
	report func(string)
	// label names the pass and the turn the line is about, "findings turn
	// 3". A pass is several turns and each turn is its own stream, so a line
	// that named only the pass could not say which of its waits this is.
	label string
	// cap is the output ceiling the call was sent with, and what the
	// reasoning is measured against.
	cap int64
	// start is when the pass began, so elapsed is the wait a person is
	// actually sitting through. turnStart is when this turn's stream opened;
	// the two are the same on the first turn.
	start     time.Time
	turnStart time.Time
	last      time.Time
	// reasoned and written are characters, not tokens: the conversion is
	// done once, at the moment a line is rendered.
	reasoned int
	written  int
	// warned keeps the cap warning to one line. Repeating it every interval
	// would bury the progress it interrupts.
	warned bool
}

// newHeartbeat returns nil when there is nowhere to report, and every method
// tolerates that: a caller with no Progress func should not have to say so at
// each call site. since is when the pass began; zero means this stream is the
// whole wait.
func newHeartbeat(opts Options, label string, since time.Time) *heartbeat {
	if opts.Progress == nil {
		return nil
	}
	now := time.Now()
	if since.IsZero() {
		since = now
	}
	return &heartbeat{
		report:    opts.Progress,
		label:     label,
		cap:       opts.MaxTokens,
		start:     since,
		turnStart: now,
		last:      now,
	}
}

// observe takes one stream event's delta, in the flat form both the stable
// and the beta unions expose it, and reports if the interval has elapsed.
// Events that carry no content, ping, message_delta, the block boundaries,
// still drive the clock, which is what keeps a stalled stream from looking
// like a finished one.
func (h *heartbeat) observe(kind, text, thinking, partialJSON string) {
	if h == nil {
		return
	}
	switch kind {
	case "thinking_delta":
		h.reasoned += len(thinking)
	case "text_delta":
		h.written += len(text)
	case "input_json_delta":
		h.written += len(partialJSON)
	}
	now := time.Now()
	if now.Sub(h.last) < heartbeatInterval {
		return
	}
	h.last = now
	h.report(h.line(now))
	if h.nearingCap() {
		h.warned = true
		h.report(fmt.Sprintf(
			"%s: reasoning has used ~%s of the %d-token output cap without starting the answer; "+
				"if it does not stop soon the call will hit the cap and return nothing",
			h.label, formatTokens(h.reasoningTokens()), h.cap))
	}
}

// line is one progress report: how long it has been, and where the output
// budget went.
func (h *heartbeat) line(now time.Time) string {
	answer := "answer not started"
	if h.written > 0 {
		answer = "answer ~" + formatTokens(envelope.EstimateTokensLen(h.written)) + " tokens"
	}
	return fmt.Sprintf("%s: %s, reasoning ~%s tokens, %s",
		h.label, h.elapsed(now), formatTokens(h.reasoningTokens()), answer)
}

// elapsed is the wait so far: the pass's, and this turn's when the pass has
// had more than one, since a reader wants to know both that the pass is
// twelve minutes in and that this turn has only just started.
func (h *heartbeat) elapsed(now time.Time) string {
	total := now.Sub(h.start).Round(time.Second)
	s := fmt.Sprintf("%s elapsed", total)
	if turn := now.Sub(h.turnStart).Round(time.Second); !h.turnStart.IsZero() && h.turnStart.After(h.start.Add(time.Second)) {
		s += fmt.Sprintf(" (%s this turn)", turn)
	}
	return s
}

// waitHeartbeat reports at each interval while a call that streams nothing
// back is in flight, and stops when the returned func is called. The
// OpenAI wire answers in one piece, so there is no delta to count and the
// only thing worth saying is that the wait is still on and how long it has
// been. The stop func is safe to call more than once.
func waitHeartbeat(opts Options, label string, since time.Time) func() {
	if opts.Progress == nil {
		return func() {}
	}
	h := newHeartbeat(opts, label, since)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		// The tests drive the interval to zero to see every stream event;
		// a ticker will not take that, so this one ticks as fast as it can.
		t := time.NewTicker(max(heartbeatInterval, time.Millisecond))
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				h.report(fmt.Sprintf("%s: %s, waiting for the reply", h.label, h.elapsed(now)))
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func (h *heartbeat) reasoningTokens() int {
	return envelope.EstimateTokensLen(h.reasoned)
}

// nearingCap is true once reasoning has taken most of the output budget and
// the answer has not begun. Both halves matter: reasoning that ran long and
// then produced a review is a call that worked.
func (h *heartbeat) nearingCap() bool {
	if h.warned || h.written > 0 || h.cap <= 0 {
		return false
	}
	return int64(h.reasoningTokens())*reasoningWarnDen >= h.cap*reasoningWarnNum
}

// formatTokens renders a count for a progress line, where a reader wants the
// magnitude and not the digits.
func formatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
