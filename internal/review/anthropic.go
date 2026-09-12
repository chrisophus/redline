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

	// Still no cache breakpoint on this path, but no longer for the old
	// reason. What blocked one was the response schema: it sits in front of the
	// system block and the ruling sent a different one from the review it
	// follows, so each call wrote the shared prefix and read none of it, at
	// 171,690 and 170,601 tokens on a real run. The contract now goes as a
	// constant tool array with the stage picked by tool_choice, so the review
	// and the ruling send the same bytes ahead of the prompt and a breakpoint
	// would be read back. Marking one is the next change and is not this one;
	// until it lands a write nobody reads would still cost a quarter above
	// base input, so this call pays the base rate and no more.
	//
	// Samples are a separate case and stay uncached whatever happens here:
	// they go out together, so three concurrent calls over one fresh prefix
	// each write it and none reads. Explore mode keeps its own breakpoints,
	// where one growing conversation means a later turn really does read an
	// earlier one.
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
	c := completion{
		text:       structuredOf(msg),
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
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(opts.Model),
		MaxTokens: opts.MaxTokens,
		System:    []anthropic.TextBlockParam{{Text: res.System}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
		// The whole catalogue, every time, with the stage chosen by name. See
		// tools.go for why the contract cannot be a per-call output format.
		Tools:      anthropicTools(),
		ToolChoice: anthropic.ToolChoiceParamOfTool(res.stage()),
	}
	if opts.Effort != "" {
		params.OutputConfig.Effort = anthropic.OutputConfigEffort(opts.Effort)
	}
	return params
}

// structuredOf is the body the parser is handed: the arguments of the tool
// call the request forced.
//
// It falls back to the text blocks when no tool call came back. A model that
// answers a forced tool_choice in prose is a broken contract, not a review
// with no findings, and the parse below will say so with the body in front of
// it. Returning nothing here would report it as an empty response instead,
// which is the one thing this producer must not do.
func structuredOf(msg anthropic.Message) string {
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			return string(t.Input)
		}
	}
	return textOf(msg)
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
