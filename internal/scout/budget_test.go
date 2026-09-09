package scout

import (
	"fmt"
	"testing"
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
