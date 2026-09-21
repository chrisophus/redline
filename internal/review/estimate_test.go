package review

import "testing"

// The estimate counts the tool array. It rides on every call, so an estimate
// without it quotes a price for a request smaller than the one that is sent.
//
// ruleRequest has always counted it and says why; nothing else did, so every
// price this tool printed was short by the whole catalogue. promptParts made
// that visible without anyone noticing: it listed a `tools` line beside a
// total that excluded it, and the test covering the breakdown allowed for the
// gap rather than failing on it.
func TestTheEstimateCountsTheCatalogue(t *testing.T) {
	opts := Options{Model: "claude-sonnet-5", API: APIAnthropic}.withDefaults()
	res, err := Assemble(exploreInput(), opts)
	if err != nil {
		t.Fatal(err)
	}
	tools := toolsTokens(res.pulls(), res.looks())
	if tools == 0 {
		t.Fatal("this fixture sends no tools, so it cannot test that they are counted")
	}
	if res.InputEstimate < tools {
		t.Errorf("the estimate of %d does not even cover the %d-token catalogue it sends",
			res.InputEstimate, tools)
	}
	var summed int
	for _, p := range res.Parts {
		summed += p.Tokens
	}
	// The parts are what a reader is shown the total is made of, so a total
	// below their sum is a breakdown that does not add up.
	if res.InputEstimate < summed {
		t.Errorf("the estimate is %d against parts summing to %d", res.InputEstimate, summed)
	}
}

// The ruling and the review price the same catalogue. The ruling was already
// right; this pins the two together so they cannot drift apart again.
func TestTheReviewAndTheRulingPriceTheSameCatalogue(t *testing.T) {
	opts := Options{Model: "claude-sonnet-5", API: APIAnthropic}.withDefaults()
	res, err := Assemble(exploreInput(), opts)
	if err != nil {
		t.Fatal(err)
	}
	ruling := res.ruleRequest(exploreInput(), opts, nil, nil)
	if a, b := toolsTokens(res.pulls(), res.looks()), toolsTokens(ruling.pulls(), ruling.looks()); a != b {
		t.Errorf("the review prices %d catalogue token(s) and the ruling %d", a, b)
	}
}
