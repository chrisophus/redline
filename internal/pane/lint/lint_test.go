package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/pane"
)

// repo is a throwaway git repository built one commit at a time.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-b", "main")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "test")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *repo) write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-m", msg)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

func (r *repo) open() *gitx.Repo {
	r.t.Helper()
	g, err := gitx.Open(r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	return g
}

// runPane drives a pane the way internal/run does.
func runPane(t *testing.T, p pane.Pane, baseSHA string) pane.Result {
	t.Helper()
	before, err := p.Observe(pane.Revision{Name: "base", Rev: baseSHA})
	if err != nil {
		t.Fatalf("observe base: %v", err)
	}
	after, err := p.Observe(pane.Worktree)
	if err != nil {
		t.Fatalf("observe worktree: %v", err)
	}
	res, err := p.Diff(before, after)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	return res
}

func rules(fs []findings.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return out
}

// --- fingerprint and multiset ---

func TestDiffIssuesIgnoresMovedCode(t *testing.T) {
	base := []Issue{{Tool: "golangci-lint", File: "a.go", Line: 3, Rule: "errcheck", Message: "unchecked error"}}
	head := []Issue{
		{Tool: "golangci-lint", File: "a.go", Line: 40, Rule: "errcheck", Message: "unchecked error"},
		{Tool: "golangci-lint", File: "a.go", Line: 41, Rule: "govet", Message: "shadowed"},
	}
	introduced, resolved := diffIssues(base, head)
	if len(introduced) != 1 || introduced[0].Rule != "govet" {
		t.Fatalf("moved finding must keep its identity; introduced: %+v", introduced)
	}
	if resolved != 0 {
		t.Fatalf("nothing was resolved, got %d", resolved)
	}
}

func TestDiffIssuesNormalizesDigits(t *testing.T) {
	base := []Issue{{File: "a.go", Rule: "lll", Message: "line is 130 characters"}}
	head := []Issue{{File: "a.go", Rule: "lll", Message: "line is 131 characters"}}
	introduced, resolved := diffIssues(base, head)
	if len(introduced) != 0 || resolved != 0 {
		t.Fatalf("a count inside the message must not change identity: %+v / %d", introduced, resolved)
	}
}

func TestDiffIssuesMatchesByCount(t *testing.T) {
	one := Issue{File: "a.go", Rule: "errcheck", Message: "unchecked error"}
	introduced, resolved := diffIssues([]Issue{one}, []Issue{one, one})
	if len(introduced) != 1 {
		t.Fatalf("a second identical violation is introduced: %+v", introduced)
	}
	introduced, resolved = diffIssues([]Issue{one, one}, []Issue{one})
	if len(introduced) != 0 || resolved != 1 {
		t.Fatalf("dropping one of two identical violations resolves one: %+v / %d", introduced, resolved)
	}
}

// --- delta, with a fake tool on PATH ---

// fakeGolangci puts a stand-in golangci-lint on PATH. It prints the
// lint-fixture.json in its working directory, so the base worktree (the
// committed fixture) and the head tree (the edited fixture) answer
// differently, exactly like a real linter run at two revisions. A fixture
// containing FAIL makes the run fail.
func fakeGolangci(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ ! -f lint-fixture.json ]; then echo '{\"Issues\":[]}'; exit 0; fi\n" +
		"if grep -q FAIL lint-fixture.json; then echo 'tool exploded' >&2; exit 3; fi\n" +
		"cat lint-fixture.json\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "golangci-lint"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
}

func golangciJSON(issues ...string) string {
	return `{"Issues":[` + strings.Join(issues, ",") + `]}`
}

func issueJSON(file string, line int, linter, text string) string {
	return `{"FromLinter":"` + linter + `","Text":"` + text + `","Pos":{"Filename":"` + file + `","Line":` + itoa(line) + `}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestDeltaReportsIntroducedAndResolved(t *testing.T) {
	fakeGolangci(t)
	r := newRepo(t)
	r.write(".golangci.yml", "linters: {}\n")
	r.write("a.go", "package a\n")
	r.write("lint-fixture.json", golangciJSON(
		issueJSON("a.go", 1, "errcheck", "unchecked error"),
		issueJSON("a.go", 2, "lll", "line too long"),
	))
	base := r.commit("base")
	// Head: the errcheck finding moved, lll is fixed, govet is new.
	r.write("a.go", "package a\n\nfunc A() {}\n")
	r.write("lint-fixture.json", golangciJSON(
		issueJSON("a.go", 3, "errcheck", "unchecked error"),
		issueJSON("a.go", 3, "govet", "shadowed variable"),
	))

	p := &Delta{Repo: r.open()}
	if got := p.Scope([]string{"a.go", "lint-fixture.json"}); len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("scope should be the go file, got %v", got)
	}
	res := runPane(t, p, base)

	if len(res.Findings) != 1 {
		t.Fatalf("expected one introduced finding, got %v", rules(res.Findings))
	}
	f := res.Findings[0]
	if f.Rule != "golangci-lint/govet" || f.File != "a.go" || f.Line != 3 {
		t.Fatalf("introduced finding misdescribed: %+v", f)
	}
	if f.Severity != findings.SeverityWarning || f.Category != findings.CategoryLint {
		t.Fatalf("severity/category: %+v", f)
	}
	var sawResolved bool
	for _, c := range res.Confirmations {
		if c.Rule == "lint-resolved" && strings.Contains(c.Message, "1 linter finding(s)") {
			sawResolved = true
		}
	}
	if !sawResolved {
		t.Fatalf("the fixed lll finding must be confirmed as resolved: %+v", res.Confirmations)
	}
}

func TestDeltaCleanRunIsConfirmed(t *testing.T) {
	fakeGolangci(t)
	r := newRepo(t)
	r.write(".golangci.yml", "linters: {}\n")
	r.write("a.go", "package a\n")
	base := r.commit("base")
	r.write("a.go", "package a\n\nfunc A() {}\n")

	res := runPane(t, &Delta{Repo: r.open()}, base)
	if len(res.Findings) != 0 {
		t.Fatalf("expected no findings, got %v", rules(res.Findings))
	}
	var confirmed bool
	for _, c := range res.Confirmations {
		if c.Rule == "lint-clean-delta" {
			confirmed = true
		}
	}
	if !confirmed {
		t.Fatalf("a clean delta is a confirmation, not silence: %+v", res.Confirmations)
	}
}

// A base revision that cannot be linted degrades the delta rather than
// erasing it: introduced findings are limited to lines this change added, and
// the degradation is stated.
func TestDeltaDegradesWhenBaseFails(t *testing.T) {
	fakeGolangci(t)
	r := newRepo(t)
	r.write(".golangci.yml", "linters: {}\n")
	r.write("a.go", "package a\nvar one = 1\nvar two = 2\n")
	r.write("lint-fixture.json", "FAIL\n")
	base := r.commit("base")
	// Head adds line 4; head lint reports line 4 (added) and line 1 (not).
	r.write("a.go", "package a\nvar one = 1\nvar two = 2\nvar three = 3\n")
	r.write("lint-fixture.json", golangciJSON(
		issueJSON("a.go", 4, "govet", "on the added line"),
		issueJSON("a.go", 1, "govet", "on an old line"),
	))

	res := runPane(t, &Delta{Repo: r.open()}, base)
	if len(res.Findings) != 1 || res.Findings[0].Line != 4 {
		t.Fatalf("degraded mode keeps only added-line findings: %+v", res.Findings)
	}
	var stated bool
	for _, u := range res.Unknowns {
		if strings.Contains(u.Message, "could not run at the base revision") {
			stated = true
		}
	}
	if !stated {
		t.Fatalf("the degradation must be stated: %+v", res.Unknowns)
	}
}

// A tool that fails at head darks the pane: no delta can be computed, and a
// missing check must never read as a clean one.
func TestDeltaHeadFailureDarksThePane(t *testing.T) {
	fakeGolangci(t)
	r := newRepo(t)
	r.write(".golangci.yml", "linters: {}\n")
	r.write("a.go", "package a\n")
	r.commit("base")
	r.write("lint-fixture.json", "FAIL\n")

	p := &Delta{Repo: r.open()}
	if _, err := p.Observe(pane.Worktree); err == nil {
		t.Fatal("a head lint failure must be an error, not an empty result")
	}
}

// A repository configured for a linter that is not installed is a dark
// sensor, not a clean pane.
func TestDeltaMissingBinaryDarksThePane(t *testing.T) {
	r := newRepo(t)
	r.write(".golangci.yml", "linters: {}\n")
	r.write("a.go", "package a\n")
	r.commit("base")

	// A PATH with git only: the configured tool is absent.
	bin := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	p := &Delta{Repo: r.open()}
	_, obsErr := p.Observe(pane.Worktree)
	if obsErr == nil || !strings.Contains(obsErr.Error(), "not on PATH") {
		t.Fatalf("expected a missing-binary error, got: %v", obsErr)
	}
}

func TestDeltaSkipsWithoutAnyLintConfig(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "package a\n")
	r.commit("base")
	p := &Delta{Repo: r.open()}
	if got := p.Scope([]string{"a.go"}); got != nil {
		t.Fatalf("no config means empty scope, got %v", got)
	}
}

func TestDeltaNotesAConfigDrivenDelta(t *testing.T) {
	fakeGolangci(t)
	r := newRepo(t)
	r.write(".golangci.yml", "linters: {}\n")
	r.write("a.go", "package a\n")
	base := r.commit("base")
	r.write(".golangci.yml", "linters:\n  disable:\n    - errcheck\n")

	p := &Delta{Repo: r.open()}
	if got := p.Scope([]string{".golangci.yml"}); len(got) != 1 {
		t.Fatalf("the config itself is in scope, got %v", got)
	}
	res := runPane(t, p, base)
	if !strings.Contains(res.Render.Summary, "edits a lint config") {
		t.Fatalf("a config-driven delta must say so: %q", res.Render.Summary)
	}
}

// --- suppressions ---

func TestSuppressionsFlagsDirectivesOnAddedLines(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "package a\n\nfunc A() {}\n")
	r.write("b.ts", "export const b = 1\n")
	base := r.commit("base")
	r.write("a.go", "package a\n\nfunc A() {}\n\nfunc B() error { return nil } //nolint:errcheck,gosec\n")
	r.write("b.ts", "// eslint-disable-next-line no-console -- debugging\nconsole.log(1)\nexport const b = 1\n")

	p := &Suppressions{Repo: r.open()}
	p.Scope([]string{"a.go", "b.ts"})
	res := runPane(t, p, base)

	if len(res.Findings) != 2 {
		t.Fatalf("expected two suppression findings, got %+v", res.Findings)
	}
	byFile := map[string]findings.Finding{}
	for _, f := range res.Findings {
		byFile[f.File] = f
	}
	goF := byFile["a.go"]
	if goF.Line != 5 || !strings.Contains(goF.Message, "errcheck,gosec") {
		t.Fatalf("nolint finding must anchor the line and name the rules: %+v", goF)
	}
	if goF.Severity != findings.SeverityInfo {
		t.Fatalf("suppressions are visibility, not a gate: %+v", goF)
	}
	tsF := byFile["b.ts"]
	if !strings.Contains(tsF.Message, "no-console") || strings.Contains(tsF.Message, "debugging") {
		t.Fatalf("eslint finding names the rule, not the justification: %+v", tsF)
	}
}

func TestSuppressionsIgnoreDirectivesAlreadyAtBase(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "package a\n\nvar x = f() //nolint:errcheck\n")
	base := r.commit("base")
	r.write("a.go", "package a\n\nvar x = f() //nolint:errcheck\n\nvar y = 2\n")

	p := &Suppressions{Repo: r.open()}
	p.Scope([]string{"a.go"})
	res := runPane(t, p, base)
	if len(res.Findings) != 0 {
		t.Fatalf("a directive the base already had is not this change's: %+v", res.Findings)
	}
	if len(res.Confirmations) != 1 {
		t.Fatalf("no directive added is a confirmation: %+v", res.Confirmations)
	}
}

// --- config drift ---

func TestConfigNamesGolangciDisables(t *testing.T) {
	r := newRepo(t)
	r.write(".golangci.yml", "linters:\n  disable:\n    - lll\n")
	base := r.commit("base")
	r.write(".golangci.yml", "linters:\n  disable:\n    - lll\n    - errcheck\nissues:\n  exclude:\n    - \"unused parameter\"\n")

	p := &Config{Repo: r.open()}
	p.Scope([]string{".golangci.yml"})
	res := runPane(t, p, base)

	var msgs []string
	for _, f := range res.Findings {
		msgs = append(msgs, f.Message)
	}
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "disables the errcheck linter") {
		t.Fatalf("the newly disabled linter must be named:\n%s", joined)
	}
	if strings.Contains(joined, "disables the lll linter") {
		t.Fatalf("a linter already disabled at base is not this change's:\n%s", joined)
	}
	if !strings.Contains(joined, `excludes findings matching "unused parameter"`) {
		t.Fatalf("a new exclude pattern must be named:\n%s", joined)
	}
}

func TestConfigNamesESLintDowngrades(t *testing.T) {
	r := newRepo(t)
	r.write(".eslintrc.json", `{"rules":{"no-console":"error","eqeqeq":["error","always"]}}`)
	base := r.commit("base")
	r.write(".eslintrc.json", `{"rules":{"no-console":"warn","eqeqeq":["error","always"],"no-var":"off"}}`)

	p := &Config{Repo: r.open()}
	p.Scope([]string{".eslintrc.json"})
	res := runPane(t, p, base)

	var msgs []string
	for _, f := range res.Findings {
		msgs = append(msgs, f.Message)
	}
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "downgrades the no-console rule from error to warn") {
		t.Fatalf("downgrade must be named:\n%s", joined)
	}
	if !strings.Contains(joined, "turns the no-var rule off") {
		t.Fatalf("a rule introduced as off must be named:\n%s", joined)
	}
	if strings.Contains(joined, "eqeqeq") {
		t.Fatalf("an unchanged rule is not drift:\n%s", joined)
	}
}

func TestConfigUnparseableFormatStillReports(t *testing.T) {
	r := newRepo(t)
	r.write("eslint.config.js", "module.exports = [];\n")
	base := r.commit("base")
	r.write("eslint.config.js", "module.exports = [{rules: {'no-console': 'off'}}];\n")

	p := &Config{Repo: r.open()}
	p.Scope([]string{"eslint.config.js"})
	res := runPane(t, p, base)

	if len(res.Findings) != 1 || res.Findings[0].Rule != "lint-config-changed" {
		t.Fatalf("an unparseable config change must still be a finding: %+v", res.Findings)
	}
	if len(res.Findings[0].Evidence) == 0 {
		t.Fatal("the diff must ride along as evidence")
	}
}

func TestConfigAddedAndRemoved(t *testing.T) {
	r := newRepo(t)
	r.write("keep.txt", "x\n")
	base := r.commit("base")
	r.write(".eslintrc.json", `{"rules":{"no-console":"off"}}`)

	p := &Config{Repo: r.open()}
	p.Scope([]string{".eslintrc.json"})
	res := runPane(t, p, base)
	if len(res.Findings) != 1 || res.Findings[0].Rule != "lint-config-added" {
		t.Fatalf("an adopted config is reported without enumerating its disables: %+v", res.Findings)
	}
}

func TestConfigChangeWithNoRuleOffIsConfirmed(t *testing.T) {
	r := newRepo(t)
	r.write(".golangci.yml", "linters:\n  disable:\n    - lll\n")
	base := r.commit("base")
	r.write(".golangci.yml", "linters:\n  disable:\n    - lll\n  enable:\n    - gosec\n")

	p := &Config{Repo: r.open()}
	p.Scope([]string{".golangci.yml"})
	res := runPane(t, p, base)
	if len(res.Findings) != 0 {
		t.Fatalf("enabling a linter is not drift: %+v", res.Findings)
	}
	if len(res.Confirmations) != 1 {
		t.Fatalf("a config change that turns nothing off is confirmed: %+v", res.Confirmations)
	}
}
