// Package run wires panes to a repository and produces one Report.
package run

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/graph"
	"github.com/ccason/redline/internal/packet"
	"github.com/ccason/redline/internal/pane"
	"github.com/ccason/redline/internal/pane/migrations"
	"github.com/ccason/redline/internal/target"
)

// Options configures one run.
type Options struct {
	Dir      string // any directory inside the repository
	Base     string // base ref; merge-base with HEAD is used
	Upstream string // branch new migrations must not collide with
	MigDir   string // optional migrations directory filter

	// These point Redline at something other than the working tree.
	// Each is materialized as a detached worktree, so the user's own
	// checkout is never moved.
	PR     string
	Branch string
	Commit string
	Range  string
	Out    string // evidence directory; excluded from the change like .redline/
}

// Result is a report plus the per-pane renders backing section 1 and the
// evidence artifacts findings point at.
type Result struct {
	Report   findings.Report
	Renders  []pane.Render
	Evidence map[string]pane.Artifact
	Target   *target.Target
	Packet   *packet.Packet
	// Review is the agent's last ingested judgment, carried across runs by
	// the session snapshot. The report's summary, walkthrough, and contract
	// highlights live only here — findings.Report has no field for them — so
	// a follow-up ingest must merge onto it rather than replace it.
	Review *packet.Review
}

// Run executes every applicable pane. Applicability is computed from the diff,
// never chosen by a caller: if the caller decides which checks to run, coverage
// becomes a sample from a distribution and a check can be silently skipped.
func Run(opts Options) (*Result, error) {
	tgt, err := target.Resolve(target.Options{
		Dir: opts.Dir, PR: opts.PR, Branch: opts.Branch,
		Commit: opts.Commit, Range: opts.Range, Base: opts.Base,
	})
	if err != nil {
		return nil, err
	}
	// Panes observe the target's directory. For a PR or a branch that is a
	// detached worktree at the head commit, so "the working tree" and "the
	// revision under review" are the same thing and no pane needs to know
	// which kind of target it is looking at.
	repo, err := gitx.Open(tgt.Dir)
	if err != nil {
		return nil, err
	}
	if tgt.Base != "" {
		opts.Base = tgt.Base
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
	changed = excludeOwnOutput(changed, opts.Out, repo.Root)

	res := &Result{Evidence: map[string]pane.Artifact{}, Target: tgt, Report: findings.Report{
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
	res.Report.Unknowns = append(res.Report.Unknowns, unbuiltPanes(changed, examined)...)
	if res.Report.Coverage.ExaminedFiles == 0 && len(changed) > 0 {
		// No pane looked at any of it. This is not a clean review and must
		// never render as one.
		res.Report.Unknowns = append(res.Report.Unknowns, findings.Unknown{
			Substrate: "redline",
			Message:   fmt.Sprintf("none of the %d changed file(s) fall under any pane Redline currently ships", len(changed)),
			Reason:    "this change is entirely unexamined; the empty findings list below is not evidence of correctness",
		})
	}
	res.Report.Finalize()
	findings.Sort(res.Report.Findings)

	// The packet is built on every run, not only for `review`: it is what the
	// HTML report renders its drill-in sections from.
	res.Packet = packet.Build(repo, tgt, baseSHA, changed, res.Report.Findings)
	attachThreads(res.Packet, tgt.Dir, changed)
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

// attachThreads adds graph-derived threads when the repository has a graphify
// graph. Absent graph, absent threads — Redline does not build one, because
// extraction is an expensive pass the user should choose to run.
func attachThreads(p *packet.Packet, dir string, changed []string) {
	path := graph.Locate(dir)
	if path == "" {
		return
	}
	g, err := graph.Load(path)
	if err != nil {
		return
	}
	p.Threads = toPacketThreads(g.Threads(changed))
}

func toPacketThreads(ts []graph.Thread) []packet.Thread {
	out := make([]packet.Thread, len(ts))
	for i, t := range ts {
		out[i] = packet.Thread{From: t.From, To: t.To, Nodes: t.Nodes, Explanation: t.Explanation}
	}
	return out
}

// excludeOwnOutput drops Redline's own evidence directory from the change.
// Without this a second run reviews the first run's output, which inflates
// coverage with files nobody wrote and shows the reviewer their own report as
// part of the diff.
func excludeOwnOutput(changed []string, outDir, repoRoot string) []string {
	skip := ownOutputPrefixes(outDir, repoRoot)
	out := changed[:0:0]
	for _, path := range changed {
		if skippedPath(path, skip) {
			continue
		}
		out = append(out, path)
	}
	return out
}

func ownOutputPrefixes(outDir, repoRoot string) []string {
	prefixes := []string{".redline"}
	if outDir == "" {
		return prefixes
	}
	rel := filepath.ToSlash(filepath.Clean(outDir))
	if filepath.IsAbs(outDir) && repoRoot != "" {
		if r, err := filepath.Rel(repoRoot, outDir); err == nil && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		} else {
			// Evidence directory is outside the repo; nothing in the change
			// can be it.
			return prefixes
		}
	}
	rel = strings.TrimPrefix(rel, "./")
	if rel != "" && rel != ".redline" {
		prefixes = append(prefixes, rel)
	}
	return prefixes
}

func skippedPath(path string, prefixes []string) bool {
	path = filepath.ToSlash(path)
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// unbuilt names the check families the catalog specifies but Redline does not
// yet implement, and the area of the change each would have covered.
var unbuilt = []struct{ Area, Detail string }{
	{"api", "checks 10-13 (OpenAPI breaking-change diff, vacuum, spec-vs-handler, observed-vs-declared) are not built"},
	{"ui", "checks 15-16 (before/after route screenshots, console and network errors) are not built"},
	{"tests", "check 14 (diff coverage — added lines no test executes) is not built"},
}

// unbuiltPanes reports, per area, that a part of this change falls under a
// check family Redline has specified but not yet shipped. Without this a
// reviewer cannot distinguish "the API pane found nothing" from "there is no
// API pane" — and will assume the first.
func unbuiltPanes(changed []string, examined map[string]bool) []findings.Unknown {
	byArea := map[string]int{}
	for _, path := range changed {
		if examined[path] {
			continue
		}
		for _, area := range packet.Areas(path) {
			byArea[area]++
		}
	}
	var out []findings.Unknown
	for _, u := range unbuilt {
		n := byArea[u.Area]
		if n == 0 {
			continue
		}
		out = append(out, findings.Unknown{
			Substrate: "redline/" + u.Area,
			Message:   fmt.Sprintf("%d file(s) in this change fall under the %s pane, which examined none of them", n, u.Area),
			Reason:    u.Detail,
		})
	}
	return out
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
