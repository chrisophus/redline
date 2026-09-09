package eval

import (
	"fmt"
	"math"
	"strings"
)

// The bar this producer is measured against.
//
// The goal is a review comparable to GitHub Copilot's in cost and in
// effectiveness. Both halves need numbers, or "comparable" means whatever the
// reader wants it to mean.
//
// Effectiveness, from GitHub's published figures over 60M reviews: 71 percent
// of reviews produce actionable feedback, averaging 5.1 comments, and 29
// percent return clean. The clean rate is the harder target and the one
// homegrown reviewers miss, because a reviewer that always finds something is
// a generator, and padding a clean change is the failure nobody notices until
// the team stops reading the output.
//
// Cost: Copilot's code-review model carried a published multiplier of 13
// premium requests, roughly 52 cents at the legacy overage rate. Copilot has
// since moved to AI Credits billed on token consumption at list API rates, so
// there is no pricing structure left to arbitrage. Matching its cost means
// matching its token spend, which is why the ceiling exists and why every
// review is priced into the ledger.
//
// These are reference points, not thresholds. Nothing fails a build for
// missing them.
const (
	// CopilotCommentRate is the mean comments on reviews that say anything.
	CopilotCommentRate = 5.1
	// CopilotCleanRate is the share of reviews that return no comments.
	CopilotCleanRate = 0.29
	// CopilotCostUSD is roughly what one Copilot code review cost under the
	// premium-request multiplier.
	CopilotCostUSD = 0.52
)

// Rates are the effectiveness numbers a configuration produced, in the same
// shape as the published figures so the two can sit side by side.
type Rates struct {
	Reviews int
	// Silent counts reviews that returned no comments at all.
	Silent int
	// Comments is the total across every review.
	Comments int
}

// CleanRate is the share of reviews that returned nothing.
func (r Rates) CleanRate() float64 {
	if r.Reviews == 0 {
		return 0
	}
	return float64(r.Silent) / float64(r.Reviews)
}

// CommentRate is the mean comments on reviews that said something. Reviews
// that returned clean are excluded, because averaging them in measures the
// clean rate a second time and hides how much a speaking review actually
// says.
func (r Rates) CommentRate() float64 {
	speaking := r.Reviews - r.Silent
	if speaking <= 0 {
		return 0
	}
	return float64(r.Comments) / float64(speaking)
}

// RatesOf reads the effectiveness numbers off a set of scorecards.
func RatesOf(cards []Scorecard) Rates {
	var r Rates
	for _, c := range cards {
		r.Reviews++
		r.Comments += c.Comments
		if c.Comments == 0 {
			r.Silent++
		}
	}
	return r
}

// Scoreboard renders the comparison the goal is stated in. Both halves are
// shown because either alone is easy to win: a reviewer that says nothing has
// a perfect clean rate and no value, and one that comments on everything has
// a high comment rate and no readers.
func Scoreboard(label string, meanCostUSD float64, r Rates, t Totals) string {
	var b strings.Builder
	b.WriteString("| Measure | This run | Copilot |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Config | %s | code review |\n", label)
	// The whole point of this table is a cost comparison against a published
	// number, so an unpriced model must not print as free.
	if math.IsNaN(meanCostUSD) {
		fmt.Fprintf(&b, "| Mean cost per review | unpriced | ~$%.2f |\n", CopilotCostUSD)
	} else {
		fmt.Fprintf(&b, "| Mean cost per review | $%.4f | ~$%.2f |\n", meanCostUSD, CopilotCostUSD)
	}
	fmt.Fprintf(&b, "| Clean rate | %.0f%% (%d/%d) | %.0f%% |\n",
		r.CleanRate()*100, r.Silent, r.Reviews, CopilotCleanRate*100)
	fmt.Fprintf(&b, "| Comments per speaking review | %.1f | %.1f |\n",
		r.CommentRate(), CopilotCommentRate)
	fmt.Fprintf(&b, "| Annotated findings caught | %d/%d | not published |\n", t.Caught, t.Expected)
	fmt.Fprintf(&b, "| False positives | %d | not published |\n", t.QuietViolations)
	if t.KnownGaps > 0 {
		fmt.Fprintf(&b, "| Misses that nothing ships to catch yet | %d | |\n", t.KnownGaps)
	}
	return b.String()
}
