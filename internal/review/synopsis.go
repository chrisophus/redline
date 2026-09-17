package review

import (
	"context"
	"fmt"
	"sort"
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
	out.expect = passExpect{files: sortedKeys(in.ShownFiles())}
	// The describing half and nothing else. This call is not judging the
	// change, so the judging tail would be two thousand tokens telling it what
	// to do with findings it has been told not to write.
	out.Tail = describingTail + synopsisTail(in)
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
// back with no overview leaves the judging call to write the whole review,
// and the reason is recorded rather than printed and forgotten: a review with
// no walkthrough because the stage failed and one whose model wrote a thin
// walkthrough are otherwise hard to tell apart.
//
// The usage is folded in whichever way it ended, because the call was billed
// either way.
func describe(ctx context.Context, in Input, opts Options, res *Result) (findings.Review, Usage, int64, string, *Result) {
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
		return findings.Review{}, usage, written, err.Error(), out
	case out.Review.Overview == "":
		return findings.Review{}, usage, written,
			"the describing call returned no overview", out
	}
	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("described %d file(s) in %s",
			len(out.Review.Files), out.Duration.Round(time.Second)))
	}
	return out.Review, usage, written, "", out
}

// judgingRequest is the judging call on a shape that described separately.
// With a walkthrough in hand it asks for findings alone. Without one it is the
// whole review, the request Assemble built, so the run still gets a
// walkthrough: every call sends the same tools, so the fallback reads the
// prompt the failed describing call cached.
func (r *Result) judgingRequest(described bool) *Result {
	out := r.clone()
	if !described {
		out.Stage = StageReview
		out.Tail = judgingTail + describingTail + r.note
		return out
	}
	out.Stage = StageFindings
	out.Tail = judgingTail + r.note
	return out
}

// missingWalkthrough is the overview a review carries when its describing call
// failed, so the report says the walkthrough is missing rather than rendering
// a review with no summary as though it had nothing to say.
func missingWalkthrough(failed string) string {
	return "No walkthrough: the describing call did not produce one (" + failed +
		"). The comments below come from the judging call alone."
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
	res.Usage.ThinkingTokens += usage.ThinkingTokens
	res.SynopsisOutputTokens += written
	res.SynopsisFailed = failed
	res.recost(model)
	if failed != "" {
		if res.Review.Overview == "" {
			res.Review.Overview = missingWalkthrough(failed)
		}
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

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
