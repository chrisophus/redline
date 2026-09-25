// Package testdelta reports cheap, deterministic facts about how a change treats
// its tests, read from the diff alone: source that changed with no test beside
// it, tests newly skipped or focused, and assertions removed. These are info
// findings, signals for the reviewer to weigh, not gates. They complement
// coverage (did a test run this line) and mutation (would a test catch it
// breaking) by answering the third question the diff can answer for free: did
// the tests move with the code.
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
		// The package clause is Go's alone: sourceWithoutTest skips every path
		// without a .go suffix, so on a TypeScript- or Python-only change
		// nothing ever asked whether a test in the package moved. Confirmations
		// are this report's "we checked and it held" channel, so a change with
		// no Go file in it gets a message naming only the checks that ran.
		// Otherwise an absence would read as a pass.
		msg := "no test was skipped or focused, and no assertions were removed"
		if p.changedGo() {
			msg = "every changed package changed a test, " + msg
		}
		res.Confirmations = append(res.Confirmations, findings.Confirmation{
			Substrate: Substrate,
			Rule:      "tests-move-with-code",
			Message:   msg,
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

// changedGo reports whether the change touches a Go file at all, which is what
// decides whether the source-without-test question was asked of it.
func (p *Pane) changedGo() bool {
	for _, f := range p.changed {
		if strings.HasSuffix(f, ".go") {
			return true
		}
	}
	return false
}

// skipsAdded reports a skip or focus directive on a line this change adds to a
// test file. A skip quietly removes a test from the run; a focus (.only, fit)
// quietly removes every other test in the file. Each line is classified in its
// own file's language, because several of these directives are ordinary words
// in another one.
func (p *Pane) skipsAdded(baseRev string, res *pane.Result, lines *[]string) {
	for _, f := range p.scoped {
		lang := testLang(f)
		if lang == "" {
			continue
		}
		added := map[int]bool{}
		for _, n := range cover.AddedLines(p.Repo.DiffPath(baseRev, f)) {
			added[n] = true
		}
		if len(added) == 0 {
			continue
		}
		content, err := p.Repo.File("", f)
		if err != nil || content == "" {
			continue
		}
		for n, line := range strings.Split(content, "\n") {
			lineNo := n + 1
			if !added[lineNo] {
				continue
			}
			if what := skipKind(line, lang); what != "" {
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
// assertion-like lines than it started. It is reported per file rather than per
// line, because a deleted line has no place in the head tree to anchor on.
func (p *Pane) assertionsRemoved(baseRev string, res *pane.Result, lines *[]string) {
	var counts []testAssertions
	for _, f := range p.scoped {
		if !isTestFile(f) {
			continue
		}
		diff := p.Repo.DiffPath(baseRev, f)
		if diff == "" {
			continue
		}
		counts = append(counts, countAssertions(f, diff))
	}
	for _, c := range assertionLosses(counts) {
		res.Findings = append(res.Findings, findings.Finding{
			File:      c.path,
			Rule:      "assertions-removed",
			Substrate: Substrate,
			Category:  findings.CategoryTests,
			Severity:  findings.SeverityInfo,
			Message:   fmt.Sprintf("this change removes %d more assertion-like line(s) than it adds in this test", c.removed-c.added),
			Context:   "Assertions are what let a test fail. A net drop can mean a weaker test; check it is intended.",
		})
		*lines = append(*lines, fmt.Sprintf("- %s (-%d assertion-like line(s))", c.path, c.removed-c.added))
	}
}

// testAssertions is one test file's assertion-like lines as its diff shows them.
type testAssertions struct {
	path           string
	added, removed int
	// created and deleted mark a diff that adds or removes the whole file.
	created, deleted bool
}

func countAssertions(path, diff string) testAssertions {
	c := testAssertions{path: path}
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case l == "--- /dev/null":
			c.created = true
		case l == "+++ /dev/null":
			c.deleted = true
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			if assertionRe.MatchString(l[1:]) {
				c.added++
			}
		case strings.HasPrefix(l, "-"):
			if assertionRe.MatchString(l[1:]) {
				c.removed++
			}
		}
	}
	return c
}

// assertionLosses picks the files that lost assertions. A file that only changed
// nets its own additions against its removals. A deleted file is judged together
// with every test file the change creates, because the changed-path list has no
// rename detection: a moved or split test arrives as one deleted path and one or
// more new ones, and netting each path alone reported the move as a loss.
func assertionLosses(files []testAssertions) []testAssertions {
	createdAdds, deletedRemoves := 0, 0
	for _, f := range files {
		if f.created {
			createdAdds += f.added
		}
		if f.deleted {
			deletedRemoves += f.removed
		}
	}
	var out []testAssertions
	for _, f := range files {
		if f.removed <= f.added {
			continue
		}
		if f.deleted && deletedRemoves <= createdAdds {
			continue
		}
		out = append(out, f)
	}
	return out
}

// skipKind classifies a skip or focus directive on a line of a test file in
// lang, or returns "" for neither. The JS/TS patterns are tried only on JS/TS
// files, because Jasmine's bare fit and fdescribe are indistinguishable from
// the English word "fit": matching them everywhere turned a Go comment about
// trimming context "to fit the ceiling" into a finding claiming the change
// focuses tests, and a Go string literal saying "no context can fit" into
// another. A directive also has to look like the call or declaration it is:
// the call forms need their opening parenthesis, and an obvious line comment
// is cut off first, so a directive merely named in a comment or a string does
// not count. This is deliberately lexical; the pane does not parse Go.
func skipKind(line, lang string) string {
	code := line
	marker := "//"
	if lang == langPy {
		marker = "#"
	}
	if i := strings.Index(code, marker); i >= 0 {
		code = code[:i]
	}
	switch lang {
	case langGo:
		if goSkipRe.MatchString(code) {
			return "skips a test"
		}
	case langJS:
		if jsFocusRe.MatchString(code) {
			return "focuses tests, so the others in the file will not run"
		}
		if jsSkipRe.MatchString(code) {
			return "skips a test"
		}
	case langPy:
		if pySkipRe.MatchString(code) {
			return "skips a test"
		}
	}
	return ""
}

// The languages skipKind can classify a line in.
const (
	langGo = "go"
	langJS = "js"
	langPy = "py"
)

var (
	goSkipRe = regexp.MustCompile(`\b[tbsf]\.Skip(Now|f)?\s*\(`) // t.Skip / b.SkipNow

	// The bare forms refuse a preceding dot, so a method that happens to be
	// named fit, like chart.fit() in a test, is not read as Jasmine's fit.
	jsSkipRe = regexp.MustCompile(`\b(it|test|describe|context)\.skip\s*\(` + // .skip
		`|(^|[^.\w$])x(it|describe)\s*\(`) // xit / xdescribe

	jsFocusRe = regexp.MustCompile(`\b(it|test|describe|context)\.only\s*\(` + // .only
		`|(^|[^.\w$])f(it|describe)\s*\(`) // fit / fdescribe

	// Python's skip decorators are declarations rather than calls, so the @ that
	// introduces them stands in for the parenthesis the call forms require.
	pySkipRe = regexp.MustCompile(`@(pytest\.mark\.skip|unittest\.skip)` + // decorators
		`|\bpytest\.skip\s*\(|\bself\.skipTest\s*\(`)

	assertionRe = regexp.MustCompile(`\b(assert|require)\.\w+` + // Go testify
		`|\bt\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow)\b` + // Go testing
		`|\bexpect\s*\(|\.should\b` + // JS/TS
		`|\bassert(Equal|True|False|Nil|NotNil|Error|NoError)?\s*\(` + // xUnit-style
		`|\bself\.assert\w+\s*\(`) // Python unittest
)

// jsExts are the JavaScript and TypeScript extensions. A ".test." or "__tests__/"
// path only counts as a JS/TS test file if it actually is one, so that a file
// from another language never gets read for JS directives.
var jsExts = []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"}

// testLang reports the language of a test file, or "" when the path is not a
// test file in a supported language.
func testLang(f string) string {
	base := path.Base(f)
	switch {
	case strings.HasSuffix(f, "_test.go"):
		return langGo
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"):
		return langPy
	case !hasExt(base, jsExts):
		return ""
	case strings.Contains(base, ".test.") || strings.Contains(base, ".spec."):
		return langJS
	case strings.Contains(f, "__tests__/"):
		return langJS
	}
	return ""
}

// isTestFile reports whether a path is a test file in a supported language.
func isTestFile(f string) bool { return testLang(f) != "" }

func hasExt(f string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(f, e) {
			return true
		}
	}
	return false
}
