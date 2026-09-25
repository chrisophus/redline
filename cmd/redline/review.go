package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/scout"
)

// cmdReview is the one command in Redline that calls a model.
//
// It reviews a saved session. With --run it observes the change and saves the
// session first, then reviews the saved file like any other, which is what
// keeps it a pure function of its inputs: the same
// session reviewed twice sees exactly the same material, and a frozen session
// is a fixture the eval can replay. It also means `run` stays model-free, and
// a repository with no API key still gets the whole report.
func cmdReview(o opts) error {
	// Read before anything is observed or sent, so a note that cannot be read
	// is refused before it costs a run.
	note, err := reviewNote(o)
	if err != nil {
		return err
	}
	o.note, o.noteFile = note, ""
	if o.observe {
		if o.stats {
			return fmt.Errorf("--stats reads the cost ledger and observes nothing; drop --run")
		}
		res, runErr := execute(o)
		if res == nil {
			return runErr
		}
		if err := write(o, res); err != nil {
			return err
		}
		recordSession(o)
		if runErr != nil {
			return runErr
		}
	}
	if o.stats {
		entries, err := review.ReadLedger(o.ledgerDir())
		if err != nil {
			return err
		}
		// One block per shape, never a mean across them: a staged run is a
		// call per cohort and a one-shot run is one call, so an average over
		// both is a price nobody was charged.
		byShape := review.SummarizeByShape(entries)
		// Stepwise is gone as a shape, but rows it wrote are still printed apart.
		for _, shape := range []string{review.PipelineOneShot, review.PipelineStaged, review.PipelineStepwise} {
			if s, ok := byShape[shape]; ok {
				fmt.Println(s.String())
			}
		}
		if len(byShape) == 0 {
			fmt.Println(review.Summarize(nil).String())
		}
		return nil
	}
	res, err := run.LoadSession(o.out)
	if err != nil {
		return err
	}
	in := review.Input{
		Report:    &res.Report,
		Change:    res.Change,
		Envelopes: res.Envelopes,
		Absent:    res.ContextAbsent,
		// The profile is already read and saved with the session; the review
		// is the last consumer that had no access to it.
		LineCoverage: res.LineCoverage,
		Prior:        res.PriorReview,
		Note:         note,
	}
	// Price the estimate against what this installation's reviews actually
	// emit, when it has emitted any.
	var expected int64
	if entries, lerr := review.ReadLedger(o.ledgerDir()); lerr == nil {
		// Priced against this shape's own rows. A staged row's output is a
		// describing call plus one per cohort, and using it to price a
		// one-shot call would quote several calls for one.
		shape := review.Options{Cohorts: o.cohorts}.Shape()
		expected = review.SummarizeByShape(entries)[shape].ExpectedOutput()
	}
	ropts := review.Options{
		DeferContext:   o.deferContext,
		API:            o.api,
		BaseURL:        o.baseURL,
		Model:          o.model,
		Effort:         o.effort,
		Ceiling:        o.ceiling,
		MaxTokens:      int64(o.maxTokens),
		MaxCostUSD:     o.maxCost,
		Mode:           o.mode,
		MaxTurns:       o.maxTurns,
		CallTurns:      o.callTurns,
		Samples:        o.samples,
		ExpectedOutput: expected,
		DryRun:         o.dryRun,
	}
	// The checking pass is off unless it is asked for. No eval arm scores it,
	// so the scout's five lookup turns and the ruling that reads them would be
	// unmeasured cost on every review, and the one trace that exists points
	// the wrong way: on this repository's PR #46, nine of the ten findings
	// that never reached the reader came back unverifiable from the ruling,
	// six of those on questions the review had certified itself. A pass whose
	// main output is withholding is the wrong trade for a reviewer meant to
	// say more. --verify turns it on.
	ropts.Verify = o.verify && o.mode != review.ModeExplore
	// The breakpoint is on unless it is turned off, for the same shape of
	// reason: a run that pays two full-rate calls over one prefix is paying
	// for nothing, and the measured saving on the pair is about a quarter of
	// the input. --no-cache is there for a caller measuring against the
	// uncached behaviour. The library refuses it where it cannot pay -
	// the OpenAI wire, several samples, the batch tier - so this flag only
	// sets the policy; the library still works out the arithmetic on its own.
	ropts.Cache = !o.noCache
	switch o.cacheTTL {
	case "", review.CacheTTL5m, review.CacheTTL1h:
		ropts.CacheTTL = o.cacheTTL
	default:
		return fmt.Errorf("--cache-ttl is %s or %s, not %q", review.CacheTTL5m, review.CacheTTL1h, o.cacheTTL)
	}
	// The describing stage runs on every review that can have one.
	//
	// What decided it: across about 400 stored samples, replies that skipped
	// the walkthrough entirely - a junk overview, no file lines, findings only
	// - ran at 23% to 40% on every packet above 34k tokens and at 0% to 7% on
	// the six small ones. The sweep never showed it, because it scores the
	// union of three samples and one sample writing the walkthrough hides two
	// that did not. A default run takes one sample.
	//
	// The second call is most of a call's input read back from cache at a
	// tenth of base rate, and it buys a walkthrough that covers every shown
	// file and a judging call whose output cap is not shared with fifty file
	// summaries. --no-synopsis is the way back to one call.
	ropts.Synopsis = !o.noSynopsis
	// Both work on the partition, and one cohort draws none.
	if o.planOnly && o.cohorts <= 1 {
		return fmt.Errorf("--plan stops before the cohort calls a split sends, and without --cohorts above 1 there is no split; pass --cohorts N")
	}
	ropts.PlanOnly = o.planOnly
	if o.onlyCohorts != "" {
		if o.cohorts <= 1 {
			return fmt.Errorf("--only-cohorts selects among the cohorts a split draws, and without --cohorts above 1 there is no split; pass --cohorts N")
		}
		for _, sel := range strings.Split(o.onlyCohorts, ",") {
			if sel = strings.TrimSpace(sel); sel != "" {
				ropts.OnlyCohorts = append(ropts.OnlyCohorts, sel)
			}
		}
	}
	ropts.Cohorts = o.cohorts
	ropts.MinCohortFiles = o.minCohortFiles
	if o.cohortContext && o.cohorts <= 1 {
		return fmt.Errorf("--cohort-context scopes context to a cohort's own files, and without --cohorts above 1 there is no split to scope by; pass --cohorts N")
	}
	ropts.CohortContext = o.cohortContext
	if o.reuseSynopsis {
		if o.noSynopsis {
			return fmt.Errorf("--reuse-synopsis reuses a walkthrough and --no-synopsis asks for none; pass one or the other")
		}
		path := filepath.Join(o.out, "review.json")
		rev, rerr := findings.LoadReview(path)
		if rerr != nil {
			return fmt.Errorf("--reuse-synopsis: %w", rerr)
		}
		if rev == nil {
			return fmt.Errorf("--reuse-synopsis: %s does not exist; run once without --reuse-synopsis first", path)
		}
		if want := change.ReviewIdentity(res.Report.BaseSHA, res.Change); rev.Revision != want {
			return fmt.Errorf("--reuse-synopsis: %s was written against %s and this change is %s; re-run without --reuse-synopsis",
				path, rev.Revision, want)
		}
		if rev.Overview == "" {
			return fmt.Errorf("--reuse-synopsis: %s has no walkthrough to reuse (its describing call did not produce one)", path)
		}
		if o.cohorts > 1 && len(rev.Cohorts) == 0 {
			return fmt.Errorf("--reuse-synopsis: %s has no partition to reuse for --cohorts %d (it was written by a one-shot review); pass --cohorts 1 or drop --reuse-synopsis",
				path, o.cohorts)
		}
		ropts.ReuseSynopsis = rev
	}
	// On unless the off flag is given, the way the cache is: the summaries
	// are what a cohort call knows about its neighbours, and a fan-out with
	// none of them gives up every cross-cohort correlation from the cohort
	// side. Turning them off is for measuring that arm; it is not the default.
	ropts.CrossSummaries = !o.noCrossSummaries
	// On the OpenAI wire the credentials are read here, in the vendor's own
	// env names, before the checking pass is wired up: the scout that runs
	// inside it now goes over the same wire and needs them. The flag wins over
	// the variable.
	if o.api == review.APIOpenAI {
		ropts.APIKey = os.Getenv("OPENAI_API_KEY")
		if ropts.BaseURL == "" {
			ropts.BaseURL = os.Getenv("OPENAI_BASE_URL")
		}
		ropts.APIUser = o.apiUser
		if ropts.APIUser == "" {
			ropts.APIUser = os.Getenv("OPENAI_USER")
		}
	}
	ropts.Describing = o.describingEndpoint(ropts)
	if o.since != "" {
		since, sinceFiles, serr := o.sinceLastReview(res)
		if serr != nil {
			return serr
		}
		ropts.SinceReview, ropts.SinceFiles = since, sinceFiles
	}
	// The scout's spend is accumulated here so a cost paid inside the verify
	// pass reaches the ledger and not only its own log line.
	tally := scoutTally{known: true}
	if !o.dryRun {
		// Progress, verbose and debug are wired before the answerer is,
		// because it captures ropts and threads all three into the lookup
		// loop. Without this the scout would run silent even with --verbose
		// on.
		ropts.Progress = func(msg string) { fmt.Fprintln(os.Stderr, "redline:", msg) }
		// --verbose is what a person watching the run sees; --debug is what
		// the run leaves behind for someone to read afterward. Independent
		// on purpose: a CI run wants --debug on (the artifact is the only
		// record once the job ends) and --verbose off (its own log already
		// has stdout/stderr, so the same lines twice would just be noise).
		ropts.Debug = o.verboseSink()
		if o.debug || os.Getenv("REDLINE_DEBUG") != "" {
			// Its own subdirectory per run, named by when the run started, so
			// a rerun against the same --out never destroys what the last one
			// wrote. The old flat layout numbered files from 1 on every
			// invocation: two runs sharing a stage name overwrote each
			// other's capture, and two runs with different shapes (a split
			// review's "synopsis"/"findings" beside a one-shot's "review")
			// left half the directory stale and half fresh with no way to
			// tell which was which.
			dir := filepath.Join(o.out, "debug", time.Now().UTC().Format("20060102T150405Z"))
			if err := os.MkdirAll(dir, 0o755); err == nil {
				var mu sync.Mutex
				var seq int
				ropts.Capture = func(name string, data []byte) {
					mu.Lock()
					seq++
					n := seq
					mu.Unlock()
					p := filepath.Join(dir, fmt.Sprintf("%02d-%s", n, name))
					if err := os.WriteFile(p, data, 0o644); err != nil {
						fmt.Fprintf(os.Stderr, "redline: could not write %s: %v\n", p, err)
					}
				}
				fmt.Fprintf(os.Stderr, "redline: writing the full LLM requests and responses to %s/\n", dir)
			}
		}
	}
	if ropts.Verify {
		ropts.Answer = scoutAnswerer(res, ropts, o.scoutSettings(), &tally)
	}
	// On unless it is turned off. A reviewer that pulls what it needs beats
	// one working from a guess made in advance about what it would want, and
	// a claim it can check while it writes is one that does not have to
	// survive a pass whose main output is withholding.
	//
	// It costs turns: three runs on one pull request put the inline shape at
	// 4.7x a plain review (see Result.Looked, printed as looked=N). That is
	// a matter of price, not a sign the shape doesn't work, and --max-cost is
	// the control for price. --no-look is there for a review that has to cost what a
	// plain one costs, and for measuring against the shape without them.
	if !o.noLook {
		ropts.Look = lookerFor(res)
	}
	if !o.dryRun {
		// Said before the call, not after it. A review is one blocking
		// request of two to five minutes and the command printed nothing
		// until it returned, so a working run and a hung one looked
		// identical. Assemble is what --dry-run does and calls nothing, so
		// the price quoted here is the price about to be paid.
		if est, aerr := review.Assemble(in, ropts); aerr == nil {
			// est carries the resolved model and wire, since Assemble is what
			// applies the defaults; this function only reports them.
			fmt.Fprintf(os.Stderr, "redline: reviewing %s with %s (%s), %d input tokens, expect %s, at most %s\n",
				describeSession(res), est.Model, est.API, est.InputEstimate,
				review.FormatCost(est.CostUSD, est.CostKnown),
				review.FormatCost(est.CostCeilingUSD, est.CostKnown))
			// Named separately when there is a second model, because the line
			// above quotes one model's price for a run that will pay two.
			if d := ropts.DescribingEndpoint(); d.Model != est.Model {
				fmt.Fprintf(os.Stderr, "redline: the walkthrough is written by %s (%s), billed apart\n",
					d.Model, d.API)
			}
			fmt.Fprintf(os.Stderr, "redline: request by part: %s\n", est.PartsLine())
		}
	}

	out, err := review.Run(context.Background(), in, ropts)
	if out != nil && tally.ran {
		// The lookups were a separate call on a separate model. Their cost is
		// recorded apart and folded into the total, so --stats and the ledger
		// count what the review actually cost end to end.
		out.ScoutCostUSD = tally.cost
		if out.CostKnown && tally.known {
			out.CostUSD += tally.cost
		}
	}
	if out != nil {
		if s := out.Budget.Summary(); s != "" {
			fmt.Fprintln(os.Stderr, "redline:", s)
		}
		if out.OverCeiling {
			// Said rather than silently absorbed. The context is what pays
			// for correlation findings, and a review that got none of it is
			// a different review from one that did.
			fmt.Fprintf(os.Stderr,
				"redline: the diff and findings alone are %d tokens against a %d ceiling, "+
					"so no context beyond the diff fits. Review a smaller range, or raise --ceiling.\n",
				out.FixedEstimate, out.Ceiling)
		}
		if out.UsageEstimated {
			// A ledger line built from an estimate is better than none, and
			// worse than one the endpoint counted; say which this is.
			fmt.Fprintf(os.Stderr,
				"redline: the endpoint reported no token usage, so the cost below is estimated "+
					"from the request size rather than counted.\n")
		}
		if !out.CostKnown && !o.dryRun && out.Usage.InputTokens > 0 {
			// A model missing from the price table has no estimate, so the
			// --max-cost tripwire in review.Run cannot fire and the request
			// went out with no cost guard at all. Silence here reads as a
			// priced request that came in under the cap.
			fmt.Fprintf(os.Stderr,
				"redline: %s is not in the price table, so the request was sent unpriced "+
					"and the --max-cost tripwire did not apply.\n", out.Model)
		}
	}
	// Record before returning the error, not after the review succeeds.
	// review.Run fills in Usage and CostUSD before it reports a truncated,
	// refused, or unparseable response, so by the time those errors surface
	// the money is already spent; returning here with no ledger line is how
	// two paid calls left reviews.jsonl untouched and --stats claiming one
	// review. The usage guard is what separates a request that was actually
	// sent from one that never left — a dry run, or a refusal before the
	// call — so those still write nothing.
	if out != nil && !o.dryRun && out.Usage.InputTokens > 0 {
		if rerr := review.Record(o.ledgerDir(), out); rerr != nil {
			// Not fatal. A review that produced findings has done its job,
			// and losing a cost line is not worth failing the command over.
			fmt.Fprintf(os.Stderr, "redline: could not record the run's cost: %v\n", rerr)
		}
		// Written here for the same reason and under the same guard: a review
		// that came back truncated or unparseable is the one someone is most
		// likely to go looking at afterwards, and it is about to return an
		// error a few lines down.
		if rerr := writeTrace(o, res, out, &tally); rerr != nil {
			fmt.Fprintf(os.Stderr, "redline: could not record what the review did: %v\n", rerr)
		}
	}
	if err != nil {
		return err
	}
	if o.dryRun {
		// Print the whole request rather than a summary of it, both halves
		// of it. The system block carries every provider's promptFragment,
		// which comes from another repository entirely and decides as much
		// about the review as the user turn does; a dry run that showed only
		// the user turn could not answer what would be sent. Then the
		// estimated price of sending it.
		fmt.Println("--- system ---")
		fmt.Println(out.System)
		fmt.Println("--- prompt ---")
		fmt.Print(out.Prompt)
		// The stage's own instruction rides after the packet, in its own
		// block, and it is the half that says what this call is for. A dry run
		// that stopped at the prompt would show the material and not the
		// instruction the model acts on.
		if out.Tail != "" {
			fmt.Println("\n--- this pass ---")
			fmt.Print(out.Tail)
		}
		fmt.Fprintf(os.Stderr, "\nredline: %d input tokens estimated; expect %s, at most %s\n",
			out.InputEstimate,
			review.FormatCost(out.CostUSD, out.CostKnown),
			review.FormatCost(out.CostCeilingUSD, out.CostKnown))
		if line := out.PartsLine(); line != "" {
			fmt.Fprintf(os.Stderr, "redline: request by part: %s\n", line)
		}
		return nil
	}
	fmt.Fprintln(os.Stderr, "redline: review", out.Summary())
	if out.Verified {
		// What was checked and what survived, because a reader told a review
		// found nine things and shown two needs to know the other seven were
		// ruled on rather than lost.
		kept, ruled := review.Kept(out.Review)
		fmt.Fprintf(os.Stderr, "redline: checked %d finding(s), kept %d\n", ruled, kept)
		if out.VerifyFailed != "" {
			fmt.Fprintf(os.Stderr,
				"redline: the checking pass did not complete (%s), so the findings below are "+
					"as the review wrote them and none of them was checked\n", out.VerifyFailed)
		}
	}
	// Turns counts samples too, so this line has to name the mode it is
	// about: a three-sample one-shot review has no turns and fetched
	// nothing.
	if out.Samples == 0 && out.Turns > 1 {
		fmt.Fprintf(os.Stderr, "redline: %d turns, %d context entries fetched", out.Turns, out.Fetched)
		if out.CapHit {
			fmt.Fprint(os.Stderr, ", stopped by the cost cap")
		}
		fmt.Fprintln(os.Stderr)
	}
	if entries, rerr := review.ReadLedger(o.ledgerDir()); rerr == nil && len(entries) > 1 {
		// The target is an average, so print the average - of the shape this
		// run just used, which is the only set this run belongs to.
		if s := review.SummarizeByShape(entries)[out.Pipeline]; s.Count > 1 {
			fmt.Fprintln(os.Stderr, "redline: to date,", s.String())
		}
	}

	// Stamped with the change it was written against, so the next `run`
	// can tell whether it belongs to what it is looking at. review.json
	// outlives the session it came from.
	reviewed := out.Review
	reviewed.Revision = change.ReviewIdentity(res.Report.BaseSHA, res.Change)
	// What the review was told, kept with what it said, so a reader of the
	// report can tell a finding the reviewer arrived at from one it was
	// pointed to.
	reviewed.Note = note
	// The recap is only readable beside the commit it measures from, and post
	// lists only the files that moved since then, so both travel with it.
	// Stamped only when this run's describing call wrote the recap, which
	// takes --since. A reused walkthrough keeps the commit and files it came
	// with: review.Run refuses --since beside --reuse-synopsis, so the reused
	// recap cannot be paired with this run's baseline, and without --since
	// there is no baseline here to stamp.
	if reviewed.Recap != "" && ropts.SinceReview != "" {
		reviewed.RecapSince, reviewed.RecapFiles = ropts.SinceReview, ropts.SinceFiles
	}
	// Carried into the file so a later `run`, `report`, or `post` that renders
	// review.json without this command's Result can still say the ruling broke
	// rather than merging the unchecked findings as if the pass had run.
	reviewed.VerifyFailed = out.VerifyFailed
	reviewed.Incomplete = out.Stopped
	// The partition a split review drew, so --reuse-synopsis can stand in
	// for a staged run's describing call too. Empty on a run that judged
	// the change in one call.
	for _, c := range out.Cohorts {
		reviewed.Cohorts = append(reviewed.Cohorts, findings.Cohort{Name: c.Name, Summary: c.Summary, Files: c.Files})
	}
	path := filepath.Join(o.out, "review.json")
	if err := review.Merge(path, reviewed); err != nil {
		return err
	}

	// Re-render from the session so the page shows the review immediately.
	// Nothing is observed again: the facts on the report are the ones that
	// were measured when the session was written, and re-measuring here
	// could quietly change them under a review that was written against the
	// old ones.
	rev, err := findings.LoadReview(path)
	if err != nil {
		return err
	}
	run.ReapplyReview(res, rev)
	if err := write(o, res); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "redline: wrote %s\n", path)
	return announce(o.out, o.port, o.open)
}

// describeSession names what is about to be reviewed, so the line printed
// before a multi-minute call says which change it is about. A session on disk
// outlives the command that wrote it, and reviewing the wrong one is the
// mistake this is meant to catch early.
func describeSession(res *run.Result) string {
	if res == nil || res.Target == nil {
		return "the last run"
	}
	return res.Target.Describe()
}

// writeTrace saves what this review did, for `redline postmortem`. It is the
// stage-one findings, the questions they named, what the lookups did about
// them, and what the ruling decided, none of which survives anywhere else:
// review.json is the outcome, and the outcome is what a reader already has.
func writeTrace(o opts, res *run.Result, out *review.Result, tally *scoutTally) error {
	look := postmortem.Lookup{
		Ran:       tally.ran,
		Model:     tally.model,
		Effort:    tally.effort,
		Error:     tally.err,
		Turns:     tally.turns,
		CostUSD:   tally.cost,
		CostKnown: tally.known && tally.ran,
		CapHit:    tally.capHit,
		Notes:     tally.log.Notes,
	}
	// Copied field by field rather than handed over. The scout is a context
	// provider and the trace is Redline's own file, so this command is the
	// one place that knows both shapes; internal/boundary is the test that
	// keeps it that way.
	for _, c := range tally.log.Calls {
		look.Calls = append(look.Calls, postmortem.Call{
			Turn: c.Turn, Tool: c.Tool, Args: c.Args, Result: c.Result, Failed: c.Failed,
		})
	}
	for _, f := range tally.log.Filed {
		look.Filed = append(look.Filed, postmortem.Filed{
			Role: f.Role, File: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
			Symbol: f.Symbol, FoundVia: f.FoundVia, Answers: f.Answers,
		})
	}
	t := postmortem.Of(out, look)
	t.Effort = out.Effort
	t.Target = describeSession(res)
	t.Revision = change.ReviewIdentity(res.Report.BaseSHA, res.Change)
	t.Note = o.note
	return postmortem.Write(o.out, t)
}

// scoutTally accumulates what the answering scout spent, so a cost paid inside
// the verify pass reaches the ledger rather than staying on a log line, and
// what it did, so `redline postmortem` can say where a lookup went.
//
// One review can run the lookups more than once in principle, so the calls
// and the records accumulate rather than replace: a second run's search is
// still part of what this review did.
type scoutTally struct {
	ran   bool
	cost  float64
	known bool
	turns int
	// model and effort are what the lookups actually ran on, after the
	// scout's own defaults.
	model, effort string
	// err is the last failure, when the lookups could not run at all. The
	// review goes on without them and the trace says so.
	err    string
	capHit bool
	log    scout.Log
}

// scoutAnswerer runs the lookups a review asked for, as the scout in its
// answering mode.
//
// This is where the two halves meet. `internal/scout` imports
// `internal/review` for its pricing, so the review producer cannot import the
// scout back, and the composition happens here in the command rather than in
// either package. That is also the honest place for it: whether a lookup costs
// money is the caller's business, and everything below degrades to nil rather
// than failing, so a checkout with no key still gets a ruling over the
// answers it has.
func scoutAnswerer(res *run.Result, ropts review.Options, scoutOpts scoutSettings, tally *scoutTally) review.Answerer {
	root := ""
	if res.Target != nil {
		root = res.Target.Dir
	}
	// A session outlives the tree it was written from. A fixture copied
	// elsewhere, or a worktree since reclaimed, has no repository to look
	// anything up in, and looking it up in the wrong one would be worse than
	// not looking.
	if root == "" || !isDir(root) {
		return nil
	}
	return func(ctx context.Context, qs []review.Question) (*envelope.Envelope, error) {
		sopts := answerOptions(root, res, ropts, scoutOpts, qs)
		fmt.Fprintf(os.Stderr, "redline: looking up %d question(s) the review asked\n", len(sopts.Questions))
		env, spend, err := scout.Run(ctx, sopts)
		if spend.Turns > 0 {
			fmt.Fprintf(os.Stderr, "redline: lookups took %d turn(s), %d record(s), %s\n",
				spend.Turns, spend.Records, review.FormatCost(spend.CostUSD, spend.CostKnown))
		}
		if tally != nil {
			tally.ran = true
			tally.cost += spend.CostUSD
			tally.known = tally.known && spend.CostKnown
			tally.turns += spend.Turns
			tally.model, tally.effort = spend.Model, spend.Effort
			tally.capHit = tally.capHit || spend.CapHit
			tally.log.Calls = append(tally.log.Calls, spend.Log.Calls...)
			tally.log.Filed = append(tally.log.Filed, spend.Log.Filed...)
			tally.log.Notes = append(tally.log.Notes, spend.Log.Notes...)
			if err != nil {
				tally.err = err.Error()
			}
		}
		return env, err
	}
}

// scoutSettings is what the command says about the checking pass alone.
type scoutSettings struct {
	Model  string
	Effort string
}

// scoutSettings resolves the checking pass's model and effort from the flags.
// --scout-model and --scout-effort win; without them the review's own --model
// and --effort carry over, so one flag runs both stages on one model unless
// the caller says otherwise. Empty stays empty, and the scout applies its own
// defaults to it, so a review run with no flags checks its findings as it
// always did.
func (o opts) scoutSettings() scoutSettings {
	s := scoutSettings{Model: o.scoutModel, Effort: o.scoutEffort}
	if s.Model == "" {
		s.Model = o.model
	}
	if s.Effort == "" {
		s.Effort = o.effort
	}
	return s
}

// sinceLastReview resolves --since and works out which files have moved
// between it and the head under review.
//
// Both are resolved here rather than in the library for the reason every other
// git question is: the library is handed a session and does not open a
// repository. A commit this checkout cannot find is refused rather than passed
// through, because the alternative is a describing call told a commit it
// cannot check and a recap written about a comparison nobody made.
//
// The head is the target's when it has one and the working tree otherwise,
// which is the same pair ReviewIdentity distinguishes.
func (o opts) sinceLastReview(res *run.Result) (string, []string, error) {
	// The checkout under review, not o.root. o.root is the session directory,
	// which is .redline inside the checkout by default and so happens to
	// resolve, but --out and --session can put it anywhere: a session cache
	// outside the repository would resolve --since against the wrong
	// repository or none at all. The target's own directory is what the panes
	// observe, and the working directory is the fallback the other commands
	// use when there is no target.
	dir := ""
	if res != nil && res.Change != nil && res.Change.Target != nil {
		dir = res.Change.Target.Dir
	}
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, fmt.Errorf("--since: %w", err)
		}
		dir = cwd
	}
	repo, err := gitx.Open(dir)
	if err != nil {
		return "", nil, fmt.Errorf("--since: %w", err)
	}
	since, err := repo.Resolve(o.since)
	if err != nil {
		return "", nil, fmt.Errorf("--since %s: %w; the previous review's commit has to be in this checkout, "+
			"which on a pull request means fetching it first", o.since, err)
	}
	head := ""
	if res != nil && res.Change != nil && res.Change.Target != nil {
		head = res.Change.Target.Head
	}
	var files []string
	if head == "" {
		files, err = repo.ChangedPaths(since)
	} else {
		files, err = repo.ChangedPathsBetween(since, head)
	}
	if err != nil {
		return "", nil, fmt.Errorf("--since %s: %w", o.since, err)
	}
	return since, files, nil
}

// describingEndpoint resolves where the describing call goes, from the
// --synopsis-* flags and the environment.
//
// The same rule scoutSettings follows: a --synopsis-* flag wins, and what it
// does not name the review's own call supplies, so --synopsis-model alone
// moves that one call and leaves the wire, the endpoint and the effort where
// they were. An endpoint with nothing set is the zero value, which the library
// reads as "the judging call's own", so a review with none of these flags
// makes exactly the calls it made before they existed.
//
// The credential is read here for the same reason the judging call's is read
// above: only the command knows the vendor's env names. It is read only when
// the describing call is on the OpenAI wire and the judging call is not,
// because that is the case where the key already in ropts belongs to another
// vendor and cannot be inherited. Where both calls are on that wire the
// library inherits the one already resolved, and an empty key on the
// Anthropic wire is left to the SDK's own credential chain.
func (o opts) describingEndpoint(ropts review.Options) review.Endpoint {
	e := review.Endpoint{
		API:     o.synopsisAPI,
		Model:   o.synopsisModel,
		BaseURL: o.synopsisBaseURL,
		Effort:  o.synopsisEffort,
	}
	if e.API == review.APIOpenAI && ropts.API != review.APIOpenAI {
		e.APIKey = os.Getenv("OPENAI_API_KEY")
		e.APIUser = os.Getenv("OPENAI_USER")
		if e.BaseURL == "" {
			e.BaseURL = os.Getenv("OPENAI_BASE_URL")
		}
	}
	return e
}

// answerOptions is the scout run the review's flags describe. The lookups go
// over the same wire the review did, on the model and effort scoutSettings
// resolved: --model and --effort used to stop at stage one, so a review run on
// another model still checked its findings with the scout's defaults, and
// there was no flag that reached the checking.
//
// The credentials follow the same rule. On the OpenAI wire the scout uses the
// credentials stage one used; on the Anthropic wire an empty key falls back
// to the SDK's own credential chain, an `ant auth login` profile included.
func answerOptions(root string, res *run.Result, ropts review.Options, scoutOpts scoutSettings, qs []review.Question) scout.Options {
	out := make([]scout.Question, 0, len(qs))
	for _, q := range qs {
		out = append(out, scout.Question{
			ID: q.ID, Kind: q.Kind, Ask: q.Ask, Subject: q.Subject,
			Claim: q.Claim, File: q.File, Line: q.Line,
		})
	}
	return scout.Options{
		Root:    root,
		Diff:    diffOf(res),
		BaseSHA: res.Report.BaseSHA,
		// Without this the answering scout sees only root-level guideline
		// files: guidelines() finds package-nested AGENTS.md and its kin by
		// walking up from each changed path, and an empty Changed has
		// nothing to walk up from. A question about a rule would then be
		// answered against the repository's most general rules rather than
		// the ones nearest the code it is about.
		Changed:   changedPaths(res.Change),
		Questions: out,
		Model:     scoutOpts.Model,
		Effort:    scoutOpts.Effort,
		API:       ropts.API,
		BaseURL:   ropts.BaseURL,
		APIKey:    ropts.APIKey,
		APIUser:   ropts.APIUser,
		Progress:  ropts.Progress,
		Debug:     ropts.Debug,
		Capture:   ropts.Capture,
	}
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// diffOf is the change as the review saw it, which is what the lookups are
// about. Rebuilt from the session rather than from git, so a frozen session
// asks its questions against the diff it was reviewed from.
func diffOf(res *run.Result) string {
	if res.Change == nil {
		return ""
	}
	var b strings.Builder
	for _, f := range res.Change.Files {
		if f.Diff == "" {
			continue
		}
		fmt.Fprintf(&b, "--- %s\n%s\n", f.Path, strings.TrimRight(f.Diff, "\n"))
	}
	return b.String()
}

// reviewNote is the note from whoever asked for this review, from --note or
// --note-file, trimmed. Empty when neither is given.
func reviewNote(o opts) (string, error) {
	switch {
	case o.note != "" && o.noteFile != "":
		return "", fmt.Errorf("--note and --note-file both give the note; pass one or the other")
	case o.noteFile != "":
		raw, err := os.ReadFile(o.noteFile)
		if err != nil {
			return "", fmt.Errorf("--note-file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return strings.TrimSpace(o.note), nil
}

// lookerFor is the tree the judging pass searches, or nil when there is none to
// search.
//
// The guard is scoutAnswerer's, for scoutAnswerer's reason: a session outlives
// the tree it was written from, and a fixture copied elsewhere or a worktree
// since reclaimed has no repository to look anything up in. Looking it up in
// the wrong one would be worse than not looking, and nil is how a pass is told
// the tools are not there rather than being handed a wrong answer.
func lookerFor(res *run.Result) review.Looker {
	root := ""
	if res.Target != nil {
		root = res.Target.Dir
	}
	if root == "" || !isDir(root) {
		return nil
	}
	return scout.NewLooker(root, res.Report.BaseSHA)
}
