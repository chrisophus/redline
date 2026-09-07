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
	}
	// Price the estimate against what this installation's reviews actually
	// emit, when it has emitted any.
	var expected int64
	if entries, lerr := review.ReadLedger(o.out); lerr == nil {
		expected = review.Summarize(entries).ExpectedOutput()
	}
	ropts := review.Options{
		Model:          o.model,
		Effort:         o.effort,
		Ceiling:        o.ceiling,
		MaxTokens:      int64(o.maxTokens),
		MaxCostUSD:     o.maxCost,
		Mode:           o.mode,
		MaxTurns:       o.maxTurns,
		ExpectedOutput: expected,
		DryRun:         o.dryRun,
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
					"so no context beyond the diff was sent. Review a smaller range, or raise --ceiling.\n",
				out.FixedEstimate, out.Ceiling)
		}
	}
	if err != nil {
		return err
	}
	if o.dryRun {
		// Print the prompt rather than a summary of it. The point of a dry
		// run is to see exactly what would be sent, and the estimated price
		// of sending it.
		fmt.Print(out.Prompt)
		fmt.Fprintf(os.Stderr, "\nredline: %d input tokens estimated; expect %s, at most %s\n",
			out.InputEstimate,
			review.FormatCost(out.CostUSD, out.CostKnown),
			review.FormatCost(out.CostCeilingUSD, out.CostKnown))
		return nil
	}
	fmt.Fprintln(os.Stderr, "redline: review", out.Summary())
	if out.Turns > 1 {
		fmt.Fprintf(os.Stderr, "redline: %d turns, %d context entries fetched", out.Turns, out.Fetched)
		if out.CapHit {
			fmt.Fprint(os.Stderr, ", stopped by the cost cap")
		}
		fmt.Fprintln(os.Stderr)
	}
	if err := review.Record(o.out, out, o.effort); err != nil {
		// Not fatal. A review that produced findings has done its job, and
		// losing a cost line is not worth failing the command over.
		fmt.Fprintf(os.Stderr, "redline: could not record the run's cost: %v\n", err)
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
