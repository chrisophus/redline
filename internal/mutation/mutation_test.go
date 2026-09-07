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
