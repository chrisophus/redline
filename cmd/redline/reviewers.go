package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/reviewer"
	"github.com/ccason/redline/internal/run"
	"github.com/ccason/redline/internal/target"
)

// noReviewer is what --with takes to mean "observe only". It exists as a
// spelling of its own so that a configured default reviewer can be turned off
// for one run without editing config.
const noReviewer = "none"

// withReviewer runs the external reviewers named by --with and folds what they
// found into the report. Redline does not compose the judgment: each adapter
// invokes its tool's own review command, and every finding is stamped with the
// reviewer's name and source "llm" so a reader can tell it from the
// deterministic panes.
//
// Several reviewers can run in one pass. Two reviewers from different vendors
// is what a second review round used to be, now that a PR does not arrive with
// one already attached. Their findings are kept side by side under their own
// names rather than reconciled — see below.
//
// It returns an error only for a mistake the operator can fix before the run is
// worth repeating: an unknown reviewer name, checked for every name before any
// reviewer runs. A reviewer that ran and failed is recorded, not raised. The
// review still produced observed evidence, exiting non-zero would tell the
// driving agent the whole review failed, and one vendor being down must not
// discard what the other found.
func withReviewer(o opts, res *run.Result) error {
	names, err := reviewersToRun(o)
	if err != nil || len(names) == 0 {
		return err
	}

	adapters, err := reviewer.Load(o.out)
	if err != nil {
		return err
	}
	chosen := make([]reviewer.Adapter, 0, len(names))
	for _, name := range names {
		a, ok := adapters[name]
		if !ok {
			return fmt.Errorf("unknown reviewer %q (known: %s; add your own in %s)",
				name, strings.Join(adapterNames(adapters), ", "), filepath.Join(o.out, "reviewers.json"))
		}
		chosen = append(chosen, a)
	}

	dir, err := reviewDir(res.Packet.Target)
	if err != nil {
		return err
	}
	tgt := reviewTarget(res.Packet.Target)

	status := newStatus(os.Stderr)
	for _, a := range chosen {
		started := time.Now()
		found, runErr := reviewer.Run(context.Background(), a, dir, tgt, o.out,
			reviewer.WithProgress(status.interval, status.update))
		if runErr != nil {
			// A reviewer that did not run must not read as a reviewer that
			// found nothing. Recording it as a failed substrate puts it in the
			// report's "did not run" section, the same treatment a pane that
			// could not execute gets, and for the same reason.
			res.Report.Substrates = append(res.Report.Substrates, findings.SubstrateStatus{
				Name:   "reviewer:" + a.Name,
				State:  findings.SubstrateFailed,
				Detail: runErr.Error(),
			})
			res.Report.Unknowns = append(res.Report.Unknowns, findings.Unknown{
				Substrate: "reviewer:" + a.Name,
				Message:   "the " + a.Name + " review did not run, so nothing it would have caught is in this report",
				Reason:    runErr.Error(),
			})
			status.fail(a.Name, time.Since(started), runErr)
			continue
		}
		status.done(a.Name, time.Since(started), len(found))

		res.Report.Substrates = append(res.Report.Substrates, findings.SubstrateStatus{
			Name:   "reviewer:" + a.Name,
			State:  findings.SubstrateRan,
			Detail: fmt.Sprintf("%d findings", len(found)),
		})
		// Appended, not merged, and that stays true with two reviewers. It is
		// tempting to badge the ones both of them reported, but deciding that
		// two differently worded sentences describe one defect is a guess, and
		// a wrong guess deletes a finding the reviewer never learns existed.
		// The report groups by reviewer instead and lets the reader compare.
		// Exact duplicates still collapse in Report.Dedupe on fingerprint.
		res.Report.Findings = append(res.Report.Findings, toFindings(a.Name, found)...)
	}

	findings.Sort(res.Report.Findings)
	res.Report.Finalize()
	res.Report.Dedupe()
	return nil
}

// reviewersToRun resolves --with into the names to run, in the order given and
// without repeats. "none" is an explicit off switch and wins over everything
// else, so a configured default can be silenced for one run.
func reviewersToRun(o opts) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	for _, raw := range o.with {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if name == noReviewer {
			return nil, nil
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// toFindings converts one reviewer's output into Redline's schema. Severity
// and confidence arrive already normalized from the adapter; what is added
// here is provenance, which is the whole point of keeping reviewer findings in
// the same list as observed ones rather than a section of their own.
func toFindings(name string, in []reviewer.Finding) []findings.Finding {
	out := make([]findings.Finding, 0, len(in))
	for _, f := range in {
		g := findings.Finding{
			File:       f.File,
			Line:       f.Line,
			Rule:       "review",
			Substrate:  "reviewer:" + name,
			Category:   findings.CategoryReview,
			Severity:   findings.Severity(f.Severity),
			Message:    f.Title,
			Context:    f.Body,
			Source:     findings.SourceLLM,
			Reviewer:   name,
			Confidence: f.Confidence,
			New:        true,
		}
		g.Fingerprint = findings.Fingerprint(g)
		out = append(out, g)
	}
	return out
}

// reviewDir is the tree the reviewer must read. For every target but the
// working one, Redline has already checked the revision out into a detached
// worktree, and that is where the reviewer belongs: pointing it at the current
// directory instead reviews whatever the developer happens to have checked
// out, so the prose and the observed evidence would describe different code.
func reviewDir(t *target.Target) (string, error) {
	if t != nil && t.Dir != "" {
		return t.Dir, nil
	}
	return os.Getwd()
}

// reviewTarget names the change in the vocabulary the reviewer's own review
// command takes: a PR number, a ref, or nothing at all for uncommitted work,
// which every reviewer reads as "the current diff".
func reviewTarget(t *target.Target) string {
	if t == nil {
		return ""
	}
	switch t.Kind {
	case target.KindPR:
		if t.PR != nil {
			return fmt.Sprintf("%d", t.PR.Number)
		}
	case target.KindWorktree:
		// Nothing to name: the reviewer runs in the tree that holds the
		// uncommitted work, and every reviewer reads a bare review as "the
		// current diff".
		return ""
	}
	// Inside the detached worktree the target is checked out at HEAD, so name
	// the revision rather than the label the user typed: a label like a branch
	// name may not resolve there, while the SHA always does.
	if t.Head != "" {
		return t.Head
	}
	return t.Label
}

func adapterNames(m map[string]reviewer.Adapter) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
