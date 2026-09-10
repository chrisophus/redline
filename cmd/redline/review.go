package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/scout"
)

// cmdReview is the one command in Redline that calls a model.
//
// It reviews the session `run` already wrote rather than observing anything
// itself, which is what makes it a pure function of its inputs: the same
// session reviewed twice sees exactly the same material, and a frozen session
// is a fixture the eval can replay. It also means `run` stays model-free, and
// a repository with no API key still gets the whole report.
func cmdReview(o opts) error {
	if o.stats {
		entries, err := review.ReadLedger(o.out)
		if err != nil {
			return err
		}
		fmt.Println(review.Summarize(entries).String())
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
	}
	// Price the estimate against what this installation's reviews actually
	// emit, when it has emitted any.
	var expected int64
	if entries, lerr := review.ReadLedger(o.out); lerr == nil {
		expected = review.Summarize(entries).ExpectedOutput()
	}
	ropts := review.Options{
		API:            o.api,
		BaseURL:        o.baseURL,
		Model:          o.model,
		Effort:         o.effort,
		Ceiling:        o.ceiling,
		MaxTokens:      int64(o.maxTokens),
		MaxCostUSD:     o.maxCost,
		Mode:           o.mode,
		MaxTurns:       o.maxTurns,
		Samples:        o.samples,
		ExpectedOutput: expected,
		DryRun:         o.dryRun,
	}
	// The checking pass is on unless it is turned off. A review that posts
	// what it cannot point at is the failure this producer was measured
	// against in the field, so the safe default is the one that checks, and
	// --no-verify is there for a caller comparing against the old behaviour
	// or paying for one call rather than two.
	ropts.Verify = !o.noVerify && o.mode != review.ModeExplore
	if o.verify {
		ropts.Verify = true
	}
	// The scout's spend is accumulated here so a cost paid inside the verify
	// pass reaches the ledger and not only its own log line.
	tally := scoutTally{known: true}
	if ropts.Verify {
		ropts.Answer = scoutAnswerer(res, o, ropts, &tally)
	}
	if o.api == review.APIOpenAI {
		// The Anthropic SDK reads its own environment. The OpenAI backend is
		// plain HTTP, so the same convention is applied here, in the names
		// the vendor's own tools use, and the flag wins over the variable.
		ropts.APIKey = os.Getenv("OPENAI_API_KEY")
		if ropts.BaseURL == "" {
			ropts.BaseURL = os.Getenv("OPENAI_BASE_URL")
		}
		ropts.APIUser = o.apiUser
		if ropts.APIUser == "" {
			ropts.APIUser = os.Getenv("OPENAI_USER")
		}
	}
	if !o.dryRun {
		// Said before the call, not after it. A review is one blocking
		// request of two to five minutes and the command printed nothing
		// until it returned, so a working run and a hung one looked
		// identical. Assemble is what --dry-run does and calls nothing, so
		// the price quoted here is the price about to be paid.
		if est, aerr := review.Assemble(in, ropts); aerr == nil {
			// est carries the resolved model and wire, since the defaults are
			// applied inside Assemble rather than out here.
			fmt.Fprintf(os.Stderr, "redline: reviewing %s with %s (%s), %d input tokens, expect %s, at most %s\n",
				describeSession(res), est.Model, est.API, est.InputEstimate,
				review.FormatCost(est.CostUSD, est.CostKnown),
				review.FormatCost(est.CostCeilingUSD, est.CostKnown))
		}
		ropts.Progress = func(msg string) {
			fmt.Fprintln(os.Stderr, "redline:", msg)
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
		if rerr := review.Record(o.out, out, o.effort); rerr != nil {
			// Not fatal. A review that produced findings has done its job,
			// and losing a cost line is not worth failing the command over.
			fmt.Fprintf(os.Stderr, "redline: could not record the run's cost: %v\n", rerr)
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
		fmt.Fprintf(os.Stderr, "\nredline: %d input tokens estimated; expect %s, at most %s\n",
			out.InputEstimate,
			review.FormatCost(out.CostUSD, out.CostKnown),
			review.FormatCost(out.CostCeilingUSD, out.CostKnown))
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
	if entries, rerr := review.ReadLedger(o.out); rerr == nil && len(entries) > 1 {
		// The target is an average, so print the average. One review's cost
		// says nothing about whether the tool is affordable to keep running.
		fmt.Fprintln(os.Stderr, "redline: to date,", review.Summarize(entries).String())
	}

	// Stamped with the change it was written against, so the next `run`
	// can tell whether it belongs to what it is looking at. review.json
	// outlives the session it came from.
	reviewed := out.Review
	reviewed.Revision = change.ReviewIdentity(res.Report.BaseSHA, res.Change)
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
	return announce(o.out, o.port, o.open && !o.noOpen)
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

// scoutTally accumulates what the answering scout spent, so a cost paid inside
// the verify pass reaches the ledger rather than staying on a log line.
type scoutTally struct {
	ran   bool
	cost  float64
	known bool
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
func scoutAnswerer(res *run.Result, o opts, ropts review.Options, tally *scoutTally) review.Answerer {
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
	// The scout speaks only to Anthropic, whichever wire the review used, so
	// it takes Anthropic credentials and never the OpenAI gateway's. Handing
	// the gateway's URL and key to an Anthropic client is why verification
	// always failed on the --api openai path.
	baseURL, apiKey := "", ""
	if ropts.API == review.APIOpenAI {
		// The review is going out over the OpenAI wire, so stage one used the
		// OpenAI key. The scout needs an Anthropic credential of its own, and
		// with only OpenAI credentials there is none: running it would spend a
		// call only to fail. It is skipped and said so, the ruling still runs
		// over no answers, and a finding that needed a lookup comes back
		// unverifiable rather than confirmed.
		if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
			fmt.Fprintln(os.Stderr, "redline: the checking pass has no Anthropic credential for its lookups "+
				"on the OpenAI wire, so they are skipped; findings that need one are marked unverifiable")
			return nil
		}
	} else {
		// The Anthropic wire. Stage one already authenticated through the
		// SDK's credential chain, an `ant auth login` profile included, so
		// these carry any explicit override and empty falls back to that
		// chain.
		baseURL, apiKey = ropts.BaseURL, ropts.APIKey
	}
	return func(ctx context.Context, qs []review.Question) (*envelope.Envelope, error) {
		out := make([]scout.Question, 0, len(qs))
		for _, q := range qs {
			out = append(out, scout.Question{
				ID: q.ID, Kind: q.Kind, Ask: q.Ask, Subject: q.Subject,
				Claim: q.Claim, File: q.File, Line: q.Line,
			})
		}
		fmt.Fprintf(os.Stderr, "redline: looking up %d question(s) the review asked\n", len(out))
		env, spend, err := scout.Run(ctx, scout.Options{
			Root:      root,
			Diff:      diffOf(res),
			BaseSHA:   res.Report.BaseSHA,
			Questions: out,
			BaseURL:   baseURL,
			APIKey:    apiKey,
		})
		if spend.Turns > 0 {
			fmt.Fprintf(os.Stderr, "redline: lookups took %d turn(s), %d record(s), %s\n",
				spend.Turns, spend.Records, review.FormatCost(spend.CostUSD, spend.CostKnown))
		}
		if tally != nil {
			tally.ran = true
			tally.cost += spend.CostUSD
			tally.known = tally.known && spend.CostKnown
		}
		return env, err
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
