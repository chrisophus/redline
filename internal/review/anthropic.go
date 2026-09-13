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

// completion is what one backend returns from one turn, in terms Run reads
// without knowing which API produced them.
type completion struct {
	text       string
	stopReason string
	// usage is filled on every path, including the ones that fail, because
	// the input is billed as soon as the request is accepted.
	usage Usage
	// fromTool records that the body came out of a tool call rather than the
	// content channel. Every stage forces a call, so false means something
	// declined to honour that: a gateway that dropped tool_choice, or a model
	// that answered in prose. The body is then unconstrained and has to be
	// narrowed to its JSON object before it is parsed.
	fromTool bool
	// refused is set when the model declined the request. detail says why,
	// when the API said.
	refused bool
	detail  string
	// truncated is set when the response hit the output cap.
	truncated bool
}

// completeAnthropic is the Messages API call. Streamed because the input is
// large and the response may be too: a non-streaming request at this size
// risks an HTTP timeout, and a timeout after paying for 120k of input is the
// worst outcome available.
func completeAnthropic(ctx context.Context, opts Options, res *Result) (completion, error) {
	var clientOpts []option.RequestOption
	if opts.APIKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(opts.BaseURL))
	}
	client := anthropic.NewClient(clientOpts...)

	// This path marks a cache breakpoint now, and anthropicParams places it.
	// What blocked one for a long time was the response schema: it sits in
	// front of the system block and the ruling sent a different one from the
	// review it follows, so each call wrote the shared prefix and read none of
	// it, at 171,690 and 170,601 tokens on a real run. The contract goes as a
	// constant tool array with the stage picked by tool_choice, so the review
	// and the ruling send the same bytes ahead of the prompt and the write is
	// read back.
	//
	// Not every run marks one, and cacheOn is the one place that decides:
	// samples go out together over one fresh prefix, so each would write it and
	// none would read it, the OpenAI wire has no breakpoint to place, and the
	// batch tier's window outlives a five-minute entry. Explore mode keeps its
	// own breakpoints, where one growing conversation means a later turn really
	// does read an earlier one.
	stream := client.Messages.NewStreaming(ctx, anthropicParams(opts, res))
	// Next returning false at the end of the stream does not close the
	// response body; only Close does. One per call here, so a deferred close
	// is enough.
	defer func() { _ = stream.Close() }()
	var msg anthropic.Message
	// The message_start event carries the input count before any content
	// arrives, so a stream that breaks partway still reports what it cost.
	usage := func() Usage {
		return Usage{
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		}
	}
	hb := newHeartbeat(opts, res.stage())
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return completion{usage: usage()}, err
		}
		hb.observe(ev.Delta.Type, ev.Delta.Text, ev.Delta.Thinking, ev.Delta.PartialJSON)
	}
	if err := stream.Err(); err != nil {
		return completion{usage: usage()}, err
	}
	body, fromTool := structuredOf(msg)
	c := completion{
		text:       body,
		fromTool:   fromTool,
		stopReason: string(msg.StopReason),
		usage:      usage(),
		truncated:  msg.StopReason == anthropic.StopReasonMaxTokens,
	}
	if msg.StopReason == anthropic.StopReasonRefusal {
		c.refused = true
		c.detail = string(msg.StopDetails.Category)
	}
	return c, nil
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
	prefix := anthropic.NewTextBlock(res.Prompt)
	if opts.cacheOn() {
		prefix.OfText.CacheControl = anthropic.CacheControlEphemeralParam{
			TTL: anthropic.CacheControlEphemeralTTL(opts.CacheTTL),
		}
	}
	blocks := []anthropic.ContentBlockParamUnion{prefix}
	if res.Tail != "" {
		blocks = append(blocks, anthropic.NewTextBlock(res.Tail))
	}
	// A model that will not be pinned to one tool has to be told in words which
	// stage this call is for, because tool_choice is what says it otherwise.
	// The block goes after the cached prefix, so a run that degrades still
	// reads the same cache entry as one that does not.
	forced := forcesTools(opts.Model)
	if !forced {
		blocks = append(blocks, anthropic.NewTextBlock(
			"\nReturn your answer by calling the "+res.stage()+" tool, and do not answer in prose."))
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(opts.Model),
		MaxTokens: opts.MaxTokens,
		System:    []anthropic.TextBlockParam{{Text: res.System}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
	}
	// The whole catalogue, every time, with the stage chosen by name. See
	// tools.go for why the contract cannot be a per-call output format.
	//
	// A brief review used to be the exception, dropping the tools so the reply
	// came back as free text. That shape was measurable here and nowhere else:
	// completeOpenAI sends the catalogue on every call. Measured apart, the two
	// emissions did not separate, so the exception is gone and both wires send
	// the same request.
	params.Tools = anthropicTools(opts)
	if forced {
		params.ToolChoice = anthropic.ToolChoiceParamOfTool(res.stage())
	} else {
		params.ToolChoice = anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	}
	thinkingOff := opts.Brief && res.stage() == StageReview
	if thinkingOff {
		// Thinking is on by default, and a free-form reply has no grammar
		// bounding its length, so the two together spend the output budget
		// twice over: a brief review measured $0.4405 and $0.4975 on the two
		// probe fixtures against the default shape's $0.10. The ladder ran
		// this same shape with thinking off and caught 8 of those 31
		// expectations against the product's 7, so the reasoning tokens were
		// not what found the defects.
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfDisabled: &anthropic.ThinkingConfigDisabledParam{},
		}
		// No output format here. It existed to bound a reply nothing else
		// bounded, back when this call sent no tools and the model picked a
		// different wrapper on every sample. The tool grammar carries the
		// contract now, and it is the same contract on both wires, which the
		// format could never be: tools.go records that a gateway serving one
		// vendor's model over another's protocol ignored response_format on one
		// review in four.
	}
	if opts.Effort != "" {
		params.OutputConfig.Effort = anthropic.OutputConfigEffort(cappedEffort(opts.Effort, thinkingOff))
	}
	return params
}

// forcesTools reports whether this model accepts tool_choice pinned to one
// tool.
//
// Every stage declares the whole catalogue and picks its contract by name, so
// pinning is how a call says which stage it is. Not every model takes it:
// claude-fable-5-1 answers 400 with `tool_choice: type "tool" and "any" are not
// supported for this model`, which failed the run outright rather than
// degrading. The same request with tool_choice auto returns 200 and calls the
// tool, so the fallback is to ask for the stage in the prompt and let the model
// reach for it. If it answers in prose anyway the extractor already handles
// that: absorb keys on where the body came from, not on which model sent it.
//
// The test names what was observed to refuse rather than what is known to
// accept, so an unrecognised model keeps the pinned choice and the stronger
// guarantee that comes with it.
func forcesTools(model string) bool {
	return !strings.Contains(model, "fable")
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

// structuredOf is the body the parser is handed: the arguments of the tool
// call the request forced. The second return says whether that is what it
// found, so the caller knows whether anything constrained the body.
//
// It falls back to the text blocks when no tool call came back. A model that
// answers a forced tool_choice in prose is a broken contract, not a review
// with no findings, and the parse below will say so with the body in front of
// it. Returning nothing here would report it as an empty response instead,
// which is the one thing this producer must not do.
func structuredOf(msg anthropic.Message) (string, bool) {
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			return string(t.Input), true
		}
	}
	return textOf(msg), false
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
