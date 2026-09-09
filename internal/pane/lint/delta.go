package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/harness"
	"github.com/chrisophus/redline/internal/pane"
)

// DeltaSubstrate is the lint-delta pane's name in the findings schema.
const DeltaSubstrate = "redline/lint"

// Delta runs the repository's configured linters at the base revision and at
// head, and reports the difference. This is the first pane that executes
// tools rather than reading git: the base side runs in a detached worktree at
// the base commit, the head side in the tree under review.
type Delta struct {
	Repo *gitx.Repo

	// tools caches detection, which reads the head tree. Detection is by the
	// head's config: the head defines what this repository lints with.
	tools     []tool
	detected  bool
	detectErr error

	// scoped is the changed files this pane examines, kept so Diff can run a
	// differ-kind tool (oasdiff) once per scoped file it covers.
	scoped []string

	// configChanged is set by Scope when the change touches a lint config. A
	// delta partly earned by editing the config has to say so: fewer findings
	// because a rule was turned off is not the same fact as fixed code.
	configChanged bool

	// Harness carries the run's .redline.yml config so this pane can run
	// harness.worktree steps (for example make stub-ui) in the base and head
	// trees before the linters read them. Set by run.Run, nil when there is no
	// config. Prepared is the run-scoped dedup set shared with the rest of the
	// run so a produce root runs at most once.
	Harness     *harness.Config
	HarnessRoot string
	Prepared    map[string]bool
}

// Name implements pane.Pane.
func (p *Delta) Name() string { return DeltaSubstrate }

func (p *Delta) detect() ([]tool, error) {
	if !p.detected {
		p.tools, p.detectErr = detect(p.Repo.Root)
		p.detected = true
	}
	return p.tools, p.detectErr
}

// Scope is the changed files a configured tool covers, plus any changed lint
// config. No configured tool means an empty scope: the pane then reads as
// skipped, the honest state for a repository that has opted into no linter
// Redline can run. A .redline.yml that fails to parse must fail the pane
// visibly, not remove it: detection still returns the built-in tools, whose
// coverage scopes files so Observe runs and reports the parse error; and if nothing
// else lands in scope, the whole change does — an unreadable config means
// nobody knows which files its tools cover.
func (p *Delta) Scope(changed []string) []string {
	tools, err := p.detect()
	var out []string
	for _, path := range changed {
		if isLintConfig(path) || isRedlineConfig(path) {
			out = append(out, path)
			p.configChanged = true
			continue
		}
		for _, t := range tools {
			if t.covers(path) {
				out = append(out, path)
				break
			}
		}
	}
	if err != nil && len(out) == 0 && len(changed) > 0 {
		out = append(out, changed...)
	}
	p.scoped = out
	return out
}

// snapshot is one revision's lint state: per tool, its issues or its failure.
type snapshot struct {
	Rev  string
	Runs []toolRun
}

type toolRun struct {
	Tool   string
	Issues []Issue
	Err    string
}

// ID implements pane.Observation.
func (s *snapshot) ID() string {
	rev := s.Rev
	if rev == "" {
		rev = "worktree"
	}
	return "lint@" + rev
}

// Observe runs every detected tool at one revision. The head side is strict —
// a tool that fails there fails the pane, because no delta can be computed.
// The base side records failures instead: an old revision that no longer
// lints (a config newer than the code, missing dependencies in a bare
// worktree) must degrade the answer, not erase it. Diff handles that case.
func (p *Delta) Observe(rev pane.Revision) (pane.Observation, error) {
	tools, err := p.detect()
	if err != nil {
		return nil, err
	}
	dir := p.Repo.Root
	if rev.Name == "base" {
		var werr error
		dir, werr = p.Repo.AddWorktree(rev.Rev)
		if werr != nil {
			return nil, fmt.Errorf("checking out the base revision to lint it: %w", werr)
		}
	}
	if p.Harness != nil {
		if _, err := harness.PrepareWorktree(dir, p.HarnessRoot, p.scoped, p.Harness, p.Prepared); err != nil {
			return nil, err
		}
	}
	snap := &snapshot{Rev: rev.Rev}
	for _, t := range tools {
		// A differ tool computes its own delta and runs once, in Diff.
		if t.isDiffer() {
			continue
		}
		// A baseline-file tool takes its "already existed" set from a
		// committed artifact, not a run at the base revision.
		if rev.Name == "base" && t.custom != nil && t.custom.Baseline.Mode == "file" {
			continue
		}
		if !p.shouldRunTool(t) {
			continue
		}
		issues, rerr := t.run(dir)
		if rerr != nil {
			if rev.Name != "base" {
				return nil, fmt.Errorf("%s at head: %v", t.name, rerr)
			}
			snap.Runs = append(snap.Runs, toolRun{Tool: t.name, Err: rerr.Error()})
			continue
		}
		if issues != nil {
			issues = normalizeIssues(dir, issues, toolScope(t, p.scoped))
		}
		snap.Runs = append(snap.Runs, toolRun{Tool: t.name, Issues: issues})
	}
	return snap, nil
}

// shouldRunTool limits lint runs to tools whose coverage intersects the
// changed code paths in scope. A head failure from eslint on a Go-only PR is
// noise; skipping the tool is the honest state.
func (p *Delta) shouldRunTool(t tool) bool {
	if t.isDiffer() {
		return false
	}
	codePaths := p.codePathsInScope()
	if len(codePaths) == 0 {
		if len(p.scoped) == 0 {
			// Observe without a prior Scope (tests, full-tree runs): run all tools.
			return true
		}
		return false
	}
	for _, path := range codePaths {
		if t.covers(path) {
			return true
		}
	}
	return false
}

func (p *Delta) codePathsInScope() []string {
	var out []string
	for _, path := range p.scoped {
		if isLintConfig(path) || isRedlineConfig(path) {
			continue
		}
		out = append(out, path)
	}
	return out
}

func toolScope(t tool, scoped []string) []string {
	if t.custom != nil && len(t.custom.Scope) > 0 {
		return t.custom.Scope
	}
	return scoped
}

// Diff reports the issues head has that base does not, and counts the ones
// base had that head resolved. Identity is the line-free fingerprint, matched
// by count, so code that moved does not read as new violations.
func (p *Delta) Diff(before, after pane.Observation) (pane.Result, error) {
	base, ok := before.(*snapshot)
	if !ok {
		return pane.Result{}, fmt.Errorf("lint: base observation is %T", before)
	}
	head, ok := after.(*snapshot)
	if !ok {
		return pane.Result{}, fmt.Errorf("lint: head observation is %T", after)
	}
	evidence := []string{base.ID(), head.ID()}
	res := pane.Result{Evidence: map[string]pane.Artifact{}}

	tools, _ := p.detect()
	customByName := map[string]*ToolConfig{}
	for _, t := range tools {
		if t.custom != nil {
			customByName[t.name] = t.custom
		}
	}

	baseByTool := map[string]toolRun{}
	for _, r := range base.Runs {
		baseByTool[r.Tool] = r
	}

	var introduced []Issue
	resolved := 0
	anyDegraded := false
	var toolNames []string
	var toolStatuses []findings.ToolStatus
	for _, hr := range head.Runs {
		toolNames = append(toolNames, hr.Tool)

		// The "already existed" set is either the base run, or — for a
		// baseline-file tool — the committed baseline artifact.
		var baseIssues []Issue
		degraded, degradeMsg, reason := false, "", ""
		if cfg := customByName[hr.Tool]; cfg != nil && cfg.Baseline.Mode == "file" {
			bi, err := p.baselineIssues(*cfg)
			if err != nil {
				degraded = true
				degradeMsg = fmt.Sprintf("%s baseline %q could not be read, so resolved findings are unknown and introduced ones are limited to added lines", hr.Tool, cfg.Baseline.File)
				reason = err.Error()
			} else {
				baseIssues = bi
			}
		} else {
			br := baseByTool[hr.Tool]
			if br.Err != "" {
				degraded = true
				degradeMsg = fmt.Sprintf("%s could not run at the base revision, so resolved findings are unknown and introduced ones are limited to added lines", hr.Tool)
				reason = br.Err
			} else {
				baseIssues = br.Issues
			}
		}

		if degraded {
			// No comparable base, so there is no delta for this tool. Degrade
			// to head issues on lines this change added: still precise, still
			// only about the change, and the degradation is stated.
			anyDegraded = true
			onAdded := p.issuesOnAddedLines(base.Rev, hr.Issues)
			introduced = append(introduced, onAdded...)
			res.Unknowns = append(res.Unknowns, findings.Unknown{
				Substrate: DeltaSubstrate,
				Message:   degradeMsg,
				Reason:    reason,
			})
			toolStatuses = append(toolStatuses, findings.ToolStatus{Name: hr.Tool, Status: "degraded", Detail: reason})
			continue
		}
		in, out := diffIssues(baseIssues, hr.Issues)
		introduced = append(introduced, in...)
		resolved += out
		toolStatuses = append(toolStatuses, findings.ToolStatus{Name: hr.Tool, Status: "ran"})
	}

	// Differ tools run once here, comparing each scoped file at base and head
	// directly; everything they report is introduced by this change.
	for _, t := range tools {
		if !t.isDiffer() {
			continue
		}
		toolNames = append(toolNames, t.name)
		files := p.differFiles(t)
		issues, unknowns, err := p.runDiffer(*t.custom, base.Rev, files)
		if err != nil {
			return pane.Result{}, fmt.Errorf("%s: %w", t.name, err)
		}
		if len(unknowns) > 0 {
			anyDegraded = true
			res.Unknowns = append(res.Unknowns, unknowns...)
		}
		introduced = append(introduced, issues...)
		st := findings.ToolStatus{Name: t.name, Status: "ran"}
		if len(unknowns) > 0 {
			st.Status = "degraded"
			st.Detail = "some scoped files had no comparable base or head side to diff"
		}
		toolStatuses = append(toolStatuses, st)
	}

	introduced = p.filterIntroduced(base.Rev, introduced)

	sort.SliceStable(introduced, func(i, j int) bool {
		a, b := introduced[i], introduced[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})

	for _, issue := range introduced {
		res.Findings = append(res.Findings, findings.Finding{
			File:      issue.File,
			Line:      issue.Line,
			Rule:      issue.Tool + "/" + issue.Rule,
			Substrate: DeltaSubstrate,
			Category:  findings.CategoryLint,
			Severity:  severity(issue.Severity),
			Message:   fmt.Sprintf("this change introduces a %s violation: %s", issue.Rule, issue.Message),
			Evidence:  evidence,
			Expected:  "no new linter findings against the base revision",
			Observed:  fmt.Sprintf("%s reports %s at head and not at base", issue.Tool, issue.Rule),
		})
	}

	if resolved > 0 {
		res.Confirmations = append(res.Confirmations, findings.Confirmation{
			Substrate: DeltaSubstrate,
			Rule:      "lint-resolved",
			Message:   fmt.Sprintf("this change resolves %d linter finding(s) present at the base revision", resolved),
		})
	}
	// The clean confirmation asserts a comparison happened for every tool.
	// A degraded tool — base run failed, baseline unreadable, a differ file
	// it could not compare — already stated an unknown, and a confirmation
	// beside it would claim the comparison that never ran.
	if len(introduced) == 0 && !anyDegraded && allBaseRan(base) {
		res.Confirmations = append(res.Confirmations, findings.Confirmation{
			Substrate: DeltaSubstrate,
			Rule:      "lint-clean-delta",
			Message:   fmt.Sprintf("no new linter finding against the base revision (%s)", strings.Join(toolNames, ", ")),
		})
	}
	res.Render = renderDelta(toolNames, introduced, resolved)
	if p.configChanged {
		res.Render.Summary += " — the change also edits a lint config, so part of this delta may be configuration rather than code"
	}
	res.Tools = toolStatuses
	return res, nil
}

// issuesOnAddedLines keeps the issues sitting on a line this change added, for
// the degraded head-only path when the base could not be linted.
func (p *Delta) issuesOnAddedLines(baseRev string, issues []Issue) []Issue {
	added := map[string]map[int]bool{}
	var out []Issue
	for _, issue := range issues {
		lines, seen := added[issue.File]
		if !seen {
			lines = map[int]bool{}
			for _, n := range cover.AddedLines(p.Repo.DiffPath(baseRev, issue.File)) {
				lines[n] = true
			}
			added[issue.File] = lines
		}
		if lines[issue.Line] {
			out = append(out, issue)
		}
	}
	return out
}

// filterIntroduced drops warning- and info-tier findings that sit on lines this
// change did not touch. Errors (for example gorefactor file-size) stay: they
// describe the tree as it is now, not a stylistic nit on untouched context.
func (p *Delta) filterIntroduced(baseRev string, introduced []Issue) []Issue {
	if len(introduced) == 0 {
		return introduced
	}
	changedFiles := map[string]bool{}
	for _, path := range p.codePathsInScope() {
		changedFiles[path] = true
	}
	var out []Issue
	for _, issue := range introduced {
		if issue.Severity == "error" {
			out = append(out, issue)
			continue
		}
		if issue.Line <= 0 {
			if changedFiles[issue.File] {
				out = append(out, issue)
			}
			continue
		}
		if len(p.issuesOnAddedLines(baseRev, []Issue{issue})) > 0 {
			out = append(out, issue)
		}
	}
	return out
}

// baselineIssues parses a baseline-file tool's committed artifact — the same
// output shape the tool always produces — into the issue set treated as
// already present.
func (p *Delta) baselineIssues(cfg ToolConfig) ([]Issue, error) {
	raw := p.Repo.File("", cfg.Baseline.File)
	if raw == "" {
		return nil, fmt.Errorf("baseline file %q is empty or missing", cfg.Baseline.File)
	}
	issues, err := parseToolOutput(raw, cfg)
	if err != nil {
		return nil, err
	}
	return normalizeIssues(p.Repo.Root, issues, cfg.Scope), nil
}

// differFiles is the scoped changed files a differ tool covers.
func (p *Delta) differFiles(t tool) []string {
	var out []string
	for _, path := range p.scoped {
		if t.covers(path) {
			out = append(out, path)
		}
	}
	return out
}

// runDiffer invokes a differ tool once per file it covers, substituting the
// file's base-revision content (materialized to a temp file) for {{base}} and
// its path in the tree under review for {{head}}. A file with no base content
// (added by this change) or no head file (deleted by it) has no pair to
// compare: the tool would choke on the missing side and fail the whole pane,
// so the file is skipped and the skip stated as an unknown instead.
func (p *Delta) runDiffer(cfg ToolConfig, baseRev string, files []string) ([]Issue, []findings.Unknown, error) {
	var all []Issue
	var unknowns []findings.Unknown
	for _, path := range files {
		baseContent := p.Repo.File(baseRev, path)
		headPath := filepath.Join(p.Repo.Root, path)
		if _, err := os.Stat(headPath); err != nil {
			unknowns = append(unknowns, findings.Unknown{
				Substrate: DeltaSubstrate,
				Message:   fmt.Sprintf("%s could not compare %s: the file no longer exists at head", cfg.Name, path),
				Reason:    "deleted files have no head side to diff",
			})
			continue
		}
		if baseContent == "" {
			unknowns = append(unknowns, findings.Unknown{
				Substrate: DeltaSubstrate,
				Message:   fmt.Sprintf("%s could not compare %s: the file has no content at the base revision", cfg.Name, path),
				Reason:    "a file added by this change has no base side to diff",
			})
			continue
		}
		tmp, err := os.CreateTemp("", "redline-base-*"+filepath.Ext(path))
		if err != nil {
			return nil, nil, err
		}
		tmpPath := tmp.Name()
		defer func() { _ = os.Remove(tmpPath) }()
		if _, err := tmp.WriteString(baseContent); err != nil {
			_ = tmp.Close()
			return nil, nil, err
		}
		if err := tmp.Close(); err != nil {
			return nil, nil, err
		}
		args := substituteArgs(cfg.Args, tmpPath, headPath)
		stdout, stderr, exit, err := runTool(p.Repo.Root, cfg.Command, args...)
		if err != nil {
			return nil, nil, err
		}
		if !exitOK(exit, cfg.OKExitCodes) {
			return nil, nil, fmt.Errorf("exited %d: %s", exit, firstLine(stderr))
		}
		issues, err := parseToolOutput(stdout, cfg)
		if err != nil {
			return nil, nil, err
		}
		all = append(all, normalizeIssues(p.Repo.Root, issues, cfg.Scope)...)
	}
	return all, unknowns, nil
}

// substituteArgs replaces the {{base}} and {{head}} placeholders in a differ
// tool's args with the two file paths.
func substituteArgs(args []string, base, head string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		switch a {
		case "{{base}}":
			out[i] = base
		case "{{head}}":
			out[i] = head
		default:
			out[i] = a
		}
	}
	return out
}

// diffIssues is the multiset comparison: introduced issues (with head line
// numbers) and the count of resolved ones.
func diffIssues(base, head []Issue) (introduced []Issue, resolved int) {
	baseCount := map[string]int{}
	for _, i := range base {
		baseCount[fingerprint(i)]++
	}
	remaining := map[string]int{}
	for k, v := range baseCount {
		remaining[k] = v
	}
	for _, i := range head {
		fp := fingerprint(i)
		if remaining[fp] > 0 {
			remaining[fp]--
			continue
		}
		introduced = append(introduced, i)
	}
	for _, v := range remaining {
		resolved += v
	}
	return introduced, resolved
}

func allBaseRan(base *snapshot) bool {
	for _, r := range base.Runs {
		if r.Err != "" {
			return false
		}
	}
	return true
}

// severity maps a tool's severity string to Redline's. golangci-lint and
// eslint issues in this package are always "warning" or "info"; gorefactor
// also reports "error" (e.g. file-size over its limit), which is real:
// nothing here downgrades it.
func severity(s string) findings.Severity {
	switch s {
	case "error":
		return findings.SeverityError
	case "info":
		return findings.SeverityInfo
	}
	return findings.SeverityWarning
}

func renderDelta(tools []string, introduced []Issue, resolved int) pane.Render {
	lines := make([]string, 0, len(introduced))
	for _, i := range introduced {
		lines = append(lines, fmt.Sprintf("+ %s:%d %s (%s)", i.File, i.Line, i.Rule, i.Tool))
	}
	summary := fmt.Sprintf("%d finding(s) introduced, %d resolved (%s)",
		len(introduced), resolved, strings.Join(tools, ", "))
	return pane.Render{Title: "Lint delta", Summary: summary, Lines: lines}
}
