package review

import (
	"context"
	"fmt"
	"sort"
	"strings"
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

// described is what the describing stage produced.
//
// A struct rather than the five return values this used to be, because the
// model it ran on is a sixth and the cost priced at that model is a seventh,
// and a caller unpacking seven positional values gets two of them the wrong
// way round eventually.
type described struct {
	// Walkthrough is the overview and the line per file, empty when Failed.
	Walkthrough findings.Review
	// Usage is what the call consumed, with the output count zeroed out and
	// moved to Written. Priced at Model, which is not always the judging
	// call's, so whether this can be folded into the review's own Usage is
	// applySynopsis's decision and not this struct's.
	Usage   Usage
	Written int64
	// Model is the model that answered, and CostUSD what the call cost at
	// that model's rates. CostKnown is false for a model with no entry in the
	// price table, where the cost has to read as unknown rather than as zero.
	Model     string
	CostUSD   float64
	CostKnown bool
	// Failed is why there is no walkthrough, empty when there is one.
	Failed string
	// Result is the describing call's own result, for the turn counts and
	// lookups the judging result folds in.
	Result *Result
}

// synopsisRequest is the describing call, assembled from the review's own
// prefix. Everything ahead of the tail is stage one's bytes exactly.
//
// opts is the describing call's own Options, not the review's: the estimate
// and the ceiling below price this call at the model it will actually go out
// on. See Options.describing.
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
	opts = opts.describing()
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
//
// opts is the review's, and the describing endpoint is resolved here rather
// than by the caller, so there is one place that decides which wire this call
// goes out on and one place that knows the answer has to be priced there too.
func describe(ctx context.Context, in Input, opts Options, res *Result) described {
	opts = opts.describing()
	if opts.Progress != nil {
		msg := "describing the change before judging it"
		if opts.Describing.set() {
			msg += ", on " + opts.Model
		}
		opts.Progress(msg)
	}
	out, err := runOnce(ctx, in, opts, res.synopsisRequest(opts, in))
	d := described{
		Usage: out.Usage, Written: out.Usage.OutputTokens, Model: opts.Model,
		// Priced against the model this call was made on, here, while that is
		// still known. runOnce has already done the same arithmetic onto
		// out.CostUSD; it is read back rather than recomputed so a run whose
		// usage was estimated rather than reported carries the same number in
		// both places.
		CostUSD: out.CostUSD, CostKnown: out.CostKnown,
		Result: out,
	}
	// The describing call's output is not the review's output. Usage carries
	// input, which is real either way; the output count is kept apart so
	// the ledger's median keeps meaning what a judging call writes.
	d.Usage.OutputTokens = 0
	switch {
	case err != nil:
		d.Failed = err.Error()
		return d
	case out.Review.Overview == "":
		d.Failed = "the describing call returned no overview"
		return d
	}
	d.Walkthrough = out.Review
	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("described %d file(s) in %s",
			len(out.Review.Files), out.Duration.Round(time.Second)))
	}
	return d
}

// judgingRequest is the judging call on a shape that described separately.
// With a walkthrough in hand it asks for findings alone. Without one it is the
// whole review, the request Assemble built, so the run still gets a
// walkthrough: every call sends the same tools, so the fallback reads the
// prompt the failed describing call cached.
//
// The walkthrough itself rides in the tail. Telling this pass that one exists
// has been tried and does not hold: the prompt block carried "the overview is
// already written" and the pass wrote it anyway, and on PR #1462 a pass told
// only that it takes add_comment spent a turn on an overview and twelve file
// lines, all refused. Both times the pass was asked to believe in something it
// could not see, while the catalogue in front of it still offered the tools to
// make one. Showing it the walkthrough answers that: the work is visibly done.
// It goes behind the cache breakpoint because it is this run's own output and
// cannot be in the block every call reads back.
func (r *Result) judgingRequest(walkthrough findings.Review, described bool) *Result {
	out := r.clone()
	if !described {
		out.Stage = StageReview
		out.Tail = judgingTail + describingTail + r.note
		return out
	}
	out.Stage = StageFindings
	out.Tail = walkthroughTail(walkthrough) + judgingTail + r.note
	return out
}

// walkthroughTail is the describing call's own output, handed to the pass that
// judges. Material, so it comes before the instruction the pass acts on.
//
// The file lines are the reason this is worth its tokens beyond stopping the
// rewrite: they are a reading of every shown file, including the ones a
// judging pass would skim, written by a call over the same material that was
// told to describe and not to judge.
func walkthroughTail(w findings.Review) string {
	if strings.TrimSpace(w.Overview) == "" && len(w.Files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## The walkthrough, already written\n\n")
	b.WriteString("An earlier call over this same material wrote what follows. " +
		"It is here so that you do not write it again, and so that you can see what that call " +
		"made of each file before you judge it.\n\n")
	if s := strings.TrimSpace(w.Overview); s != "" {
		b.WriteString(s + "\n\n")
	}
	// Sorted, because Files is a map and a tail that reordered between two
	// runs of one change would be a different request for no reason.
	paths := make([]string, 0, len(w.Files))
	for path := range w.Files {
		if strings.TrimSpace(path) != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		fmt.Fprintf(&b, "- %s: %s\n", path, strings.TrimSpace(w.Files[path]))
	}
	return b.String()
}

// missingWalkthrough is the overview a review carries when its describing call
// failed, so the report says the walkthrough is missing rather than rendering
// a review with no summary as though it had nothing to say.
func missingWalkthrough(failed string) string {
	return "No walkthrough: the describing call did not produce one (" + failed +
		"). The comments below come from the judging call alone."
}

// applySynopsis puts the walkthrough on the review the judging call wrote, and
// records which call it came from and what that call cost.
//
// model is the judging call's. Where the describing call ran on that same
// model its tokens join Usage and are priced with everything else, which is
// what every review did before a second model was reachable. Where it ran on
// another one they cannot: Usage is a token count with no model attached, and
// one record cannot carry two rate cards. Those tokens stay out of it and the
// cost the describing call was already priced at is carried across instead.
func applySynopsis(res *Result, model string, d described) {
	if res == nil {
		return
	}
	res.SynopsisModel = d.Model
	if res.synopsisPricedApart(model) {
		res.SynopsisCostUSD = d.CostUSD
	} else {
		res.Usage.InputTokens += d.Usage.InputTokens
		res.Usage.CacheReadTokens += d.Usage.CacheReadTokens
		res.Usage.CacheWriteTokens += d.Usage.CacheWriteTokens
		res.Usage.ThinkingTokens += d.Usage.ThinkingTokens
	}
	res.SynopsisOutputTokens += d.Written
	res.SynopsisFailed = d.Failed
	res.recost(model)
	if d.Failed != "" {
		if res.Review.Overview == "" {
			res.Review.Overview = missingWalkthrough(d.Failed)
		}
		return
	}
	res.Synopsis = true
	res.Review.Overview = d.Walkthrough.Overview
	res.Review.Files = d.Walkthrough.Files
}

// synopsisPricedApart reports whether the describing call's cost is already a
// number in SynopsisCostUSD rather than tokens in Usage waiting to be priced.
//
// True exactly when that call ran on a model other than the judging one,
// which is the only case where pricing its tokens at the judging model's rate
// would be wrong. Derived from the two model names rather than stored as its
// own flag, so there is no second copy of the fact to fall out of step.
func (r *Result) synopsisPricedApart(model string) bool {
	return r != nil && r.SynopsisModel != "" && r.SynopsisModel != model
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
	apart := Usage{OutputTokens: r.RulingOutputTokens}
	if !r.synopsisPricedApart(model) {
		apart.OutputTokens += r.SynopsisOutputTokens
	}
	if extra, ok := apart.Cost(model); ok {
		r.CostUSD += extra
	}
	if r.synopsisPricedApart(model) {
		// A model with no entry in the price table prices as unknown, and a
		// total missing one of its two calls has to say so. Asked of the
		// table rather than carried on the result, so the one place that
		// knows what is priced is the only place that decides.
		if _, ok := LookupPricing(r.SynopsisModel); !ok {
			r.CostKnown = false
		}
		r.CostUSD += r.SynopsisCostUSD
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
