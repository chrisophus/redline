// Package run wires panes to a repository and produces one Report.
package run

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/harness"
	"github.com/chrisophus/redline/internal/mutation"
	"github.com/chrisophus/redline/internal/pane"
	"github.com/chrisophus/redline/internal/pane/lint"
	"github.com/chrisophus/redline/internal/pane/migrations"
	"github.com/chrisophus/redline/internal/pane/openapi"
	"github.com/chrisophus/redline/internal/pane/testdelta"
	"github.com/chrisophus/redline/internal/provider"
	"github.com/chrisophus/redline/internal/target"
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

	// AllowMissingCoverage skips the fail-fast check for configured coverage
	// harness profiles. Other configured profiles (mutation, etc.) still apply.
	AllowMissingCoverage bool
}

// Result is a report plus the per-pane renders backing section 1 and the
// evidence artifacts findings point at.
type Result struct {
	Report   findings.Report
	Renders  []pane.Render
	Evidence map[string]pane.Artifact
	Target   *target.Target
	// Change is the file-by-file account of the diff the report renders its
	// walkthrough and drill-in sections from.
	Change *change.Set
	// LineCoverage overlays the profile onto the diff: per changed .go file,
	// each coverable line mapped to whether a test ran it. Nil when no profile.
	LineCoverage map[string]map[int]bool

	// Envelopes is the resolved context, one per language provider that ran.
	// It is produced here, in the deterministic wave, so `redline review` is
	// a pure function of a saved session and a frozen fixture carries the
	// exact input its review was given.
	Envelopes []*envelope.Envelope
	// ContextAbsent names providers that were configured and did not run. A
	// provider that errors degrades to a missing input rather than blocking
	// anything, and the eval needs to tell a genuine miss from a gap.
	ContextAbsent []string
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

	// Generated output leaves the change here, before any pane or the report
	// sees it, so the coverage denominator counts files a reviewer would
	// actually read. What was dropped is recorded and shown: an exclusion the
	// reader cannot see is indistinguishable from a file that never changed.
	changed, generated := change.Generated(tgt.Dir, changed, repo.AttrSet("linguist-generated", changed))

	cfg, err := harness.Load(harnessConfigRoot(opts.Dir, tgt))
	if err != nil {
		return nil, err
	}
	configRoot := harnessConfigRoot(opts.Dir, tgt)
	prepared := map[string]bool{}
	roots := harness.Roots{Observe: tgt.Dir, Origin: originCoverageDir(opts.Dir, tgt), Caller: configRoot}
	if err := harness.Require(roots, changed, cfg, harness.RequireOpts{
		SkipCoverage: opts.AllowMissingCoverage,
	}); err != nil {
		return nil, err
	}
	if cfg != nil {
		if _, err := harness.PrepareWorktree(tgt.Dir, configRoot, changed, cfg, prepared); err != nil {
			return nil, err
		}
	}

	res := &Result{Evidence: map[string]pane.Artifact{}, Target: tgt, Report: findings.Report{
		BaseRef: baseRef,
		BaseSHA: baseSHA,
		Scope:   changed,
	}}

	panes := []pane.Pane{
		&migrations.Pane{Repo: repo, UpstreamRef: resolveRef(repo, opts.Upstream, baseRef), Dir: opts.MigDir},
		&openapi.Pane{Repo: repo},
		&lint.Delta{Repo: repo, Harness: cfg, HarnessRoot: configRoot, Prepared: prepared},
		&lint.Suppressions{Repo: repo},
		&lint.Config{Repo: repo},
		&testdelta.Pane{Repo: repo},
	}

	// Every path in the tree under review, listed once and only if a pane
	// turns out to have nothing in the change to look at. It answers a
	// different question from scope: not "does this pane apply to the change"
	// but "does this pane apply to the repository". A repository with no
	// migrations gets no mention of migrations; one whose migrations this
	// change did not touch is told so, quietly.
	var allPaths []string
	listAll := func() []string {
		if allPaths != nil {
			return allPaths
		}
		var blobs map[string]string
		var lerr error
		if tgt.Head == "" {
			blobs, lerr = repo.WorktreeBlobs()
		} else {
			blobs, lerr = repo.Blobs(tgt.Head)
		}
		if lerr != nil {
			// Unknown whether the pane applies: fall back to saying it was
			// skipped, the state that still gets a line on the report.
			allPaths = []string{}
			return allPaths
		}
		allPaths = make([]string, 0, len(blobs))
		for path := range blobs {
			allPaths = append(allPaths, path)
		}
		sort.Strings(allPaths)
		return allPaths
	}

	examined := map[string]bool{}
	for _, p := range panes {
		status := findings.SubstrateStatus{Name: p.Name()}
		scope := p.Scope(changed)
		for _, path := range scope {
			examined[path] = true
		}
		if len(scope) == 0 {
			// Scope over the whole tree is only consulted for a pane that will
			// not run, so the state it leaves behind in the pane is unused.
			if all := listAll(); len(all) > 0 && len(p.Scope(all)) == 0 {
				status.State = findings.SubstrateNotApplicable
				status.Detail = "the repository has no files this pane reads"
			} else {
				status.State = findings.SubstrateSkipped
				status.Detail = "this change touches none of the files this pane reads"
			}
			res.Report.Substrates = append(res.Report.Substrates, status)
			continue
		}
		out, err := runPane(p, baseSHA)
		if err != nil {
			// A pane that applied but did not run is recorded as failed and
			// as an unknown; it never reads as a pass.
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
		res.Report.Tools = append(res.Report.Tools, out.Tools...)
		res.Renders = append(res.Renders, out.Render)
		for id, a := range out.Evidence {
			res.Evidence[id] = a
		}
	}

	res.Report.Coverage = coverage(changed, examined)
	res.Report.Coverage.Generated = generated
	res.Report.Unknowns = append(res.Report.Unknowns, unbuiltPanes(changed, examined)...)
	// The agent's review, if it has written one. Loaded before Finalize so its
	// line comments become findings that get fingerprints stamped with the rest;
	// verdicts merge after, joining on those fingerprints. Never fatal: a run
	// reports facts with or without a reading, and a broken file is stated.
	review, verr := findings.LoadReview(filepath.Join(opts.Out, "review.json"))
	if verr != nil {
		res.Report.Unknowns = append(res.Report.Unknowns, findings.Unknown{
			Substrate: "redline/review",
			Message:   "review.json was present but could not be read, so no agent review was merged",
			Reason:    verr.Error(),
		})
	}
	if review != nil {
		res.Report.Findings = append(res.Report.Findings, review.CommentFindings()...)
		if review.Overview != "" || len(review.Files) > 0 {
			res.Report.Agent = &findings.AgentReview{Overview: review.Overview, Files: review.Files}
		}
	}
	if res.Report.Coverage.ExaminedFiles == 0 && len(changed) > 0 {
		// No pane looked at any of it. This is not a clean review and must
		// never render as one. Stated after the agent's comments merge, so
		// the reason can be honest about whether the findings list is empty.
		reason := "this change is entirely unexamined; the empty findings list below is not evidence of correctness"
		if len(res.Report.Findings) > 0 {
			reason = "no pane covers these files; the findings below come from the agent's review"
		}
		res.Report.Unknowns = append(res.Report.Unknowns, findings.Unknown{
			Substrate: "redline",
			Message:   fmt.Sprintf("none of the %d changed file(s) fall under any pane Redline currently ships", len(changed)),
			Reason:    reason,
		})
	}
	res.Report.Finalize()
	if review != nil {
		res.Report.MergeVerdicts(review.Verdicts)
	}
	findings.Sort(res.Report.Findings)

	res.Envelopes, res.ContextAbsent = resolveContext(&res.Report, configRoot, tgt.Dir, baseSHA, changed)
	res.Change = change.Build(repo, tgt, baseSHA, changed)
	covDir := originCoverageDir(opts.Dir, tgt)
	attachDiffCoverage(&res.Report, res.Change, tgt.Dir, covDir, cfg)
	res.LineCoverage = lineCoverageOverlay(tgt.Dir, covDir, res.Change)
	attachMutation(&res.Report, res.Change, roots, cfg)
	if review != nil {
		mergeMutationVerdicts(res.Report.Mutation, review.MutationVerdicts)
	}
	recordMutationSubstrate(&res.Report, res.Change, cfg)
	return res, nil
}

// resolveContext runs the language providers this repository configured and
// collects their envelopes.
//
// Nothing here is fatal. A provider that is missing, broken, or slow leaves a
// gap in the context the review gets, and the gap is recorded as an unknown
// so a later empty review can be told apart from one that had nothing to work
// with. Redline links no provider and knows none by name: the registry finds
// them by config file and by .redline.yml.
func resolveContext(rep *findings.Report, configRoot, observeRoot, baseSHA string, changed []string) ([]*envelope.Envelope, []string) {
	providers, err := provider.Detect(configRoot)
	if err != nil {
		rep.Unknowns = append(rep.Unknowns, findings.Unknown{
			Substrate: "redline/context",
			Message:   "a context provider is declared but could not be read, so its context is missing from the review",
			Reason:    err.Error(),
		})
	}
	var envs []*envelope.Envelope
	var absent []string
	for _, p := range providers {
		if len(p.Claimed(changed)) == 0 {
			continue
		}
		env, runErr := p.Run(observeRoot, baseSHA)
		if runErr != nil {
			absent = append(absent, fmt.Sprintf("%s (context): %v", p.Name, runErr))
			rep.Unknowns = append(rep.Unknowns, findings.Unknown{
				Substrate: "redline/context",
				Message:   fmt.Sprintf("context provider %s claimed files in this change but did not run", p.Name),
				Reason:    runErr.Error(),
			})
			continue
		}
		for _, note := range env.Notes {
			rep.Unknowns = append(rep.Unknowns, findings.Unknown{
				Substrate: "redline/context",
				Message:   fmt.Sprintf("%s could not fully resolve this change", p.Name),
				Reason:    note,
			})
		}
		if unknown := env.UnknownRoles(); len(unknown) > 0 {
			rep.Unknowns = append(rep.Unknowns, findings.Unknown{
				Substrate: "redline/context",
				Message:   fmt.Sprintf("%s tagged expansions with role(s) this Redline does not rank: %s", p.Name, strings.Join(unknown, ", ")),
				Reason:    "they are ranked last rather than dropped; a newer provider may want a newer Redline",
			})
		}
		envs = append(envs, env)
	}
	return envs, absent
}

// attachDiffCoverage computes the number that stands in for reading the tests.
// It needs the per-file diffs, so it runs after change.Build. When no harness
// coverage profile is configured, a missing profile is left off the report.
func attachDiffCoverage(rep *findings.Report, ch *change.Set, dir, originDir string, cfg *harness.Config) {
	if ch == nil || len(ch.Files) == 0 {
		return
	}
	changed := make([]cover.Changed, 0, len(ch.Files))
	changedPaths := make([]string, 0, len(ch.Files))
	goFiles := 0
	for _, f := range ch.Files {
		if filepath.Ext(f.Path) != ".go" {
			continue
		}
		goFiles++
		changedPaths = append(changedPaths, f.Path)
		changed = append(changed, cover.Changed{Path: f.Path, Added: cover.AddedLines(f.Diff)})
	}
	rep.Coverage.CoverableFiles = goFiles
	if goFiles == 0 {
		return
	}
	requires := cfg != nil && cfg.RequiresCoverage(changedPaths)
	rep.Coverage.Diff = cover.Compute(dir, changed)
	if rep.Coverage.Diff == nil && originDir != "" && originDir != dir {
		rep.Coverage.Diff = cover.Compute(originDir, changed)
	}
	if !requires {
		return
	}
	switch {
	case rep.Coverage.Diff == nil:
		reason := "looked for coverage.out, cover.out, coverage.txt and c.out; run the suite with -coverprofile to get this number"
		reason += "; or run redline with --prepare when produce is configured"
		rep.Unknowns = append(rep.Unknowns, findings.Unknown{
			Substrate: "redline/tests",
			Message:   fmt.Sprintf("no coverage profile was found, so whether any test executes the %d changed Go file(s) is unknown", goFiles),
			Reason:    reason,
		})
	case rep.Coverage.Diff.Stale:
		rep.Unknowns = append(rep.Unknowns, findings.Unknown{
			Substrate: "redline/tests",
			Message:   fmt.Sprintf("the coverage profile %s is older than a file in this change, so its number does not describe the code under review", rep.Coverage.Diff.Profile),
			Reason:    "re-run the suite with -coverprofile",
		})
	}
}

// attachMutation reads a configured gomutants report when present. Unconfigured
// mutation profiles are ignored entirely.
func attachMutation(rep *findings.Report, ch *change.Set, roots harness.Roots, cfg *harness.Config) {
	if ch == nil || len(ch.Files) == 0 || cfg == nil {
		return
	}
	changed := make([]mutation.Changed, 0, len(ch.Files))
	for _, f := range ch.Files {
		if filepath.Ext(f.Path) != ".go" {
			continue
		}
		changed = append(changed, mutation.Changed{Path: f.Path, Added: cover.AddedLines(f.Diff)})
	}
	if len(changed) == 0 {
		return
	}
	for _, rel := range cfg.MutationPaths() {
		root := harness.ArtifactRoot(roots, rel)
		if root == "" {
			continue
		}
		if res := mutation.Compute(root, changed); res != nil {
			rep.Mutation = res
			return
		}
	}
}

// mergeMutationVerdicts attaches an agent's judgments to surviving mutants by
// key, after attachMutation has stamped each survivor's key. A verdict whose key
// matches no survivor is dropped, the same way MergeVerdicts drops an unmatched
// finding fingerprint. findings.Verdict is translated to mutation's own type so
// the two packages stay decoupled.
func mergeMutationVerdicts(res *mutation.Result, verdicts map[string]findings.Verdict) {
	if res == nil || len(verdicts) == 0 {
		return
	}
	for i := range res.Survived {
		for j := range res.Survived[i].Mutants {
			m := &res.Survived[i].Mutants[j]
			if v, ok := verdicts[m.Key]; ok {
				m.Verdict = &mutation.Verdict{Ruling: v.Ruling, Rationale: v.Rationale, Source: string(findings.SourceLLM)}
			}
		}
	}
}

// recordMutationSubstrate states mutation in substrates[] so findings.json shows
// whether gomutants ran, the way the panes do. Mutation is not a pane, it reads
// a report rather than observing two revisions, but a reader should still see
// "mutation: ran", a not-applicable row that renders nowhere when it is not
// configured, and a confirmation when every mutant on the added lines was killed.
func recordMutationSubstrate(rep *findings.Report, ch *change.Set, cfg *harness.Config) {
	configured := cfg != nil && len(cfg.MutationPaths()) > 0
	st, conf := mutationSubstrate(rep.Mutation, configured, hasGoFile(ch))
	rep.Substrates = append(rep.Substrates, st)
	if conf != nil {
		rep.Confirmations = append(rep.Confirmations, *conf)
	}
}

// mutationSubstrate is the pure decision behind recordMutationSubstrate: given
// the diff-scoped result, whether a report is configured, and whether the change
// touches Go, it returns the substrate row and an optional all-killed
// confirmation. Split out so the states can be tested without a repository.
func mutationSubstrate(m *mutation.Result, configured, hasGo bool) (findings.SubstrateStatus, *findings.Confirmation) {
	const name = "redline/mutation"
	if m != nil {
		st := findings.SubstrateStatus{
			Name:   name,
			State:  findings.SubstrateRan,
			Detail: fmt.Sprintf("%s: %d killed, %d survived on this change's added lines", m.Report, m.Killed, m.Lived),
		}
		if m.Lived == 0 && m.Killed > 0 {
			return st, &findings.Confirmation{
				Substrate: name,
				Rule:      "mutation-all-killed",
				Message:   fmt.Sprintf("every mutant on this change's added lines was killed (%d)", m.Killed),
			}
		}
		return st, nil
	}
	if !configured {
		return findings.SubstrateStatus{
			Name:   name,
			State:  findings.SubstrateNotApplicable,
			Detail: "no gomutants report configured in .redline.yml",
		}, nil
	}
	detail := "the gomutants report covers none of this change's added lines"
	if !hasGo {
		detail = "this change touches no Go file to mutate"
	}
	return findings.SubstrateStatus{Name: name, State: findings.SubstrateSkipped, Detail: detail}, nil
}

func hasGoFile(ch *change.Set) bool {
	if ch == nil {
		return false
	}
	for _, f := range ch.Files {
		if strings.HasSuffix(f.Path, ".go") {
			return true
		}
	}
	return false
}

// originCoverageDir returns the origin checkout's root when the reviewed
// revision is that checkout's current HEAD, so a coverage profile living there
// can stand in for the pristine worktree's absent one. It returns "" for any
// other revision (an older commit, another branch, a PR): that profile would
// not describe the reviewed code, and absence stays the honest answer. It also
// returns "" for a working-tree target, whose Dir already is the origin root.
func originCoverageDir(dir string, tgt *target.Target) string {
	if dir == "" {
		dir = "."
	}
	orig, err := gitx.Open(dir)
	if err != nil || orig.Root == tgt.Dir {
		return ""
	}
	head, err := orig.Head()
	if err != nil || head != tgt.Head {
		return ""
	}
	return orig.Root
}

// lineCoverageOverlay reads per-line coverage for the changed Go files from the
// same profile the coverage pane used: the worktree first, then the origin
// checkout when reviewing its HEAD. Empty when there is no profile — the diff
// then renders with no coverage stripe, which is the honest "nobody measured".
func lineCoverageOverlay(dir, originDir string, ch *change.Set) map[string]map[int]bool {
	if ch == nil {
		return nil
	}
	prof := cover.Load(dir)
	if prof == nil && originDir != "" && originDir != dir {
		prof = cover.Load(originDir)
	}
	if prof == nil {
		return nil
	}
	out := map[string]map[int]bool{}
	for _, f := range ch.Files {
		if filepath.Ext(f.Path) != ".go" {
			continue
		}
		if lc := prof.LineCoverage(f.Path); len(lc) > 0 {
			out[f.Path] = lc
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	// The breaking-change diff ships; what is still missing for api files the
	// pane did not claim is spec linting and any comparison against the code
	// that serves the contract.
	{"api", "vacuum spec linting, spec-vs-handler and observed-vs-declared response checks are not built"},
	{"ui", "checks 15-16 (before/after route screenshots, console and network errors) are not built"},
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
		for _, area := range change.Areas(path) {
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

// ReapplyReview swaps the reviewer's own findings on a loaded session for a
// fresh review, so the report can be re-rendered without observing the change
// again.
//
// The reviewer's findings are removed by source and substrate rather than
// merged: re-running a review produces a new set, and appending would leave
// the previous run's remarks on the page forever. Every pane's finding is
// untouched, and so are the verdicts, which live in review.json and belong to
// whoever wrote them.
func ReapplyReview(res *Result, rev *findings.Review) {
	kept := res.Report.Findings[:0:0]
	for _, f := range res.Report.Findings {
		if f.Source == findings.SourceLLM && f.Substrate == reviewSubstrate {
			continue
		}
		kept = append(kept, f)
	}
	res.Report.Findings = kept
	res.Report.Agent = nil
	if rev != nil {
		res.Report.Findings = append(res.Report.Findings, rev.CommentFindings()...)
		if rev.Overview != "" || len(rev.Files) > 0 {
			res.Report.Agent = &findings.AgentReview{Overview: rev.Overview, Files: rev.Files}
		}
	}
	res.Report.Finalize()
	if rev != nil {
		res.Report.MergeVerdicts(rev.Verdicts)
		mergeMutationVerdicts(res.Report.Mutation, rev.MutationVerdicts)
	}
	findings.Sort(res.Report.Findings)
}

// reviewSubstrate is the name every finding that came from a reviewer rather
// than a pane carries.
const reviewSubstrate = "redline/review"
