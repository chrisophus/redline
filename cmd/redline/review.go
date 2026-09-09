package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
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

	out, err := review.Run(context.Background(), in, ropts)
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

	path := filepath.Join(o.out, "review.json")
	if err := review.Merge(path, out.Review); err != nil {
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
