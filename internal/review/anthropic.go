package review

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// APIs. Which wire the one call goes over. The prompt, the output schema,
// the ceiling and the cost tripwire are decided before this choice is
// consulted, so a review is the same review whichever wire carries it.
const (
	APIAnthropic = "anthropic"
	APIOpenAI    = "openai"
)

// idleTimeout bounds the gap between bytes arriving on the wire, not the
// call: a heavy-reasoning turn legitimately runs many minutes, and the API
// sends something - a ping if nothing else - every few seconds while it is
// really there.
//
// It has to sit below the SSE decoder, not in the turn loop: the decoder
// discards ping frames before a caller ever sees them (anthropic-sdk-go's
// packages/ssestream, `case "ping": continue`), so resetting a timer on
// every stream.Next() starves on exactly the traffic that proves the
// connection is alive - caught by a test written against that assumption,
// which failed until the timeout moved here.
//
// A gap this wide with no bytes at all, ping included, is a stalled
// connection, and the failure this exists to name: a call that hung for
// upwards of twenty minutes with the heartbeat silent throughout, because
// the heartbeat only ever reports what a stream event told it and no event
// ever arrived to tell it anything.
//
// A var so a test can drive a stall in milliseconds rather than minutes;
// nothing in the product writes to it.
var idleTimeout = 120 * time.Second

// idleTransport wraps the default transport so a streamed response times
// out on its own silence, independent of what the API is sending: a read
// that produces nothing for idleTimeout fails with an error naming the
// stall, rather than blocking forever.
type idleTransport struct{}

func (idleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &idleReader{r: resp.Body}
	return resp, nil
}

// idleReader races each Read against idleTimeout, not the whole response: a
// stream that keeps producing bytes, however slowly overall, never trips it.
//
// The underlying Read runs in its own goroutine reading into a private
// buffer, not the caller's: a Read that times out has already handed the
// caller's buffer back for other use, and a late-arriving Read into it would
// race whatever reused it. The buffer, and the goroutine reading into it, are
// abandoned on a timeout; both are freed once the real Read finally returns,
// which Close (via the deferred stream.Close in send) forces by closing the
// underlying connection.
type idleReader struct {
	r io.ReadCloser
}

func (d *idleReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
		buf []byte
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, len(p))
		n, err := d.r.Read(buf)
		ch <- result{n, err, buf}
	}()
	select {
	case res := <-ch:
		copy(p, res.buf[:res.n])
		return res.n, res.err
	case <-time.After(idleTimeout):
		return 0, fmt.Errorf("no data received in %s, not even a ping: the connection stalled, this is not the model reasoning", idleTimeout)
	}
}

func (d *idleReader) Close() error { return d.r.Close() }

// completion is what one pass returned, across all its turns, in terms Run
// reads without knowing which API produced them.
type completion struct {
	text       string
	stopReason string
	// thinking is the reasoning summary the stream carried, joined across
	// thinking blocks. Empty unless the call asked for a summary, since Sonnet 5
	// sends thinking blocks with no text by default. Only --debug keeps it.
	thinking string
	// usage is filled on every path, including the ones that fail, because
	// the input is billed as soon as the request is accepted.
	usage Usage
	// fromTool records that the body was assembled from tool calls rather
	// than read from the content channel. False means the model answered in
	// prose even after being asked for calls: the body is then unconstrained
	// and has to be narrowed to its JSON object before it is parsed.
	fromTool bool
	// turns is how many turns the pass took, rejected how many calls in them
	// were refused for failing their schema, and stopped why the pass ended
	// before the model called done, empty when it did.
	turns    int
	rejected int
	stopped  string
	// fetched is how many held-back context entries the pass asked for.
	fetched int
	// refused is set when the model declined the request. detail says why,
	// when the API said.
	refused bool
	detail  string
	// truncated is set when the response hit the output cap.
	truncated bool
	// model is the model the response says served it, which is not always the
	// one requested: an alias resolves to a snapshot, and a gateway can route
	// elsewhere without saying so anywhere else.
	model string
}

// completeAnthropic runs one pass over the Messages API. Each turn is
// streamed, because the input is large and a non-streaming request at this
// size risks an HTTP timeout after the input has been paid for.
func completeAnthropic(ctx context.Context, opts Options, res *Result) (completion, error) {
	var clientOpts []option.RequestOption
	if opts.APIKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(opts.BaseURL))
	}
	clientOpts = append(clientOpts, option.WithHTTPClient(&http.Client{Transport: idleTransport{}}))
	conv := &anthropicConversation{
		client: anthropic.NewClient(clientOpts...),
		opts:   opts,
		stage:  res.stage(),
		params: anthropicParams(opts, res),
	}
	return converse(ctx, opts, res, conv)
}

// anthropicConversation is one pass's conversation on the Messages API.
//
// The prompt carries a cache breakpoint, placed by anthropicParams, and each
// turn after the first moves a second one to the end of the conversation, so
// a turn reads everything before it from the cache and pays full rate only
// for what the previous turn added. Two breakpoints of the four the endpoint
// allows.
type anthropicConversation struct {
	client anthropic.Client
	opts   Options
	stage  string
	params anthropic.MessageNewParams
	last   anthropic.Message
	// rolling is where the moving breakpoint sits, so the next turn can take
	// it off before placing its own.
	rolling *anthropic.CacheControlEphemeralParam
}

func (a *anthropicConversation) send(ctx context.Context, maxTokens int64) (turnReply, error) {
	a.params.MaxTokens = maxTokens
	stream := a.client.Messages.NewStreaming(ctx, a.params)
	// Next returning false at the end of the stream does not close the
	// response body; only Close does.
	defer func() { _ = stream.Close() }()
	var msg anthropic.Message
	// The message_start event carries the input count before any content
	// arrives, so a stream that breaks partway still reports what it cost.
	reply := func() turnReply {
		return turnReply{
			usage: Usage{
				InputTokens:      msg.Usage.InputTokens,
				OutputTokens:     msg.Usage.OutputTokens,
				CacheReadTokens:  msg.Usage.CacheReadInputTokens,
				CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
				ThinkingTokens:   msg.Usage.OutputTokensDetails.ThinkingTokens,
			},
			thinking: thinkingOf(msg),
			model:    string(msg.Model),
		}
	}
	hb := newHeartbeat(a.opts, a.stage)
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return reply(), err
		}
		hb.observe(ev.Delta.Type, ev.Delta.Text, ev.Delta.Thinking, ev.Delta.PartialJSON)
		if a.opts.onOutput != nil && ev.Type == "content_block_start" {
			a.opts.onOutput()
		}
	}
	if err := stream.Err(); err != nil {
		return reply(), err
	}
	a.last = msg
	r := reply()
	r.stopReason = string(msg.StopReason)
	r.truncated = msg.StopReason == anthropic.StopReasonMaxTokens
	if msg.StopReason == anthropic.StopReasonRefusal {
		r.refused = true
		r.detail = string(msg.StopDetails.Category)
	}
	r.text = textOf(msg)
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			r.calls = append(r.calls, toolCall{ID: t.ID, Name: t.Name, Input: t.Input})
		}
	}
	return r, nil
}

func (a *anthropicConversation) answer(_ turnReply, results []callResult) {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(results))
	for _, r := range results {
		blocks = append(blocks, anthropic.NewToolResultBlock(r.id, r.content, r.isError))
	}
	a.extend(blocks)
}

func (a *anthropicConversation) nudge(_ turnReply, text string) {
	a.extend([]anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(text)})
}

// extend appends the model's last reply and the user turn that answers it,
// and moves the rolling breakpoint onto the end of that turn.
func (a *anthropicConversation) extend(blocks []anthropic.ContentBlockParamUnion) {
	a.params.Messages = append(a.params.Messages, a.last.ToParam(), anthropic.NewUserMessage(blocks...))
	if !a.opts.cacheOn() || len(blocks) == 0 {
		return
	}
	if a.rolling != nil {
		*a.rolling = anthropic.CacheControlEphemeralParam{}
	}
	content := a.params.Messages[len(a.params.Messages)-1].Content
	end := &content[len(content)-1]
	cc := anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTL(a.opts.CacheTTL)}
	switch {
	case end.OfToolResult != nil:
		end.OfToolResult.CacheControl = cc
		a.rolling = &end.OfToolResult.CacheControl
	case end.OfText != nil:
		end.OfText.CacheControl = cc
		a.rolling = &end.OfText.CacheControl
	}
}

// thinkingOf joins the text of a message's thinking blocks. A stream that broke
// partway still carries what it had accumulated, and a call that ran out of
// room while thinking is the one whose reasoning is most worth reading.
func thinkingOf(msg anthropic.Message) string {
	var parts []string
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.ThinkingBlock); ok && strings.TrimSpace(t.Thinking) != "" {
			parts = append(parts, t.Thinking)
		}
	}
	return strings.Join(parts, "\n\n")
}

// anthropicParams is the request one review makes, without sending it.
//
// Split out so the batch path builds the identical request: a batched review
// that differed from a streamed one in any field would make the eval's cheap
// arm measure something other than what ships.
func anthropicParams(opts Options, res *Result) anthropic.MessageNewParams {
	// The shared prefix is its own block, and the breakpoint goes at the end
	// of it. On the system block instead it would cache a few thousand tokens
	// of the hundred and seventy thousand that matter; behind the stage's
	// instruction it would cache bytes the next call does not send.
	var blocks []anthropic.ContentBlockParamUnion
	prefix := anthropic.NewTextBlock(res.Prompt)
	if opts.cacheOn() {
		prefix.OfText.CacheControl = anthropic.CacheControlEphemeralParam{
			TTL: anthropic.CacheControlEphemeralTTL(opts.CacheTTL),
		}
	}
	// Which calls answer this pass, after the cached prompt so every pass of a
	// run reads the same entry, and ahead of the pass's own instruction.
	blocks = append(blocks, prefix, anthropic.NewTextBlock(callsBlock(res.stage(), res.pulls())))
	if res.Tail != "" {
		blocks = append(blocks, anthropic.NewTextBlock(res.Tail))
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(opts.Model),
		MaxTokens: opts.MaxTokens,
		System:    []anthropic.TextBlockParam{{Text: res.System}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
	}
	// Every tool, every time: the same bytes on every call of a run. The
	// choice is never forced: forcing one is incompatible with thinking on
	// this API, and a call left to choose still calls the tool the calls
	// block says answers it, with the loop's nudge-then-parse fallback behind
	// it if it does not. See loop.go.
	params.Tools = anthropicTools(res.pulls())
	params.ToolChoice = anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	// Every call asks for adaptive thinking with a summarized display. Sonnet 5
	// thinks by default now that the choice is never pinned, and asking for the
	// summary is what puts the reasoning on the stream as it is written: the
	// heartbeat counts it there, a stream that is not silent while the model
	// thinks may also keep a proxy that closes quiet streams from closing this
	// one, and the review keeps the summary for the postmortem. Without this
	// the model still thinks and still bills for it, just invisibly.
	//
	// Nothing turns it off any more. --brief was the one caller that did, and
	// it sent {type: "disabled"}, which Fable rejects outright.
	params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
		Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
	}}
	if opts.Effort != "" {
		params.OutputConfig.Effort = anthropic.OutputConfigEffort(opts.Effort)
	}
	return params
}

func textOf(msg anthropic.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
