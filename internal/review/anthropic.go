package review

import (
	"context"
	"strings"

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
	blocks = append(blocks, prefix, anthropic.NewTextBlock(callsBlock(res.stage())))
	if res.Tail != "" {
		blocks = append(blocks, anthropic.NewTextBlock(res.Tail))
	}
	forced := forcesStage(opts)
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(opts.Model),
		MaxTokens: opts.MaxTokens,
		System:    []anthropic.TextBlockParam{{Text: res.System}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
	}
	// Every tool, every time: the same bytes on every call of a run. A pinned
	// call must call some tool, and the calls block says which; a model that
	// refuses the pin is left to choose and told the same thing in words.
	params.Tools = anthropicTools()
	if forced {
		params.ToolChoice = anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}
	} else {
		params.ToolChoice = anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	}
	thinkingOff := opts.Brief && res.stage() == StageReview
	if thinkingOff {
		// Off for a brief review. The reason recorded here once was a cost and
		// recall measurement taken through a proxy that rewrote the system
		// prompt, against a free-form reply the tool calls have since
		// replaced, and it is void. Taken again on 2026-09-14, turning thinking
		// back on left the short prompt's stub replies where they were, 5 of 42
		// against 17 of 126 with it off, so the setting was left alone.
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfDisabled: &anthropic.ThinkingConfigDisabledParam{},
		}
		// No output format here. It existed to bound a reply nothing else
		// bounded, back when this call sent no tools and the model picked a
		// different wrapper on every sample. The tool calls carry the answer
		// now, the same calls on both wires, which the format could never be:
		// openai.go records that a gateway serving one vendor's model over
		// another's protocol ignored response_format on one review in four.
	}
	if opts.Thinking && !thinkingOff {
		// Asked for although Sonnet 5 thinks unasked, because an older model
		// thinks only when asked, and the summary puts the reasoning on the
		// stream as it is written. The heartbeat counts it there, and a stream
		// that is not silent while the model thinks may also keep a proxy that
		// closes quiet streams from closing this one.
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
			Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
		}}
	}
	if opts.Effort != "" {
		params.OutputConfig.Effort = anthropic.OutputConfigEffort(cappedEffort(opts.Effort, thinkingOff))
	}
	return params
}

// forcesTools reports whether this model accepts a tool_choice that requires a
// tool call.
//
// Requiring one keeps a pass from answering in prose. Not every model takes
// it: claude-fable-5-1 answers 400 with `tool_choice: type "tool" and "any"
// are not supported for this model`, which failed the run outright rather than
// degrading. The same request with tool_choice auto returns 200 and makes the
// calls, and the calls block already says which calls answer the pass. If it
// answers in prose anyway the loop asks once for the calls, and then hands the
// prose to the parser.
//
// The test names what was observed to refuse rather than what is known to
// accept, so an unrecognised model keeps the pinned choice and the stronger
// guarantee that comes with it.
func forcesTools(model string) bool {
	return !strings.Contains(model, "fable")
}

// forcesStage reports whether this call requires a tool call: never for a
// model that refuses it, and never for a call asked to think, since a call
// pinned to the tools does not think. See Options.Thinking.
func forcesStage(opts Options) bool {
	return forcesTools(opts.Model) && !opts.Thinking
}

// cappedEffort is the effort a request may ask for once it has also turned
// thinking off.
//
// The two are set from different places and neither knew about the other:
// --brief disables thinking on the review call, --effort is whatever the caller
// passed, and both are documented flag values, so `--brief --effort xhigh` built
// a request the endpoint refuses. It answers 400 with "output_config.effort
// 'xhigh' is not supported when thinking is disabled on this model. Use effort
// 'high' or below, or enable thinking." The same request at 'high' returns 200,
// so the two top rungs come down one step and the run proceeds.
//
// Capping the effort rather than restoring the thinking, because the
// measurement behind the brief call is that its reasoning tokens were not what
// found the defects: that shape with thinking off caught 8 of 31 expectations
// against the product's 7, and thinking on spent the output budget twice over.
// Turning it back on here would undo the thing --brief is for.
func cappedEffort(effort string, thinkingOff bool) string {
	if !thinkingOff {
		return effort
	}
	switch effort {
	case "xhigh", "max":
		return "high"
	}
	return effort
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
