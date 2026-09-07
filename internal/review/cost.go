package review

import (
	"fmt"
	"sort"
	"strings"
)

// Pricing is USD per million tokens, by direction.
type Pricing struct {
	InPerM  float64
	OutPerM float64
}

// priceTable is list rates, keyed by model-id prefix so a dated or versioned
// id resolves to its family without an entry per revision. Longest prefix
// wins.
//
// These drift. They are here so a run can print what it cost rather than
// leave the number to a billing page nobody opens, and a modest absolute
// error does not change that. An unknown model prices as unknown, never as
// zero: a review that silently reports costing nothing is worse than one that
// admits it does not know.
var priceTable = map[string]Pricing{
	"claude-fable-5":  {InPerM: 10, OutPerM: 50},
	"claude-mythos-5": {InPerM: 10, OutPerM: 50},
	"claude-opus-5":   {InPerM: 5, OutPerM: 25},
	"claude-opus-4":   {InPerM: 5, OutPerM: 25},
	"claude-sonnet-5": {InPerM: 2, OutPerM: 10},
	"claude-sonnet-4": {InPerM: 3, OutPerM: 15},
	"claude-haiku-4":  {InPerM: 1, OutPerM: 5},
}

// LookupPricing resolves a model id to its rate by longest-prefix match.
func LookupPricing(model string) (Pricing, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	keys := make([]string, 0, len(priceTable))
	for k := range priceTable {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if strings.HasPrefix(m, k) {
			return priceTable[k], true
		}
	}
	return Pricing{}, false
}

// Usage is what one review actually consumed.
type Usage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

// Cost prices a usage record. ok is false for a model with no entry, and the
// caller must render that as unknown rather than as zero.
//
// The four token types are kept apart rather than summed to one input
// number. Summing them means a caching event reads as a context-size event,
// and the two call for opposite responses.
func (u Usage) Cost(model string) (usd float64, ok bool) {
	p, ok := LookupPricing(model)
	if !ok {
		return 0, false
	}
	const million = 1_000_000.0
	// Cache writes bill above base input and reads far below it. Neither is
	// used by default here (a pre-push tool firing a few times a day pays
	// every write and reads none of them), so the multipliers are stated
	// rather than assumed away.
	in := float64(u.InputTokens)/million*p.InPerM +
		float64(u.CacheWriteTokens)/million*p.InPerM*1.25 +
		float64(u.CacheReadTokens)/million*p.InPerM*0.1
	out := float64(u.OutputTokens) / million * p.OutPerM
	return in + out, true
}

// EstimateCost prices a request before it is sent, from an input-token
// estimate and the output ceiling. It assumes the model spends its whole
// output allowance, so the number is an upper bound.
func EstimateCost(model string, inputTokens int, maxOutput int64) (usd float64, ok bool) {
	return Usage{InputTokens: int64(inputTokens), OutputTokens: maxOutput}.Cost(model)
}

// FormatCost renders a cost for a log line, saying so when it is unknown.
func FormatCost(usd float64, ok bool) string {
	if !ok {
		return "$? (no rate for this model)"
	}
	return fmt.Sprintf("$%.4f", usd)
}
