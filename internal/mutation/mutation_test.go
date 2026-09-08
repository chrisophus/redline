package mutation_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisophus/redline/internal/mutation"
)

const sample = `{
  "go_module": "example.com/x",
  "files": [
    {"file_name": "internal/foo/foo.go", "mutations": [
      {"type":"CONDITIONALS_BOUNDARY","status":"KILLED","line":10,"replacement":">="},
      {"type":"EXPRESSION_REMOVE","status":"LIVED","line":12,"original":"!ok","replacement":"true"},
      {"type":"RETURN_ZERO","status":"LIVED","line":40,"replacement":"0"},
      {"type":"INTEGER_INCREMENT","status":"NOT COVERED","line":50}
    ]}
  ]
}`

func writeReport(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Mutation is scoped to the lines the change adds. A survivor off the diff (line
// 40) is another change's problem; a not-covered mutant (line 50) is coverage's
// story, not this one.
func TestComputeScopesToAddedLines(t *testing.T) {
	dir := writeReport(t, "mutants.json", sample)
	res := mutation.Compute(dir, []mutation.Changed{{Path: "internal/foo/foo.go", Added: []int{10, 12}}})
	if res == nil {
		t.Fatal("a report with mutants on changed lines must produce a result")
	}
	if res.Killed != 1 || res.Lived != 1 {
		t.Fatalf("killed=%d lived=%d, want 1 and 1", res.Killed, res.Lived)
	}
	if len(res.Survived) != 1 || len(res.Survived[0].Mutants) != 1 {
		t.Fatalf("survivors = %+v", res.Survived)
	}
	m := res.Survived[0].Mutants[0]
	if m.Line != 12 || m.Mutator != "EXPRESSION_REMOVE" || m.Original != "!ok" || m.Replacement != "true" {
		t.Fatalf("survivor = %+v", m)
	}
}

// gomutants pointed at a package writes a shorter file_name than the
// repository-relative path redline holds; a suffix match must still find it.
func TestComputeMatchesBySuffix(t *testing.T) {
	dir := writeReport(t, "mutants.json", `{"files":[{"file_name":"foo.go","mutations":[
	  {"type":"X","status":"LIVED","line":3,"original":"a","replacement":"b"}]}]}`)
	res := mutation.Compute(dir, []mutation.Changed{{Path: "internal/foo/foo.go", Added: []int{3}}})
	if res == nil || res.Lived != 1 {
		t.Fatalf("suffix match failed: %+v", res)
	}
}

func TestComputeNilWhenNoReport(t *testing.T) {
	if res := mutation.Compute(t.TempDir(), []mutation.Changed{{Path: "a.go", Added: []int{1}}}); res != nil {
		t.Fatalf("no report must read as nil, got %+v", res)
	}
}

// A report that says nothing about any line the change touched is "nobody
// measured this change", which must read as nil, not as a clean result.
func TestComputeNilWhenNothingOnChangedLines(t *testing.T) {
	dir := writeReport(t, "mutants.json", sample)
	if res := mutation.Compute(dir, []mutation.Changed{{Path: "internal/foo/foo.go", Added: []int{99}}}); res != nil {
		t.Fatalf("no measured line must read as nil, got %+v", res)
	}
}

func TestComputeFindsMutationReportJSON(t *testing.T) {
	dir := writeReport(t, "mutation-report.json", sample)
	res := mutation.Compute(dir, []mutation.Changed{{Path: "internal/foo/foo.go", Added: []int{12}}})
	if res == nil || res.Lived != 1 {
		t.Fatalf("mutation-report.json: %+v", res)
	}
}

// gomutants v0.6.0 reports INFRA_ERROR when the test binary died for a reason
// of its own. Counting it as killed is how a runner that ran out of memory
// reads as a suite that caught the break.
const v060 = `{
  "go_module": "example.com/x",
  "files": [
    {"file_name": "internal/foo/foo.go", "mutations": [
      {"id":"internal/foo/foo.go:Double:RETURN_ZERO#1","type":"RETURN_ZERO","status":"LIVED","line":10,"replacement":"0"},
      {"id":"internal/foo/foo.go:Double:CONDITIONALS_BOUNDARY#1","type":"CONDITIONALS_BOUNDARY","status":"INFRA_ERROR","line":11},
      {"id":"internal/foo/foo.go:Double:EXPRESSION_REMOVE#1","type":"EXPRESSION_REMOVE","status":"EQUIVALENT","line":12},
      {"id":"internal/foo/foo.go:Double:RETURN_ERROR_NIL#1","type":"RETURN_ERROR_NIL","status":"KILLED","line":13}
    ]}
  ]
}`

func TestInfraErrorIsCountedApartFromKilled(t *testing.T) {
	dir := writeReport(t, "mutants.json", v060)
	res := mutation.Compute(dir, []mutation.Changed{{Path: "internal/foo/foo.go", Added: []int{10, 11, 12, 13}}})
	if res == nil {
		t.Fatal("a v0.6.0 report on changed lines must produce a result")
	}
	if res.Killed != 1 || res.Lived != 1 || res.Infra != 1 || res.Equivalent != 1 {
		t.Fatalf("killed=%d lived=%d infra=%d equivalent=%d, want 1 each",
			res.Killed, res.Lived, res.Infra, res.Equivalent)
	}
	if len(res.Unreliable) != 1 || len(res.Unreliable[0].Mutants) != 1 ||
		res.Unreliable[0].Mutants[0].Line != 11 {
		t.Fatalf("the lines that were not measured have to be namable: %+v", res.Unreliable)
	}
	if len(res.Survived) != 1 || len(res.Survived[0].Mutants) != 1 ||
		res.Survived[0].Mutants[0].Line != 10 {
		t.Fatalf("an equivalent mutant is not a survivor: %+v", res.Survived)
	}
}

// A run where every mutant failed on the runner is not a run with nothing to
// say. It is the one that most needs saying.
func TestInfraOnlyReportIsNotNil(t *testing.T) {
	body := `{"files":[{"file_name":"a.go","mutations":[
	  {"id":"a.go:F:RETURN_ZERO#1","type":"RETURN_ZERO","status":"INFRA_ERROR","line":3}]}]}`
	dir := writeReport(t, "mutants.json", body)
	res := mutation.Compute(dir, []mutation.Changed{{Path: "a.go", Added: []int{3}}})
	if res == nil || res.Infra != 1 {
		t.Fatalf("a report of nothing but failed runs must still be reported: %+v", res)
	}
}

// The id is what survives a rebase that moves the line, so it is the verdict
// key when the report has one.
func TestSurvivorKeyPrefersTheMutantID(t *testing.T) {
	dir := writeReport(t, "mutants.json", v060)
	res := mutation.Compute(dir, []mutation.Changed{{Path: "internal/foo/foo.go", Added: []int{10}}})
	m := res.Survived[0].Mutants[0]
	if m.ID != "internal/foo/foo.go:Double:RETURN_ZERO#1" || m.Key != m.ID {
		t.Fatalf("survivor = %+v; the key must be the mutant id when there is one", m)
	}
	if m.Repro() != "gomutants --run-mutant-id 'internal/foo/foo.go:Double:RETURN_ZERO#1'" {
		t.Fatalf("repro = %q", m.Repro())
	}
}

// A pre-0.6.0 report has no ids, and the path-and-line key still has to work.
func TestSurvivorKeyFallsBackToPathAndLine(t *testing.T) {
	dir := writeReport(t, "mutants.json", sample)
	res := mutation.Compute(dir, []mutation.Changed{{Path: "internal/foo/foo.go", Added: []int{12}}})
	m := res.Survived[0].Mutants[0]
	if m.Key != "internal/foo/foo.go:12:EXPRESSION_REMOVE" || m.Repro() != "" {
		t.Fatalf("survivor = %+v; repro = %q", m, m.Repro())
	}
}
