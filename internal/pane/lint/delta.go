package lint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ccason/redline/internal/cover"
	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/pane"
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
	tools    []tool
	detected bool

	// configChanged is set by Scope when the change touches a lint config. A
	// delta partly earned by editing the config has to say so: fewer findings
	// because a rule was turned off is not the same fact as fixed code.
	configChanged bool
}

// Name implements pane.Pane.
func (p *Delta) Name() string { return DeltaSubstrate }

func (p *Delta) detect() []tool {
	if !p.detected {
		p.tools = detect(p.Repo.Root)
		p.detected = true
	}
	return p.tools
}

// Scope is the changed files a configured linter covers, plus any changed
// lint config. No configured linter means an empty scope: the pane then reads
// as skipped, which is the honest state for a repository that has not opted
// into any linter Redline can run.
func (p *Delta) Scope(changed []string) []string {
	tools := p.detect()
	if len(tools) == 0 {
		return nil
	}
	var out []string
	for _, path := range changed {
		if isLintConfig(path) {
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
// a tool that fails there darks the pane, because no delta can be computed.
// The base side records failures instead: an old revision that no longer
// lints (a config newer than the code, missing dependencies in a bare
// worktree) must degrade the answer, not erase it. Diff handles that case.
func (p *Delta) Observe(rev pane.Revision) (pane.Observation, error) {
	dir := p.Repo.Root
	if rev.Name == "base" {
		var err error
		dir, err = p.Repo.AddWorktree(rev.Rev)
		if err != nil {
			return nil, fmt.Errorf("checking out the base revision to lint it: %w", err)
		}
	}
	snap := &snapshot{Rev: rev.Rev}
	for _, t := range p.detect() {
		issues, err := t.run(dir)
		if err != nil {
			if rev.Name != "base" {
				return nil, fmt.Errorf("%s at head: %v", t.name, err)
			}
			snap.Runs = append(snap.Runs, toolRun{Tool: t.name, Err: err.Error()})
			continue
		}
		snap.Runs = append(snap.Runs, toolRun{Tool: t.name, Issues: issues})
	}
	return snap, nil
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

	baseByTool := map[string]toolRun{}
	for _, r := range base.Runs {
		baseByTool[r.Tool] = r
	}

	var introduced []Issue
	resolved := 0
	var toolNames []string
	for _, hr := range head.Runs {
		toolNames = append(toolNames, hr.Tool)
		br := baseByTool[hr.Tool]
		if br.Err != "" {
			// The base run failed, so there is no delta for this tool. Degrade
			// to head issues on lines this change added: still precise, still
			// only about the change, and the degradation is stated.
			onAdded := p.issuesOnAddedLines(base.Rev, hr.Issues)
			introduced = append(introduced, onAdded...)
			res.Unknowns = append(res.Unknowns, findings.Unknown{
				Substrate: DeltaSubstrate,
				Message:   fmt.Sprintf("%s could not run at the base revision, so resolved findings are unknown and introduced ones are limited to added lines", hr.Tool),
				Reason:    br.Err,
			})
			continue
		}
		in, out := diffIssues(br.Issues, hr.Issues)
		introduced = append(introduced, in...)
		resolved += out
	}

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
	if len(introduced) == 0 && allBaseRan(base) {
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

func severity(s string) findings.Severity {
	if s == "info" {
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
