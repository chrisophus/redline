package review

import (
	"context"
	"fmt"
	"time"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

// The describing stage.
//
// One call says what the change is and the call after it says what is wrong
// with it. They are the two jobs the one-shot producer does in one turn, and
// splitting them is worth a call because they were never equal partners inside
// one output cap: on measured runs the file summaries came back sparse or
// missing, and whole reviews were lost to the cap with fifty of them in front
// of the findings.
//
// Both calls send the same system block and the same shared prompt, differing
// only in the tail and the tool they are forced to call, so the second reads
// the prefix the first wrote and pays a tenth for it. That is the whole reason
// this is affordable, and it is why the tail is a separate block rather than
// more prompt.

// ExpectedSynopsisTokens prices the describing call's output. A walkthrough is
// an overview and a line per file: hundreds of tokens on a small change and a
// few thousand on a large one, against the tens of thousands a judging call
// spends on reasoning. Used only for the estimate; the ledger records what it
// actually wrote.
const ExpectedSynopsisTokens int64 = 3000

// synopsisRequest is the describing call, assembled from the review's own
// prefix. Everything ahead of the tail is stage one's bytes exactly.
func (r *Result) synopsisRequest(opts Options, in Input) *Result {
	out := r.clone()
	out.Stage = StageSynopsis
	out.Tail = synopsisTail(in)
	out.InputEstimate = r.InputEstimate + envelope.EstimateTokens(out.Tail)
	out.CostUSD, out.CostKnown = EstimateCost(opts.Model, out.InputEstimate, ExpectedSynopsisTokens)
	out.CostCeilingUSD, _ = CeilingCost(opts.Model, out.InputEstimate, opts.MaxTokens)
	return out
}

// synopsisCeilingCost is what the describing call could cost at worst, for the
// tripwire that decides whether to send anything at all. Zero when the stage
// is off, the same shape verifyCeilingCost uses for the ruling.
func synopsisCeilingCost(opts Options, in Input, res *Result) float64 {
	if !opts.Synopsis || res == nil {
		return 0
	}
	cost, ok := CeilingCost(opts.Model, res.synopsisRequest(opts, in).InputEstimate, opts.MaxTokens)
	if !ok {
		return 0
	}
	return cost
}

// describe runs the stage and returns the walkthrough it wrote.
//
// It never fails the review. A describing call that breaks, refuses, or comes
// back with no overview leaves the run on the one-shot contract, where the
// judging call writes the walkthrough as it always did, and the reason is
// recorded rather than printed and forgotten: a review whose walkthrough is
// thin because the stage fell back and one whose model wrote a thin
// walkthrough are the same artifact otherwise.
//
// The usage is folded in whichever way it ended, because the call was billed
// either way.
func describe(ctx context.Context, in Input, opts Options, res *Result) (findings.Review, Usage, int64, string) {
	if opts.Progress != nil {
		opts.Progress("describing the change before judging it")
	}
	out, err := runOnce(ctx, in, opts, res.synopsisRequest(opts, in))
	usage, written := out.Usage, out.Usage.OutputTokens
	// The describing call's output is not the review's output. Usage carries
	// input, which is real either way; the output count is returned apart so
	// the ledger's median keeps meaning what a judging call writes.
	usage.OutputTokens = 0
	switch {
	case err != nil:
		return findings.Review{}, usage, written, err.Error()
	case out.Review.Overview == "":
		return findings.Review{}, usage, written,
			"the describing call returned no overview"
	}
	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("described %d file(s) in %s",
			len(out.Review.Files), out.Duration.Round(time.Second)))
	}
	return out.Review, usage, written, ""
}

// applySynopsis puts the walkthrough on the review the judging call wrote, and
// records which call it came from.
func applySynopsis(res *Result, model string, walkthrough findings.Review, usage Usage, written int64, failed string) {
	if res == nil {
		return
	}
	res.Usage.InputTokens += usage.InputTokens
	res.Usage.CacheReadTokens += usage.CacheReadTokens
	res.Usage.CacheWriteTokens += usage.CacheWriteTokens
	res.SynopsisOutputTokens += written
	res.SynopsisFailed = failed
	res.recost(model)
	if failed != "" {
		return
	}
	res.Synopsis = true
	res.Review.Overview = walkthrough.Overview
	res.Review.Files = walkthrough.Files
}

// recost recomputes what this run has cost so far.
//
// Usage carries the input of every call and the output of the judging one.
// The describing and ruling calls keep their output apart, so the ledger's
// median stays a claim about what a judging call writes, and their cost is
// added back here because both were billed. One place does this because three
// callers were adding two of the three and each missed a different one.
func (r *Result) recost(model string) {
	if r == nil {
		return
	}
	r.CostUSD, r.CostKnown = r.Usage.Cost(model)
	if !r.CostKnown {
		return
	}
	apart := Usage{OutputTokens: r.RulingOutputTokens + r.SynopsisOutputTokens}
	if extra, ok := apart.Cost(model); ok {
		r.CostUSD += extra
	}
}
