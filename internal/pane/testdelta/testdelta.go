// Package testdelta reports cheap, deterministic facts about how a change treats
// its tests, read from the diff alone: source that changed with no test beside
// it, tests newly skipped or focused, and assertions removed. These are info
// findings, signals for the reviewer to weigh, not gates. They sit next to
// coverage (did a test run this line) and mutation (would a test catch it
// breaking) with the third question the diff can answer for free: did the tests
// move with the code.
package testdelta

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/pane"
)

// Substrate is the pane's name in the findings schema.
const Substrate = "redline/test-delta"

// Pane reports test-facing facts a diff shows on its own.
type Pane struct {
	Repo *gitx.Repo

	// changed is the whole changed set, kept for the source-without-test fact,
	// which is about files this pane does not otherwise open. scoped is the code
	// files it inspects line by line.
	changed []string
	scoped  []string
}

// codeExts are the files this pane inspects for skips and assertion changes, and
// the ones whose packages the source-without-test fact reasons about.
var codeExts = []string{".go", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".py"}

// Name implements pane.Pane.
func (p *Pane) Name() string { return Substrate }

// Scope selects the changed code files. It also stashes the full changed set:
// the source-without-test fact must fire precisely when no test changed, and a
// pane with an empty scope never runs, so scope must be non-empty whenever code
// changed at all.
func (p *Pane) Scope(changed []string) []string {
	p.changed = changed
	var out []string
	for _, f := range changed {
		if hasExt(f, codeExts) {
			out = append(out, f)
		}
	}
	p.scoped = out
	return out
}

type observation struct{ Rev string }

func (o *observation) ID() string { return "test-delta@" + o.Rev }

// Observe records the revision. Like the suppressions pane, the real work reads
// the diff in Diff, because every fact here is a property of added or removed
// lines rather than of a whole-file snapshot.
func (p *Pane) Observe(rev pane.Revision) (pane.Observation, error) {
	return &observation{Rev: rev.Rev}, nil
}

// Diff emits the test-delta facts.
func (p *Pane) Diff(before, after pane.Observation) (pane.Result, error) {
	base, ok := before.(*observation)
	if !ok {
		return pane.Result{}, fmt.Errorf("test-delta: base observation is %T", before)
	}
	res := pane.Result{Evidence: map[string]pane.Artifact{}}
	var lines []string

	p.sourceWithoutTest(&res, &lines)
	p.skipsAdded(base.Rev, &res, &lines)
	p.assertionsRemoved(base.Rev, &res, &lines)

	if len(res.Findings) == 0 {
		res.Confirmations = append(res.Confirmations, findings.Confirmation{
			Substrate: Substrate,
			Rule:      "tests-move-with-code",
			Message:   "every changed package changed a test, none were skipped or focused, and no assertions were removed",
		})
	}
	res.Render = pane.Render{
		Title:   "Tests",
		Summary: fmt.Sprintf("%d test-delta fact(s) from the diff", len(res.Findings)),
		Lines:   lines,
	}
	return res, nil
}

// sourceWithoutTest names each Go package this change edits without touching a
// test in it. Go only: its one-test-file-per-package convention makes "a test in
// this package changed" a clean question, which JS and Python layouts do not
// answer as cleanly.
func (p *Pane) sourceWithoutTest(res *pane.Result, lines *[]string) {
	src := map[string]bool{}
	test := map[string]bool{}
	for _, f := range p.changed {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		dir := path.Dir(f)
		if strings.HasSuffix(f, "_test.go") {
			test[dir] = true
		} else {
			src[dir] = true
		}
	}
	var dirs []string
	for dir := range src {
		if !test[dir] {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		res.Findings = append(res.Findings, findings.Finding{
			Anchor:    &findings.Anchor{Kind: "package", ID: dir},
			Rule:      "source-without-test",
			Substrate: Substrate,
			Category:  findings.CategoryTests,
			Severity:  findings.SeverityInfo,
			Message:   fmt.Sprintf("code in %s changed but no test in that package did", dir),
			Context:   "Not necessarily a problem: an existing test may already cover the change. Worth checking the new behavior has one.",
		})
		*lines = append(*lines, "~ "+dir+" (no test changed)")
	}
}

// skipsAdded reports a skip or focus directive on a line this change adds to a
// test file. A skip quietly removes a test from the run; a focus (.only, fit)
// quietly removes every other test in the file.
func (p *Pane) skipsAdded(baseRev string, res *pane.Result, lines *[]string) {
	for _, f := range p.scoped {
		if !isTestFile(f) {
			continue
		}
		added := map[int]bool{}
		for _, n := range cover.AddedLines(p.Repo.DiffPath(baseRev, f)) {
			added[n] = true
		}
		if len(added) == 0 {
			continue
		}
		content := p.Repo.File("", f)
		if content == "" {
			continue
		}
		for n, line := range strings.Split(content, "\n") {
			lineNo := n + 1
			if !added[lineNo] {
				continue
			}
			if what := skipKind(line); what != "" {
				res.Findings = append(res.Findings, findings.Finding{
					File:      f,
					Line:      lineNo,
					Rule:      "test-skip-added",
					Substrate: Substrate,
					Category:  findings.CategoryTests,
					Severity:  findings.SeverityInfo,
					Message:   "this change " + what,
					Observed:  strings.TrimSpace(line),
				})
				*lines = append(*lines, fmt.Sprintf("+ %s:%d %s", f, lineNo, what))
			}
		}
	}
}

// assertionsRemoved reports a test file that ends the change with fewer
// assertion-like lines than it started. Netting added against removed keeps a
// pure move or rename from reading as a loss; only a real drop is reported, and
// per file rather than per line, because a deleted line has no home in the head
// tree to anchor on.
func (p *Pane) assertionsRemoved(baseRev string, res *pane.Result, lines *[]string) {
	for _, f := range p.scoped {
		if !isTestFile(f) {
			continue
		}
		diff := p.Repo.DiffPath(baseRev, f)
		if diff == "" {
			continue
		}
		added, removed := 0, 0
		for _, l := range strings.Split(diff, "\n") {
			if strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---") {
				continue
			}
			switch {
			case strings.HasPrefix(l, "+"):
				if assertionRe.MatchString(l[1:]) {
					added++
				}
			case strings.HasPrefix(l, "-"):
				if assertionRe.MatchString(l[1:]) {
					removed++
				}
			}
		}
		if removed > added {
			res.Findings = append(res.Findings, findings.Finding{
				File:      f,
				Rule:      "assertions-removed",
				Substrate: Substrate,
				Category:  findings.CategoryTests,
				Severity:  findings.SeverityInfo,
				Message:   fmt.Sprintf("this change removes %d more assertion-like line(s) than it adds in this test", removed-added),
				Context:   "Assertions are what let a test fail. A net drop can mean a weaker test; check it is intended.",
			})
			*lines = append(*lines, fmt.Sprintf("- %s (-%d assertion-like line(s))", f, removed-added))
		}
	}
}

// skipKind classifies a skip or focus directive, or returns "" for neither.
func skipKind(line string) string {
	switch {
	case focusRe.MatchString(line):
		return "focuses tests, so the others in the file will not run"
	case skipRe.MatchString(line):
		return "skips a test"
	}
	return ""
}

var (
	skipRe = regexp.MustCompile(`\b[tbsf]\.Skip(Now|f)?\s*\(` + // Go t.Skip / b.Skip
		`|\b(it|test|describe|context)\.skip\b` + // JS/TS .skip
		`|\bx(it|describe)\b` + // JS/TS xit / xdescribe
		`|@(pytest\.mark\.skip|unittest\.skip)` + // Python decorators
		`|\bpytest\.skip\s*\(|\bself\.skipTest\s*\(`)

	focusRe = regexp.MustCompile(`\b(it|test|describe|context)\.only\b|\bf(it|describe)\b`)

	assertionRe = regexp.MustCompile(`\b(assert|require)\.\w+` + // Go testify
		`|\bt\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow)\b` + // Go testing
		`|\bexpect\s*\(|\.should\b` + // JS/TS
		`|\bassert(Equal|True|False|Nil|NotNil|Error|NoError)?\s*\(` + // xUnit-style
		`|\bself\.assert\w+\s*\(`) // Python unittest
)

// isTestFile reports whether a path is a test file in a supported language.
func isTestFile(f string) bool {
	base := path.Base(f)
	switch {
	case strings.HasSuffix(f, "_test.go"):
		return true
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"):
		return true
	case strings.Contains(base, ".test.") || strings.Contains(base, ".spec."):
		return true
	case strings.Contains(f, "__tests__/"):
		return true
	}
	return false
}

func hasExt(f string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(f, e) {
			return true
		}
	}
	return false
}
