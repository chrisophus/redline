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

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(opts.Model),
		MaxTokens: opts.MaxTokens,
		System: []anthropic.TextBlockParam{{
			Text: res.System,
		}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(res.Prompt)),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: outputSchema()},
		},
	}
	if opts.Effort != "" {
		params.OutputConfig.Effort = anthropic.OutputConfigEffort(opts.Effort)
	}

	stream := client.Messages.NewStreaming(ctx, params)
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
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return completion{usage: usage()}, err
		}
	}
	if err := stream.Err(); err != nil {
		return completion{usage: usage()}, err
	}
	c := completion{
		text:       textOf(msg),
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

func textOf(msg anthropic.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
