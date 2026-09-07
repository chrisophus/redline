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
	ropts := review.Options{
		Model:      o.model,
		Effort:     o.effort,
		Ceiling:    o.ceiling,
		MaxTokens:  int64(o.maxTokens),
		MaxCostUSD: o.maxCost,
		DryRun:     o.dryRun,
	}

	out, err := review.Run(context.Background(), in, ropts)
	if out != nil {
		if s := out.Budget.Summary(); s != "" {
			fmt.Fprintln(os.Stderr, "redline:", s)
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
		fmt.Fprintf(os.Stderr, "\nredline: %d input tokens estimated, at most %s to send\n",
			out.InputEstimate, review.FormatCost(out.CostUSD, out.CostKnown))
		return nil
	}
	fmt.Fprintln(os.Stderr, "redline: review", out.Summary())

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
