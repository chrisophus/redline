package scout

import (
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/review"
)

// The cost governor, and the prompt-cache arithmetic it needs to be right.
//
// Split out of scout.go because it is a separate concern from driving the
// loop, and because adding it inline pushed that file one line past its size
// limit. The loop asks one question of this file — may the next turn be sent
// — and the answer depends on what the wire charged for the last one.

// overBudget stops the loop before a turn that would take the run past its
// allowance. It prices the turn about to be sent, which is what a governor
// has to do: knowing afterwards that a turn was too expensive is knowing it
// too late.
//
// cachedLast is what the previous turn read from or wrote to the prompt
// cache. When it is more than nothing, the prefix under the breakpoints is
// priced at the cached rate, because that is what the wire will charge for
// it; a governor that prices a cached brief at the full rate stops a run
// three turns before its money is gone. It is evidence rather than
// assumption: a prefix too short to cache reports no cached tokens, and the
// full rate stands.
func overBudget(opts Options, spend Spend, params anthropic.MessageNewParams, cachedLast int64) (bool, string) {
	next := estimateInput(params)
	if cachedLast > 0 {
		next -= cacheDiscount(estimatePrefix(params))
	}
	ceiling, ok := review.CeilingCost(opts.Model, next, opts.MaxTokens)
	if !ok {
		// An unpriced model cannot be governed by cost. Turns still bound it.
		return false, ""
	}
	if spend.CostUSD+ceiling <= opts.MaxCostUSD {
		return false, ""
	}
	return true, fmt.Sprintf(
		"the search for context stopped at its cost cap of %s after %d turn(s); there may be context it had not reached",
		review.FormatCost(opts.MaxCostUSD, true), spend.Turns)
}

// cacheDiscount is how much less a cached prefix of n tokens costs than an
// uncached one, in tokens at the full rate. A cached read is a tenth of the
// input price.
func cacheDiscount(prefix int) int {
	return prefix * 9 / 10
}

// estimatePrefix sizes the part of the request under the cache breakpoints:
// the system prompt and the brief.
func estimatePrefix(params anthropic.MessageNewParams) int {
	n := 0
	for _, s := range params.System {
		n += envelope.EstimateTokens(s.Text)
	}
	if len(params.Messages) > 0 {
		for _, block := range params.Messages[0].Content {
			if t := block.OfText; t != nil {
				n += envelope.EstimateTokens(t.Text)
			}
		}
	}
	return n
}
