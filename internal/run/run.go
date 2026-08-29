// Package run wires panes to a repository and produces one Report.
package run

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/pane"
	"github.com/ccason/redline/internal/pane/migrations"
)

// Options configures one run.
type Options struct {
	Dir      string // any directory inside the repository
	Base     string // base ref; merge-base with HEAD is used
	Upstream string // branch new migrations must not collide with
	MigDir   string // optional migrations directory filter
}

// Result is a report plus the per-pane renders backing section 1 and the
// evidence artifacts findings point at.
type Result struct {
	Report   findings.Report
	Renders  []pane.Render
	Evidence map[string]pane.Artifact
}

// Run executes every applicable pane. Applicability is computed from the diff,
// never chosen by a caller: if the caller decides which checks to run, coverage
// becomes a sample from a distribution and a check can be silently skipped.
func Run(opts Options) (*Result, error) {
	repo, err := gitx.Open(opts.Dir)
	if err != nil {
		return nil, err
	}
	baseRef := resolveRef(repo, opts.Base)
	if baseRef == "" {
		return nil, fmt.Errorf("cannot determine a base revision: tried %s. "+
			"Pass --base REF, or make a first commit if this repository has none",
			strings.Join(defaultRefs, ", "))
	}
	baseSHA, err := repo.MergeBase(baseRef)
	if err != nil {
		return nil, fmt.Errorf("base %q: %w", baseRef, err)
	}
	changed, err := repo.ChangedPaths(baseSHA)
	if err != nil {
		return nil, err
	}

	res := &Result{Evidence: map[string]pane.Artifact{}, Report: findings.Report{
		BaseRef: baseRef,
		BaseSHA: baseSHA,
		Scope:   changed,
	}}

	panes := []pane.Pane{
		&migrations.Pane{Repo: repo, UpstreamRef: resolveRef(repo, opts.Upstream, baseRef), Dir: opts.MigDir},
	}

	examined := map[string]bool{}
	for _, p := range panes {
		status := findings.SubstrateStatus{Name: p.Name()}
		scope := p.Scope(changed)
		for _, path := range scope {
			examined[path] = true
		}
		if len(scope) == 0 {
			status.State = findings.SubstrateSkipped
			status.Detail = "no files in scope for this pane"
			res.Report.Substrates = append(res.Report.Substrates, status)
			continue
		}
		out, err := runPane(p, baseSHA)
		if err != nil {
			// A pane that applied but did not run is a dark sensor. It is
			// recorded as failed and as an unknown; it never reads as a pass.
			status.State = findings.SubstrateFailed
			status.Detail = err.Error()
			res.Report.Substrates = append(res.Report.Substrates, status)
			res.Report.Unknowns = append(res.Report.Unknowns, findings.Unknown{
				Substrate: p.Name(),
				Message:   "pane applied to this change but did not run",
				Reason:    err.Error(),
			})
			continue
		}
		status.State = findings.SubstrateRan
		res.Report.Substrates = append(res.Report.Substrates, status)
		res.Report.Findings = append(res.Report.Findings, out.Findings...)
		res.Report.Confirmations = append(res.Report.Confirmations, out.Confirmations...)
		res.Report.Unknowns = append(res.Report.Unknowns, out.Unknowns...)
		res.Renders = append(res.Renders, out.Render)
		for id, a := range out.Evidence {
			res.Evidence[id] = a
		}
	}

	res.Report.Coverage = coverage(changed, examined)
	if res.Report.Coverage.ExaminedFiles == 0 && len(changed) > 0 {
		// No pane looked at any of it. This is not a clean review and must
		// never render as one.
		res.Report.Unknowns = append(res.Report.Unknowns, findings.Unknown{
			Substrate: "redline",
			Message:   fmt.Sprintf("none of the %d changed file(s) fall under any pane Redline currently ships", len(changed)),
			Reason:    "this change is entirely unexamined; the empty findings list below is not evidence of correctness",
		})
	}
	sortFindings(res.Report.Findings)
	res.Report.Finalize()
	return res, nil
}

func runPane(p pane.Pane, baseSHA string) (pane.Result, error) {
	before, err := p.Observe(pane.Revision{Name: "base", Rev: baseSHA})
	if err != nil {
		return pane.Result{}, fmt.Errorf("observe base: %w", err)
	}
	after, err := p.Observe(pane.Worktree)
	if err != nil {
		return pane.Result{}, fmt.Errorf("observe worktree: %w", err)
	}
	return p.Diff(before, after)
}

// resolveRef returns the first ref that resolves: an explicit request, then
// each fallback, then the usual upstream names — so rung 1 needs no config
// file. An explicit ref is returned even if it does not resolve, so the caller
// reports the real failure instead of silently checking something else. When
// nothing resolves the result is empty, and the pane reports the check as
// undetermined rather than quietly checking nothing.
// defaultRefs are the branch names tried, in order, when none is given.
var defaultRefs = []string{"origin/main", "origin/master", "main", "master"}

func resolveRef(repo *gitx.Repo, want string, fallbacks ...string) string {
	if want != "" {
		return want
	}
	for _, candidate := range append(fallbacks, defaultRefs...) {
		if candidate != "" && repo.Exists(candidate) {
			return candidate
		}
	}
	return ""
}

// coverage reports how much of the change at least one pane examined.
func coverage(changed []string, examined map[string]bool) findings.Coverage {
	c := findings.Coverage{ChangedFiles: len(changed), ExaminedFiles: len(examined)}
	for _, path := range changed {
		if !examined[path] {
			c.Unexamined = append(c.Unexamined, path)
		}
	}
	return c
}

// sortFindings orders by severity, then substrate, then rule, then location,
// so output is stable across runs. Surprise ranking decides emphasis within
// the report; it never decides inclusion.
func sortFindings(fs []findings.Finding) {
	rank := map[findings.Severity]int{findings.SeverityError: 0, findings.SeverityWarning: 1, findings.SeverityInfo: 2}
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if rank[a.Severity] != rank[b.Severity] {
			return rank[a.Severity] < rank[b.Severity]
		}
		if a.Substrate != b.Substrate {
			return a.Substrate < b.Substrate
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.File < b.File
	})
}
