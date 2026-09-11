package review

import (
	"fmt"
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
	stage  string
	// cap is the output ceiling the call was sent with, and what the
	// reasoning is measured against.
	cap int64
	// start is when the stream opened, so elapsed is the wait a person is
	// actually sitting through.
	start time.Time
	last  time.Time
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
// each call site.
func newHeartbeat(opts Options, stage string) *heartbeat {
	if opts.Progress == nil {
		return nil
	}
	now := time.Now()
	return &heartbeat{
		report: opts.Progress,
		stage:  stage,
		cap:    opts.MaxTokens,
		start:  now,
		last:   now,
	}
}

// observe takes one stream event's delta, in the flat form both the stable
// and the beta unions expose it, and reports if the interval has elapsed.
// Events that carry no content — ping, message_delta, the block boundaries —
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
			h.stage, formatTokens(h.reasoningTokens()), h.cap))
	}
}

// line is one progress report: how long it has been, and where the output
// budget went.
func (h *heartbeat) line(now time.Time) string {
	elapsed := now.Sub(h.start).Round(time.Second)
	answer := "answer not started"
	if h.written > 0 {
		answer = "answer ~" + formatTokens(envelope.EstimateTokensLen(h.written)) + " tokens"
	}
	return fmt.Sprintf("%s: %s elapsed, reasoning ~%s tokens, %s",
		h.stage, elapsed, formatTokens(h.reasoningTokens()), answer)
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
