package scout

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/chrisophus/redline/internal/review"
)

// The governor read a total that was only computed after the loop, so inside
// it the accumulated spend was always zero and --max-cost priced one turn's
// ceiling forever. A run could spend every turn without the cap ever firing.
func TestTheCostCapCountsWhatTheRunHasAlreadySpent(t *testing.T) {
	var responses []string
	for i := 0; i < 8; i++ {
		responses = append(responses, msg("tool_use", recordCall(fmt.Sprintf("tu_%d", i))))
	}
	api := serve(t, responses...)

	// Each scripted turn reports 500 input and 40 output tokens. One turn's
	// ceiling is well under the cap; several turns of accumulated spend plus
	// the next ceiling is not, so a governor that accumulates stops early and
	// one that does not runs to the turn limit.
	_, spend, err := runScout(t, api, Options{MaxTurns: 8, MaxTokens: 4000, MaxCostUSD: 0.05})
	if err != nil {
		t.Fatal(err)
	}
	if !spend.CapHit {
		t.Fatalf("ran %d turn(s) and never hit the cap; the governor is not accumulating", spend.Turns)
	}
	if spend.Turns == 0 || spend.Turns >= 8 {
		t.Errorf("turns = %d, want the cap to bind partway through", spend.Turns)
	}
	if spend.CostUSD <= 0 {
		t.Error("the run reported no cost, so nothing was accumulated")
	}
}

// Turn zero writes the prompt cache, and a write bills a quarter above base
// input. The governor priced the whole first request at base, so the ceiling
// for the one turn every run sends was a quarter of the prefix short.
func TestTheFirstTurnIsPricedAtTheRateThatWritesTheCache(t *testing.T) {
	opts := Options{Model: "claude-sonnet-5", MaxTokens: 2000}.withDefaults()
	prefix := strings.Repeat("the brief the scout resends every turn. ", 4000)
	params := anthropic.MessageNewParams{
		System:   []anthropic.TextBlockParam{{Text: prefix}},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prefix))},
	}

	// What the wire bills for turn zero: the prefix at the cache-write rate,
	// and every output token it is allowed.
	p := estimatePrefix(params)
	worst, ok := review.Usage{
		InputTokens:      int64(estimateInput(params) - p),
		CacheWriteTokens: int64(p),
		OutputTokens:     opts.MaxTokens,
	}.Cost(opts.Model)
	if !ok {
		t.Fatalf("%s is unpriced, so this proves nothing", opts.Model)
	}

	opts.MaxCostUSD = worst * 0.99
	if stop, _ := overBudget(opts, Spend{}, params, 0); !stop {
		t.Errorf("a turn whose worst case is %s was sent under a cap of %s",
			review.FormatCost(worst, true), review.FormatCost(opts.MaxCostUSD, true))
	}
	// And it still sends what it can afford: a governor that refuses the first
	// turn of every run is a worse bug than the one above.
	opts.MaxCostUSD = worst * 1.01
	if stop, reason := overBudget(opts, Spend{}, params, 0); stop {
		t.Errorf("a payable first turn was refused: %s", reason)
	}
}
