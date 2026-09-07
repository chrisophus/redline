package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
)

func TestJSONPathGet(t *testing.T) {
	root := map[string]any{
		"results": []any{
			map[string]any{
				"file":  "a.go",
				"range": map[string]any{"start": map[string]any{"line": float64(12)}},
				"tags":  []any{"x", "y"},
			},
		},
	}
	if v, ok := jsonPathGet(root, "results[0].range.start.line"); !ok || asInt(v, ok) != 12 {
		t.Errorf("nested line = %v (ok=%v), want 12", v, ok)
	}
	if v, ok := jsonPathGet(root, "results[0].tags[1]"); !ok || asString(v, ok) != "y" {
		t.Errorf("array index = %v (ok=%v), want y", v, ok)
	}
	if _, ok := jsonPathGet(root, "results[0].missing"); ok {
		t.Error("a missing key must report ok=false")
	}
	if _, ok := jsonPathGet(root, "results[9].file"); ok {
		t.Error("an out-of-range index must report ok=false")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/*.ts", "web/src/app.ts", true},
		{"**/*.ts", "app.ts", true},
		{"*.yaml", "openapi.yaml", true},
		{"*.yaml", "api/openapi.yaml", false}, // * does not cross a separator
		{"api/**", "api/v1/spec.yaml", true},
		{"openapi.yaml", "openapi.yaml", true},
	}
	for _, tc := range cases {
		if got := matchesAnyGlob([]string{tc.pattern}, tc.path); got != tc.want {
			t.Errorf("match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestParseGenericJSON(t *testing.T) {
	cfg := ToolConfig{
		Name:        "oasdiff",
		ResultsPath: "",
		Fields: FieldPaths{
			File: "source", Line: "line", Rule: "id", Message: "text", Severity: "level",
		},
		SeverityMap: map[string]string{"ERR": "error", "WARN": "warning", "INFO": "info"},
	}
	out := `[{"id":"response-removed","text":"deleted 200","level":"ERR","source":"openapi.yaml","line":42}]`
	issues, err := parseGenericJSON(out, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("want one issue, got %+v", issues)
	}
	got := issues[0]
	if got.File != "openapi.yaml" || got.Line != 42 || got.Rule != "response-removed" ||
		got.Message != "deleted 200" || got.Severity != "error" || got.Tool != "oasdiff" {
		t.Errorf("misparsed: %+v", got)
	}
}

func TestParseSARIF(t *testing.T) {
	out := `{"runs":[{"results":[
	  {"ruleId":"G404","level":"warning","message":{"text":"weak rng"},
	   "locations":[{"physicalLocation":{"artifactLocation":{"uri":"main.go"},"region":{"startLine":7}}}]}
	]}]}`
	issues, err := parseSARIF(out, ToolConfig{Name: "gosec"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("want one issue, got %+v", issues)
	}
	got := issues[0]
	if got.File != "main.go" || got.Line != 7 || got.Rule != "G404" || got.Severity != "warning" {
		t.Errorf("misparsed SARIF: %+v", got)
	}
}

func TestParseSpectralWithLineOffset(t *testing.T) {
	cfg := ToolConfig{Name: "vacuum", Format: "spectral", LineOffset: 1}
	out := `[{"code":"oas3-schema","message":"bad schema","severity":0,"source":"openapi.yaml","range":{"start":{"line":10}}}]`
	issues, err := parseToolOutput(out, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := issues[0]
	// Spectral lines are 0-indexed; the offset makes it 1-indexed line 11.
	if got.Line != 11 || got.Rule != "oas3-schema" || got.Severity != "error" || got.File != "openapi.yaml" {
		t.Errorf("misparsed Spectral: %+v", got)
	}
}

func TestLoadConfigRejectsMissingCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".redline.yml"),
		[]byte("tools:\n  - name: vacuum\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(dir); err == nil {
		t.Fatal("a tool with no command must be rejected, not silently ignored")
	}
}

func TestLoadConfigAbsentIsNotAnError(t *testing.T) {
	cfg, err := loadConfig(t.TempDir())
	if err != nil || cfg != nil {
		t.Fatalf("no config is (nil, nil), got (%v, %v)", cfg, err)
	}
}

// A configured linter runs at base and head and its new issues become
// findings, exactly like the built-in tools, driven only by .redline.yml.
func TestConfiguredLinterProducesDelta(t *testing.T) {
	fakeToolFromFixture(t, "mylint", "mylint-fixture.json")
	r := newRepo(t)
	r.write(".redline.yml", `tools:
  - name: mylint
    command: mylint
    scope: ["**/*.go"]
    format: json
    resultsPath: issues
    fields: {file: file, line: line, rule: rule, message: msg, severity: sev}
`)
	r.write("a.go", "package a\n")
	r.write("mylint-fixture.json", `{"issues":[{"file":"a.go","line":1,"rule":"old","msg":"x","sev":"warning"}]}`)
	base := r.commit("base")
	// Head resolves "old" and introduces "new".
	r.write("a.go", "package a\n\nfunc A() {}\n")
	r.write("mylint-fixture.json", `{"issues":[{"file":"a.go","line":3,"rule":"new","msg":"y","sev":"error"}]}`)

	p := &Delta{Repo: r.open()}
	if got := p.Scope([]string{"a.go", ".redline.yml", "mylint-fixture.json"}); len(got) < 1 {
		t.Fatalf("the go file and config are in scope, got %v", got)
	}
	res := runPane(t, p, base)
	if len(res.Findings) != 1 || res.Findings[0].Rule != "mylint/new" {
		t.Fatalf("the introduced finding must come through: %+v", res.Findings)
	}
	if res.Findings[0].Severity != findings.SeverityError {
		t.Errorf("configured severity must survive: %+v", res.Findings[0])
	}
}

// baselineIssues mode reads a committed artifact as the "already existed"
// set instead of running the tool at the base revision.
func TestConfiguredLinterBaselineFileMode(t *testing.T) {
	fakeToolFromFixture(t, "mylint", "mylint-fixture.json")
	r := newRepo(t)
	r.write(".redline.yml", `tools:
  - name: mylint
    command: mylint
    scope: ["**/*.go"]
    format: json
    resultsPath: issues
    fields: {file: file, line: line, rule: rule, message: msg, severity: sev}
    baseline: {mode: file, file: mylint-baseline.json}
`)
	r.write("a.go", "package a\n")
	// The baseline already accepts "known"; head adds "fresh".
	r.write("mylint-baseline.json", `{"issues":[{"file":"a.go","line":1,"rule":"known","msg":"x","sev":"warning"}]}`)
	base := r.commit("base")
	r.write("a.go", "package a\n\nfunc A() {}\n")
	r.write("mylint-fixture.json", `{"issues":[{"file":"a.go","line":1,"rule":"known","msg":"x","sev":"warning"},{"file":"a.go","line":3,"rule":"fresh","msg":"y","sev":"warning"}]}`)

	p := &Delta{Repo: r.open()}
	p.Scope([]string{"a.go"})
	res := runPane(t, p, base)
	if len(res.Findings) != 1 || res.Findings[0].Rule != "mylint/fresh" {
		t.Fatalf("only the issue absent from the baseline is introduced: %+v", res.Findings)
	}
}

// fakeToolFromFixture puts a stand-in binary on PATH that prints the named
// fixture file from its working directory (an empty result if absent), so the
// same fixture at two revisions drives a real base-vs-head delta.
func fakeToolFromFixture(t *testing.T, name, fixture string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ ! -f " + fixture + " ]; then echo '{\"issues\":[]}'; exit 0; fi\n" +
		"cat " + fixture + "\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
}

// A differ tool (oasdiff shape) runs once, comparing the file at base and at
// head directly; every change it reports is introduced. The fake stands in
// for oasdiff: it emits a breaking-change record only when the two files it
// is handed actually differ.
func TestConfiguredDifferTool(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"if diff -q \"$1\" \"$2\" >/dev/null 2>&1; then echo '[]'; else " +
		"echo '[{\"id\":\"response-removed\",\"text\":\"200 removed\",\"level\":\"ERR\",\"source\":\"openapi.yaml\",\"line\":5}]'; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "fakeoasdiff"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())

	r := newRepo(t)
	r.write(".redline.yml", `tools:
  - name: oasdiff
    kind: differ
    command: fakeoasdiff
    args: ["{{base}}", "{{head}}"]
    scope: ["openapi.yaml"]
    format: json
    fields: {file: source, line: line, rule: id, message: text, severity: level}
    severityMap: {ERR: error}
`)
	r.write("openapi.yaml", "openapi: 3.0.0\npaths:\n  /a: {get: {responses: {200: {}}}}\n")
	base := r.commit("base")
	// Head drops the 200 response — the files now differ.
	r.write("openapi.yaml", "openapi: 3.0.0\npaths:\n  /a: {get: {responses: {404: {}}}}\n")

	p := &Delta{Repo: r.open()}
	if got := p.Scope([]string{"openapi.yaml", ".redline.yml"}); len(got) < 1 {
		t.Fatalf("the spec is in scope, got %v", got)
	}
	res := runPane(t, p, base)
	if len(res.Findings) != 1 || res.Findings[0].Rule != "oasdiff/response-removed" {
		t.Fatalf("the differ's breaking change must be a finding: %+v", res.Findings)
	}
	if res.Findings[0].File != "openapi.yaml" || res.Findings[0].Line != 5 ||
		res.Findings[0].Severity != findings.SeverityError {
		t.Errorf("differ finding misdescribed: %+v", res.Findings[0])
	}
}

// A differ tool has no pair to compare for a file this change adds or
// deletes. Handing the tool an empty base or a missing head path would fail
// it and dark the whole pane; instead the file is skipped, the skip stated,
// and the clean confirmation withheld.
func TestDifferSkipsAddedAndDeletedFiles(t *testing.T) {
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	r := newRepo(t)
	r.write(".redline.yml", `tools:
  - name: oasdiff
    kind: differ
    command: fakeoasdiff-never-runs
    args: ["{{base}}", "{{head}}"]
    scope: ["*.yaml"]
    format: json
    fields: {file: source, line: line, rule: id, message: text, severity: level}
`)
	r.write("deleted.yaml", "openapi: 3.0.0\n")
	base := r.commit("base")
	r.write("added.yaml", "openapi: 3.0.0\n")
	if err := os.Remove(filepath.Join(r.dir, "deleted.yaml")); err != nil {
		t.Fatal(err)
	}

	p := &Delta{Repo: r.open()}
	if got := p.Scope([]string{"added.yaml", "deleted.yaml"}); len(got) != 2 {
		t.Fatalf("both specs are in scope, got %v", got)
	}
	res := runPane(t, p, base)
	if len(res.Findings) != 0 {
		t.Fatalf("nothing was compared, so nothing is introduced: %+v", res.Findings)
	}
	if len(res.Unknowns) != 2 {
		t.Fatalf("each skipped file must be stated, got %+v", res.Unknowns)
	}
	for _, c := range res.Confirmations {
		if c.Rule == "lint-clean-delta" {
			t.Fatal("a comparison that never ran must not confirm a clean delta")
		}
	}
}

// An unreadable baseline degrades the delta and says so; the lint-clean-delta
// confirmation would assert a comparison that never happened, so it is
// withheld even when no head issue lands on an added line.
func TestBaselineUnreadableForfeitsCleanConfirmation(t *testing.T) {
	fakeToolFromFixture(t, "mylint", "mylint-fixture.json")
	r := newRepo(t)
	r.write(".redline.yml", `tools:
  - name: mylint
    command: mylint
    scope: ["**/*.go"]
    format: json
    resultsPath: issues
    fields: {file: file, line: line, rule: rule, message: msg, severity: sev}
    baseline: {mode: file, file: missing-baseline.json}
`)
	r.write("a.go", "package a\n")
	base := r.commit("base")
	r.write("a.go", "package a\n\nfunc A() {}\n")

	p := &Delta{Repo: r.open()}
	p.Scope([]string{"a.go"})
	res := runPane(t, p, base)
	found := false
	for _, u := range res.Unknowns {
		if strings.Contains(u.Message, "baseline") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unreadable baseline must be stated, got %+v", res.Unknowns)
	}
	for _, c := range res.Confirmations {
		if c.Rule == "lint-clean-delta" {
			t.Fatal("an unreadable baseline must withhold the clean confirmation")
		}
	}
}
