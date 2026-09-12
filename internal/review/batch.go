package review

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Batch mode is for reviews nobody is waiting on.
//
// The Message Batches endpoint takes every token at half price, input and
// output alike, and answers within twenty-four hours. That window is an
// expiry rather than a promise, which rules it out for `redline review`: a
// pre-push tool that might answer tomorrow is not a pre-push tool. What it
// fits exactly is the eval sweep, which sends one review per fixture, reads
// the scores when they arrive, and cares only about the total. Halving that
// price is also what makes the sweeps affordable that decide every other
// question about cost, so this pays for itself twice.
//
// Single-shot only, and the guards below say so rather than discovering it on
// the wire: a batched request cannot run a tool loop, so explore mode has no
// batch form, and the checking pass is a second call that depends on the
// first, so it has none either.

// batchPollInterval is how often the batch is asked whether it has ended.
// The endpoint asks for no more than one poll a minute per batch, and a sweep
// that finishes in ten minutes loses nothing by learning about it within one.
const batchPollInterval = 30 * time.Second

// RunBatch reviews a set of changes as one Message Batch, at half the price
// of the same reviews sent one at a time.
//
// The results come back in the same order as the inputs, so a caller can pair
// them with whatever it assembled them from. A review that failed on its own
// terms - refused, truncated, unparseable - is returned as its own error in
// the matching slot rather than failing the set, because one bad fixture
// should not throw away the other ten that were paid for.
func RunBatch(ctx context.Context, ins []Input, opts Options) ([]*Result, []error, error) {
	opts = opts.withDefaults()
	if len(ins) == 0 {
		return nil, nil, nil
	}
	if opts.API != APIAnthropic {
		return nil, nil, fmt.Errorf("batch mode is only implemented against the Anthropic API; got %q", opts.API)
	}
	if opts.Mode == ModeExplore {
		return nil, nil, fmt.Errorf("explore mode asks for context over several turns, and a batched request is single-shot; use --mode oneshot to batch")
	}
	if opts.Samples > 1 {
		return nil, nil, fmt.Errorf("--samples unions several reviews of one change; batch the changes instead and sample outside it")
	}
	if opts.Verify {
		return nil, nil, fmt.Errorf("the checking pass reads stage one's findings, so it cannot go out in the same batch as them; run the batch unverified")
	}

	// Assembled and priced before anything is sent, so the tripwire that
	// guards one review guards each of a hundred. The sum is reported too:
	// the per-request guard cannot see that a sweep is about to send fifty
	// of them.
	results := make([]*Result, len(ins))
	var worst float64
	var priced bool
	for i, in := range ins {
		res, err := Assemble(in, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("request %d: %w", i, err)
		}
		if res.OverCeiling {
			return nil, nil, fmt.Errorf(
				"request %d: the diff and findings alone are %d tokens against a %d-token ceiling",
				i, res.FixedEstimate, opts.Ceiling)
		}
		// Priced at the tier that will actually bill it, so the guard neither
		// refuses a batch that fits nor waves through one that does not.
		res.CostUSD *= BatchDiscount
		res.CostCeilingUSD *= BatchDiscount
		res.Batched = true
		if res.CostKnown && res.CostCeilingUSD > opts.MaxCostUSD {
			return nil, nil, fmt.Errorf(
				"request %d: worst-case cost %s exceeds the %s tripwire; raise --max-cost or lower --ceiling",
				i, FormatCost(res.CostCeilingUSD, true), FormatCost(opts.MaxCostUSD, true))
		}
		if res.CostKnown {
			priced = true
			worst += res.CostCeilingUSD
		}
		results[i] = res
	}
	if opts.Progress != nil && priced {
		// Half, because that is what the batch tier bills. Said before the
		// send rather than after, since this is the last moment the caller
		// can decide not to.
		opts.Progress(fmt.Sprintf("submitting %d review(s) as one batch: worst case %s at the batch tier's half rate",
			len(ins), FormatCost(worst, true)))
	}

	var clientOpts []option.RequestOption
	if opts.APIKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(opts.BaseURL))
	}
	client := anthropic.NewClient(clientOpts...)

	reqs := make([]anthropic.MessageBatchNewParamsRequest, len(results))
	for i, res := range results {
		p := anthropicParams(opts, res)
		reqs[i] = anthropic.MessageBatchNewParamsRequest{
			CustomID: strconv.Itoa(i),
			Params: anthropic.MessageBatchNewParamsRequestParams{
				Model:        p.Model,
				MaxTokens:    p.MaxTokens,
				System:       p.System,
				Messages:     p.Messages,
				OutputConfig: p.OutputConfig,
			},
		}
	}

	start := time.Now()
	batch, err := client.Messages.Batches.New(ctx, anthropic.MessageBatchNewParams{Requests: reqs})
	if err != nil {
		return results, nil, fmt.Errorf("submitting the batch: %w", err)
	}

	for batch.ProcessingStatus != anthropic.MessageBatchProcessingStatusEnded {
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf("batch %s: %s, %d done of %d, %s elapsed",
				batch.ID, batch.ProcessingStatus,
				batch.RequestCounts.Succeeded+batch.RequestCounts.Errored+
					batch.RequestCounts.Canceled+batch.RequestCounts.Expired,
				len(reqs), time.Since(start).Round(time.Second)))
		}
		select {
		case <-ctx.Done():
			// The batch outlives this process and goes on being billed, so
			// the id is the one thing worth carrying out of a cancellation.
			return results, nil, fmt.Errorf("waiting on batch %s: %w", batch.ID, ctx.Err())
		case <-time.After(batchPollInterval):
		}
		batch, err = client.Messages.Batches.Get(ctx, batch.ID, anthropic.MessageBatchGetParams{})
		if err != nil {
			return results, nil, fmt.Errorf("polling batch %s: %w", batch.ID, err)
		}
	}

	// Results arrive in whatever order they finished, so every one of them is
	// placed by its custom id and never by its position.
	errs := make([]error, len(results))
	seen := make([]bool, len(results))
	stream := client.Messages.Batches.ResultsStreaming(ctx, batch.ID, anthropic.MessageBatchResultsParams{})
	for stream.Next() {
		item := stream.Current()
		i, convErr := strconv.Atoi(item.CustomID)
		if convErr != nil || i < 0 || i >= len(results) {
			return results, errs, fmt.Errorf("batch %s returned custom_id %q, which is not one of the %d sent",
				batch.ID, item.CustomID, len(results))
		}
		seen[i] = true
		res := results[i]
		res.Duration = time.Since(start)
		res.Turns = 1
		switch item.Result.AsAny().(type) {
		case anthropic.MessageBatchSucceededResult:
			msg := item.Result.Message
			c := completion{
				text:       textOf(msg),
				stopReason: string(msg.StopReason),
				usage: Usage{
					InputTokens:      msg.Usage.InputTokens,
					OutputTokens:     msg.Usage.OutputTokens,
					CacheReadTokens:  msg.Usage.CacheReadInputTokens,
					CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
				},
				truncated: msg.StopReason == anthropic.StopReasonMaxTokens,
			}
			if msg.StopReason == anthropic.StopReasonRefusal {
				c.refused = true
				c.detail = string(msg.StopDetails.Category)
			}
			res.Usage = c.usage
			res.CostUSD, res.CostKnown = res.Usage.Cost(opts.Model)
			res.CostUSD *= BatchDiscount
			errs[i] = res.absorb(opts, res.stage(), c)
		default:
			// Errored, canceled or expired. The tokens an errored request
			// read are still billed, but the batch result carries no usage
			// to record them from, so the slot stays at its estimate and
			// says why rather than reporting a free call.
			// Only the errored variant carries a message; canceled and
			// expired leave it empty, and "came back expired:" with nothing
			// after the colon reads as a truncated error rather than a
			// complete one.
			why := item.Result.Error.Error.Message
			if strings.TrimSpace(why) == "" {
				why = "the batch endpoint gave no reason"
			}
			errs[i] = fmt.Errorf("batch request %d came back %s: %s",
				i, item.Result.Type, why)
		}
	}
	if err := stream.Err(); err != nil {
		return results, errs, fmt.Errorf("reading results for batch %s: %w", batch.ID, err)
	}
	for i, ok := range seen {
		if !ok {
			errs[i] = fmt.Errorf("batch %s returned no result for request %d", batch.ID, i)
		}
	}
	return results, errs, nil
}
