package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/pane"
)

// SuppressSubstrate is the suppression pane's name in the findings schema.
const SuppressSubstrate = "redline/suppressions"

// Suppressions collects every lint-silencing directive this change adds. It is
// the highest-signal lint fact a diff carries: the author explicitly told a
// linter to be quiet, and the reviewer should see where and about what.
//
// Only directives on lines the diff adds are reported. A directive that
// merely moved keeps its old justification; one the change introduces is the
// one that needs a fresh look. Findings are info severity: many silencings
// are legitimate, and the point is visibility, not a gate — a repository that
// wants them to block can list info in its profile.
type Suppressions struct {
	Repo *gitx.Repo

	// scoped is what Scope selected; Diff walks it.
	scoped []string
}

// directive matches one silencing convention. Rules captures the rule list
// when the convention names one.
type directive struct {
	kind  string
	re    *regexp.Regexp
	rules int // submatch index of the rule list, 0 for none
}

var directives = []directive{
	{kind: "nolint", re: regexp.MustCompile(`//\s*nolint(?::([\w\-, ]+))?`), rules: 1},
	{kind: "eslint-disable", re: regexp.MustCompile(`eslint-disable(?:-next-line|-line)?\s*([\w\-/@, ]*)`), rules: 1},
	{kind: "ts-ignore", re: regexp.MustCompile(`@ts-ignore`)},
	{kind: "ts-expect-error", re: regexp.MustCompile(`@ts-expect-error`)},
	{kind: "ts-nocheck", re: regexp.MustCompile(`@ts-nocheck`)},
	{kind: "noqa", re: regexp.MustCompile(`#\s*noqa(?::?\s*([\w, ]+))?`), rules: 1},
	{kind: "type-ignore", re: regexp.MustCompile(`#\s*type:\s*ignore`)},
	{kind: "pylint-disable", re: regexp.MustCompile(`#\s*pylint:\s*disable=([\w\-, ]+)`), rules: 1},
	{kind: "rust-allow", re: regexp.MustCompile(`#!?\[allow\(([^)]*)\)\]`), rules: 1},
}

// suppressible are the extensions the directive table can appear in.
var suppressible = []string{
	".go", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".vue", ".svelte",
	".py", ".rs",
}

// Name implements pane.Pane.
func (p *Suppressions) Name() string { return SuppressSubstrate }

// Scope is the changed files a directive could appear in.
func (p *Suppressions) Scope(changed []string) []string {
	var out []string
	for _, path := range changed {
		if hasSuffixAny(path, suppressible...) {
			out = append(out, path)
		}
	}
	p.scoped = out
	return out
}

// suppressObservation records the revision; the pane model observes whole
// revisions, but a directive is a fact about specific lines, so the real work
// reads the diff per scoped file in Diff.
type suppressObservation struct{ Rev string }

func (s *suppressObservation) ID() string {
	rev := s.Rev
	if rev == "" {
		rev = "worktree"
	}
	return "suppressions@" + rev
}

// Observe records the revision; the comparison happens in Diff, against the
// diff itself, because "added by this change" is a property of lines rather
// than of whole-file directive counts.
func (p *Suppressions) Observe(rev pane.Revision) (pane.Observation, error) {
	return &suppressObservation{Rev: rev.Rev}, nil
}

// Diff reports every directive sitting on a line this change added.
func (p *Suppressions) Diff(before, after pane.Observation) (pane.Result, error) {
	base, ok := before.(*suppressObservation)
	if !ok {
		return pane.Result{}, fmt.Errorf("suppressions: base observation is %T", before)
	}
	res := pane.Result{Evidence: map[string]pane.Artifact{}}
	var lines []string

	paths := append([]string(nil), p.scoped...)
	sort.Strings(paths)
	for _, path := range paths {
		added := map[int]bool{}
		for _, n := range cover.AddedLines(p.Repo.DiffPath(base.Rev, path)) {
			added[n] = true
		}
		if len(added) == 0 {
			continue
		}
		content := p.Repo.File("", path)
		if content == "" {
			continue
		}
		for n, line := range strings.Split(content, "\n") {
			lineNo := n + 1
			if !added[lineNo] {
				continue
			}
			for _, d := range directives {
				m := d.re.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				silenced := "its findings on this line"
				if d.rules > 0 {
					// An eslint directive can carry a justification after "--";
					// that text is not a rule list.
					rules, _, _ := strings.Cut(m[d.rules], "--")
					if rules = strings.TrimSpace(rules); rules != "" {
						silenced = rules
					}
				}
				res.Findings = append(res.Findings, findings.Finding{
					File:      path,
					Line:      lineNo,
					Rule:      "suppression-added",
					Substrate: SuppressSubstrate,
					Category:  findings.CategoryLint,
					Severity:  findings.SeverityInfo,
					Message:   fmt.Sprintf("this change adds a %s directive silencing %s", d.kind, silenced),
					Observed:  strings.TrimSpace(line),
					Context:   "The author told the linter to be quiet here. Worth checking that the silencing is justified, since whatever it hides will never appear in lint output again.",
				})
				lines = append(lines, fmt.Sprintf("+ %s:%d %s", path, lineNo, d.kind))
				break
			}
		}
	}
	if len(res.Findings) == 0 {
		res.Confirmations = append(res.Confirmations, findings.Confirmation{
			Substrate: SuppressSubstrate,
			Rule:      "no-suppressions-added",
			Message:   "this change adds no lint-silencing directive",
		})
	}
	res.Render = pane.Render{
		Title:   "Suppressions",
		Summary: fmt.Sprintf("%d lint-silencing directive(s) added by this change", len(res.Findings)),
		Lines:   lines,
	}
	return res, nil
}
