package review

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

// The staged pipeline: one call describes the change and partitions it, and
// one call per part judges its own part.
//
// What the fan-out buys is not a smaller input per call - every call carries
// the whole prefix, because that is what the cache is keyed on - but a smaller
// task per call. The failure it is aimed at is measured: a reviewer with fifty
// files in front of it spends its finding-count on the first few, and the
// per-file summaries and the findings share one output cap. Whether a smaller
// task raises recall is the question the arm exists to answer, not an
// assumption this code makes.
//
// Everything here degrades to the shape below it. A stage one that fails or
// comes back without a usable partition leaves the run on the one-shot
// contract; a partition with one cohort is the synopsis path exactly; cohort
// calls that fail are counted and the rest are merged, because a review over
// four cohorts of five is worth more than no review.

// Shapes a run is recorded under. None of them is a setting: Options.Shape
// reads them off Cohorts and Stepwise, and the ledger groups rows by them.
const (
	PipelineOneShot  = "oneshot"
	PipelineStaged   = "staged"
	PipelineStepwise = "stepwise"
)

// The fan-out's defaults.
//
// One cohort is no split: the describing call and one judging call. The split
// is off until a caller asks for it, because no measurement yet says it earns
// its extra calls. Three files is where partitioning starts to mean anything at
// all - below it the groups are smaller than the summaries describing them.
const (
	DefaultCohorts        = 1
	DefaultMinCohortFiles = 3
)

// Cohort is one group of files and what they do together, as stage one drew
// it. Summary is what the other cohorts' calls are shown of this one, so it
// is written for a reader who cannot see these diffs.
type Cohort struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Files   []string `json:"files"`
}

// cohortsRequest is stage one when the run fans out: the describing call's
// prefix and tail, under the contract that also carries the partition.
func (r *Result) cohortsRequest(opts Options, in Input) *Result {
	out := r.synopsisRequest(opts, in)
	// The bound the rest of the run enforces, not the raw flag. The tripwire
	// prices cohortBound, the progress line prints it and repairPartition
	// folds anything above it, so a stage one told a larger number spends
	// output on a partition that is then silently collapsed.
	out.Tail = cohortsTail(in, cohortBound(opts, in))
	out.InputEstimate = r.InputEstimate + envelope.EstimateTokens(out.Tail)
	out.CostUSD, out.CostKnown = EstimateCost(opts.Model, out.InputEstimate, ExpectedSynopsisTokens)
	out.CostCeilingUSD, _ = CeilingCost(opts.Model, out.InputEstimate, opts.MaxTokens)
	return out
}

// cohortRequest is one stage-two call: the same prefix as every other, scoped
// by its instruction to one cohort.
func (r *Result) cohortRequest(opts Options, mine Cohort, others []Cohort, mineIdx, bound int) *Result {
	out := r.clone()
	out.Stage = StageFindings
	out.Tail = cohortTail(mine, others, mineIdx, opts.CrossSummaries) + r.note
	out.InputEstimate = r.InputEstimate + envelope.EstimateTokens(out.Tail)
	out.CostUSD, out.CostKnown = EstimateCost(opts.Model, out.InputEstimate, ExpectedOutputTokens)
	out.CostCeilingUSD, _ = CeilingCost(opts.Model, out.InputEstimate, cohortMaxTokens(opts, bound))
	return out
}

// stagedCeilingCost is what the fan-out could cost at worst, for the tripwire
// that decides whether to send anything at all: stage one plus a call per
// cohort, at the upper bound the run is allowed to draw.
//
// The bound rather than the count, because the count is stage one's to choose
// and the tripwire refuses before stage one runs. A guard that priced one
// review and then paid for six is the failure it exists to prevent.
func stagedCeilingCost(opts Options, in Input, res *Result) float64 {
	if opts.Shape() != PipelineStaged || res == nil {
		return 0
	}
	one, ok := CeilingCost(opts.Model, res.cohortsRequest(opts, in).InputEstimate, opts.MaxTokens)
	if !ok {
		return 0
	}
	if opts.PlanOnly {
		// No cohort call is sent under --plan, so pricing the fan-out here
		// would refuse a run for calls it will never make. Stage one is the
		// whole bill.
		return one
	}
	bound := pricingCohortBound(opts, in)
	// Priced off a cohort request rather than the bare prefix: a cohort's
	// tail carries its file list and, with cross-summaries on, a line for
	// every other cohort, and undercounting it once per call is undercounting
	// the shape this guard exists to price. The cohort is representative -
	// the prefix dominates and the tails are within a few hundred tokens of
	// each other - and stage one has not run, so there is no real partition
	// to price against.
	sample := Cohort{Name: "cohort", Summary: strings.Repeat("x ", 20)}
	for path := range in.ShownFiles() {
		sample.Files = append(sample.Files, path)
	}
	peers := make([]Cohort, bound)
	for i := range peers {
		peers[i] = sample
	}
	each, _ := CeilingCost(opts.Model,
		res.cohortRequest(opts, sample, peers, 0, bound).InputEstimate,
		cohortMaxTokens(opts, bound))
	// Stage one is counted here, so the synopsis ceiling is not added on top
	// of it: this is the same call under a wider contract.
	return one + each*float64(bound)
}

// cohortMaxTokens is one cohort call's response cap: the whole run's cap
// divided across the fan-out.
//
// The budget is one review's, not one per cohort. Six calls at the full cap
// is six times a review's worst case, which on a mid-sized change is $6
// against a $2 tripwire - so the guard refused every real fixture in the
// first sweep, correctly, for a request nobody intended to make. A cohort
// reviewing two files does not need the cap a reviewer of fifty needs, and
// the run measured here spent 3,267 output tokens across five of them.
//
// Floored, because a cap small enough to truncate a cohort's findings would
// buy the guard by breaking the thing it guards.
func cohortMaxTokens(opts Options, cohorts int) int64 {
	if cohorts <= 1 {
		return opts.MaxTokens
	}
	per := opts.MaxTokens / int64(cohorts)
	if per < MinCohortMaxTokens {
		per = min(MinCohortMaxTokens, opts.MaxTokens)
	}
	return per
}

// MinCohortMaxTokens is the floor on a cohort call's response cap. Well above
// what a cohort of a handful of files was measured writing, so the division
// above bounds the bill without bounding the review.
const MinCohortMaxTokens int64 = 8000

// cohortBound is how many stage-two calls this run may make. A change with
// fewer shown files than MinCohortFiles is one cohort whatever the flag says:
// partitioning four files into six groups spends six calls to review four
// files and dilutes each one's context for nothing.
func cohortBound(opts Options, in Input) int {
	shown := len(in.ShownFiles())
	if shown < opts.MinCohortFiles || opts.Cohorts <= 1 {
		return 1
	}
	if opts.Cohorts > shown {
		return shown
	}
	return opts.Cohorts
}

// pricingCohortBound is cohortBound narrowed by OnlyCohorts, for the tripwire
// and its message. Stage one has not run when either reads this, so there is
// no real partition to count selector matches against; one call per
// selector is the same approximation cohortBound already makes for the
// unfiltered case, capped at the bound so a caller cannot buy more by naming
// more selectors than the partition could ever hold.
func pricingCohortBound(opts Options, in Input) int {
	bound := cohortBound(opts, in)
	if len(opts.OnlyCohorts) > 0 && len(opts.OnlyCohorts) < bound {
		return len(opts.OnlyCohorts)
	}
	return bound
}

// runStaged is the pipeline: describe and partition, then judge per cohort.
//
// It returns a Result the rest of Run does not have to know is staged: the
// union of the cohorts' findings with stage one's walkthrough on it, which is
// what Verify reads and what the report renders.
func runStaged(ctx context.Context, in Input, opts Options, res *Result) (*Result, error) {
	bound := cohortBound(opts, in)
	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("describing the change and splitting it into at most %d cohort(s)", bound))
	}
	one, err := runOnce(ctx, in, opts, res.cohortsRequest(opts, in))
	walkthrough, usage, written, failed := describedBy(one, err)
	if failed != "" {
		// Stage one is the call the whole shape depends on: it writes the
		// cache the fan-out reads and the partition the fan-out is drawn
		// from. Without it this is a one-shot review that has already paid
		// for a failed call, which is worse than a one-shot review and much
		// better than no review.
		if opts.Progress != nil {
			opts.Progress("the describing call did not produce a walkthrough (" + failed +
				"); this review is one call for findings alone")
		}
		// One judging call over the whole change, under the same tools the
		// failed call sent, so it reads the prefix that call wrote.
		out, rerr := runOnce(ctx, in, opts, res.judgingRequest(false))
		// Stage one was billed whether or not it answered, and the write it
		// made over the whole prefix is the expensive half. Folding it in
		// here is what stops a run that paid for two calls from joining the
		// one-shot distribution at one call's price - FellBack labels the
		// row, and without this the number on it is still wrong.
		applySynopsis(out, opts.Model, findings.Review{}, usage, written, failed)
		if out != nil {
			out.Pipeline = PipelineOneShot
			out.FellBack = failed
		}
		return out, rerr
	}
	cohorts, note := repairPartition(in.ShownFiles(), one.Cohorts, bound)
	if note != "" && opts.Progress != nil {
		opts.Progress("the partition needed repair: " + note)
	}
	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("described %d file(s) in %d cohort(s): %s",
			len(walkthrough.Files), len(cohorts), cohortNames(cohorts)))
	}
	if len(opts.OnlyCohorts) > 0 {
		selected, serr := selectCohorts(cohorts, opts.OnlyCohorts)
		if serr != nil {
			return nil, serr
		}
		cohorts = selected
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf("restricted to %d cohort(s): %s",
				len(cohorts), cohortNames(cohorts)))
		}
	}
	if opts.PlanOnly {
		// Stop here: the partition is drawn and stage one is paid for, but
		// no cohort call goes out. Comments is empty because nothing judged
		// the change, which is what PlanOnly on the result says so Summary
		// does not read it as a clean review.
		out := res.clone()
		applySynopsis(out, opts.Model, walkthrough, usage, written, "")
		out.Pipeline = PipelineStaged
		out.Cohorts = cohorts
		out.PlanOnly = true
		return out, nil
	}

	merged, err := fanOut(ctx, in, opts, res, cohorts)
	applySynopsis(merged, opts.Model, walkthrough, usage, written, "")
	if merged != nil {
		merged.Pipeline = PipelineStaged
		merged.Cohorts = cohorts
	}
	return merged, err
}

// describedBy reads stage one's answer the way describe does, so the staged
// path and the synopsis path agree on what counts as a walkthrough and on
// which tokens belong to the judging call's median.
func describedBy(one *Result, err error) (findings.Review, Usage, int64, string) {
	if one == nil {
		return findings.Review{}, Usage{}, 0, "the describing call returned nothing"
	}
	usage, written := one.Usage, one.Usage.OutputTokens
	usage.OutputTokens = 0
	switch {
	case err != nil:
		return findings.Review{}, usage, written, err.Error()
	case one.Review.Overview == "":
		return findings.Review{}, usage, written, "the describing call returned no overview"
	}
	return one.Review, usage, written, ""
}

// fanOut runs one judging call per cohort, together, and unions what they
// found. The calls share no state, so the money scales with the cohort count
// and the wall clock does not.
func fanOut(ctx context.Context, in Input, opts Options, res *Result, cohorts []Cohort) (*Result, error) {
	type answer struct {
		res *Result
		err error
	}
	out := make([]answer, len(cohorts))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var landed int
	for i, cohort := range cohorts {
		wg.Add(1)
		go func(i int, cohort Cohort) {
			defer wg.Done()
			// The run's response budget, divided. Each call sends the cap it
			// was priced at, so the tripwire's arithmetic and the wire's
			// request are the same number.
			one := opts
			one.Samples = 1
			one.MaxTokens = cohortMaxTokens(opts, len(cohorts))
			got, err := runOnce(ctx, in, one, res.cohortRequest(opts, cohort, cohorts, i, len(cohorts)))
			out[i] = answer{res: got, err: err}
			if opts.Progress == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			landed++
			if err != nil {
				opts.Progress(fmt.Sprintf("cohort %d of %d (%s) failed: %v",
					landed, len(cohorts), cohort.Name, err))
				return
			}
			opts.Progress(fmt.Sprintf("cohort %d of %d (%s): %d finding(s), %s",
				landed, len(cohorts), cohort.Name, len(got.Review.Comments),
				FormatCost(got.CostUSD, got.CostKnown)))
		}(i, cohort)
	}
	wg.Wait()

	merged := res.clone()
	// The breakpoint was this run's, whichever call wrote it. Read off the
	// options rather than off a cohort's result because the merged row is
	// what the ledger records, and a run that read 366,000 tokens back with
	// a zero in that column reads as a run that never asked for one.
	merged.Cached = opts.cacheOn()
	var kept []*Result
	var failures []string
	for _, a := range out {
		if a.res != nil {
			merged.Usage.InputTokens += a.res.Usage.InputTokens
			merged.Usage.OutputTokens += a.res.Usage.OutputTokens
			merged.Usage.CacheReadTokens += a.res.Usage.CacheReadTokens
			merged.Usage.CacheWriteTokens += a.res.Usage.CacheWriteTokens
			merged.Usage.ThinkingTokens += a.res.Usage.ThinkingTokens
			if a.res.Duration > merged.Duration {
				merged.Duration = a.res.Duration
			}
		}
		if a.err != nil {
			failures = append(failures, a.err.Error())
			continue
		}
		merged.Stubs += a.res.Stubs
		kept = append(kept, a.res)
	}
	merged.recost(opts.Model)
	merged.CohortsFailed = len(failures)
	merged.Turns = len(kept)
	// An empty partition is not an empty failure list: a change whose shown
	// set is empty - a test-only change with tests held back - leaves
	// repairPartition with nothing to place, and indexing failures[0] on the
	// way out is a panic rather than an error.
	if len(cohorts) == 0 {
		return merged, fmt.Errorf("stage one drew no cohort covering any shown file")
	}
	if len(kept) == 0 {
		return merged, fmt.Errorf("all %d cohort call(s) failed: %s", len(cohorts), failures[0])
	}
	// The same union the samples take, by the same key: two cohorts that both
	// reach for a file on the boundary between them are describing one defect,
	// and a reader of the merged review must see it once.
	merged.Review = unionReviews(kept)
	// A truncation anywhere wins. kept[0] is whichever cohort landed first,
	// and the fan-out makes truncation more likely rather than less - each
	// call runs against a divided cap - so reading one slot would hide the
	// column the ledger uses to tell a cheap run from a wasted one.
	merged.StopReason = kept[0].StopReason
	// Every cohort goes to the same model, so the first one's answer names it.
	merged.ServedModel = kept[0].ServedModel
	for _, k := range kept {
		if k.Truncated {
			merged.StopReason, merged.Truncated = k.StopReason, true
			break
		}
	}
	return merged, nil
}

// repairPartition makes what stage one returned usable, and says what it had
// to change.
//
// A partition is repaired rather than rejected because the alternative is
// throwing away a paid call over a file in two groups. Every shown file ends
// in exactly one cohort: the first that named it, or the first cohort if
// nothing did. Files that were never shown are dropped, cohorts left empty are
// dropped, and a partition with nothing in it is one cohort of everything.
func repairPartition(shown map[string]bool, cohorts []Cohort, bound int) ([]Cohort, string) {
	var notes []string
	taken := map[string]bool{}
	var kept []Cohort
	for _, c := range cohorts {
		var files []string
		for _, path := range c.Files {
			switch {
			case !shown[path]:
				notes = append(notes, path+" was not shown")
			case taken[path]:
				notes = append(notes, path+" was named twice")
			default:
				taken[path] = true
				files = append(files, path)
			}
		}
		if len(files) == 0 {
			continue
		}
		c.Files = files
		kept = append(kept, c)
	}
	if len(kept) > bound {
		// Over the bound the run was priced for. The tail folds into the
		// last cohort inside it rather than being dropped: the files still
		// have to be reviewed by somebody.
		for _, c := range kept[bound:] {
			kept[bound-1].Files = append(kept[bound-1].Files, c.Files...)
		}
		notes = append(notes, fmt.Sprintf("%d cohorts over the bound of %d were folded into the last",
			len(kept)-bound, bound))
		kept = kept[:bound]
	}
	var missed []string
	for path := range shown {
		if !taken[path] {
			missed = append(missed, path)
		}
	}
	sort.Strings(missed)
	if len(missed) > 0 {
		if len(kept) == 0 {
			kept = []Cohort{{Name: "the change", Summary: "every file in this change."}}
		}
		kept[0].Files = append(kept[0].Files, missed...)
		notes = append(notes, fmt.Sprintf("%d shown file(s) were in no cohort", len(missed)))
	}
	if len(notes) > 3 {
		notes = append(notes[:3], fmt.Sprintf("and %d more", len(notes)-3))
	}
	return kept, strings.Join(notes, ", ")
}

// selectCohorts narrows a drawn partition to the ones a caller asked for by
// position, by name, or by a file inside one, so a run can pay for one
// cohort's judgment after --plan showed what stage one would draw.
//
// A selector is a 1-based index into cohorts as printed, or a
// case-insensitive substring tried first against every cohort's name and,
// only where that matches nothing, against the files inside each one. The
// name is stage one's to word fresh on every run - two calls over the same
// change can describe it in different words - so a selector kept from an
// earlier plan is chasing wording that may already have moved. A file path
// does not move, which is what makes it the fallback rather than the first
// try: naming a phrase from the summary is what a caller remembers, and
// naming a file is what still works when the summary said something else
// this time. A selector matching nothing is an error naming the selector,
// not a silent empty result: a partition of zero cohorts reads as a change
// with nothing to review, and it would be a filter that missed rather than
// a change that is clean.
func selectCohorts(cohorts []Cohort, selectors []string) ([]Cohort, error) {
	var out []Cohort
	seen := map[int]bool{}
	for _, sel := range selectors {
		sel = strings.TrimSpace(sel)
		if sel == "" {
			continue
		}
		matched := false
		if n, err := strconv.Atoi(sel); err == nil {
			if n >= 1 && n <= len(cohorts) {
				idx := n - 1
				if !seen[idx] {
					seen[idx] = true
					out = append(out, cohorts[idx])
				}
				matched = true
			}
		} else {
			// Falls through to a file path when no cohort name matches,
			// because the name is stage one's to word fresh on every run and
			// a selector kept from an earlier plan is chasing wording that
			// may already have changed; a file path is the one thing about
			// a cohort that does not move between runs.
			needle := strings.ToLower(sel)
			for i, c := range cohorts {
				if strings.Contains(strings.ToLower(c.Name), needle) {
					if !seen[i] {
						seen[i] = true
						out = append(out, cohorts[i])
					}
					matched = true
				}
			}
			if !matched {
				for i, c := range cohorts {
					for _, f := range c.Files {
						if strings.Contains(strings.ToLower(f), needle) {
							if !seen[i] {
								seen[i] = true
								out = append(out, cohorts[i])
							}
							matched = true
							break
						}
					}
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf(
				"--only-cohorts %q matched no cohort: stage one drew %s",
				sel, cohortNames(cohorts))
		}
	}
	return out, nil
}

func cohortNames(cohorts []Cohort) string {
	names := make([]string, 0, len(cohorts))
	for _, c := range cohorts {
		names = append(names, fmt.Sprintf("%s (%d)", c.Name, len(c.Files)))
	}
	return strings.Join(names, ", ")
}
