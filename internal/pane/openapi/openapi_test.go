package openapi

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

func (r *repo) write(path, body string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-m", msg)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

// diff observes the spec at the given base commit against the working tree,
// which is how a pre-push run sees it.
func (r *repo) diff(base string) pane.Result {
	r.t.Helper()
	g, err := gitx.Open(r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	p := &Pane{Repo: g}
	p.Scope([]string{"api/openapi.yaml"})

	before, err := p.Observe(pane.Revision{Name: "base", Rev: base})
	if err != nil {
		r.t.Fatal(err)
	}
	after, err := p.Observe(pane.Worktree)
	if err != nil {
		r.t.Fatal(err)
	}
	res, err := p.Diff(before, after)
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

func rules(res pane.Result) []string {
	var out []string
	for _, f := range res.Findings {
		out = append(out, f.Rule)
	}
	return out
}

func find(t *testing.T, res pane.Result, rule string) findings.Finding {
	t.Helper()
	for _, f := range res.Findings {
		if f.Rule == rule {
			return f
		}
	}
	t.Fatalf("no %q finding; got %v", rule, rules(res))
	return findings.Finding{}
}

const twoOps = `openapi: 3.0.0
info:
  title: pets
  version: 1.0.0
paths:
  /pets:
    get:
      responses:
        "200":
          description: ok
        "404":
          description: missing
    post:
      requestBody:
        content:
          application/json:
            schema:
              required: [name]
      responses:
        "201":
          description: created
`

func TestScopeOnlyClaimsSpecs(t *testing.T) {
	p := &Pane{}
	got := p.Scope([]string{
		"api/openapi.yaml",
		"api/swagger.json",
		"deploy/values.yaml", // also YAML, also has a paths key in real life
		"internal/api/handlers.go",
		"README.md",
	})
	want := []string{"api/openapi.yaml", "api/swagger.json"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAddedOperationIsNotBreaking(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", twoOps)
	base := r.commit("spec")
	r.write("api/openapi.yaml", strings.Replace(twoOps, "  /pets:", "  /health:\n    get:\n      responses:\n        \"200\":\n          description: ok\n  /pets:", 1))

	res := r.diff(base)
	for _, f := range res.Findings {
		if f.Severity == findings.SeverityError {
			t.Errorf("adding an operation must not be an error: %s", f.Message)
		}
	}
	if len(res.Confirmations) == 0 {
		t.Fatal("a spec that moved without breaking anything is a confirmation, not silence")
	}
	joined := strings.Join(res.Render.Lines, "\n")
	if !strings.Contains(joined, "added: GET /health") {
		t.Errorf("the added operation must be named: %q", joined)
	}
}

func TestRemovedOperationIsBreaking(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", twoOps)
	base := r.commit("spec")
	// Drop POST /pets.
	r.write("api/openapi.yaml", twoOps[:strings.Index(twoOps, "    post:")])

	res := r.diff(base)
	f := find(t, res, "api-operation-removed")
	if f.Severity != findings.SeverityError {
		t.Errorf("severity = %q, want error", f.Severity)
	}
	if !strings.Contains(f.Message, "POST /pets") {
		t.Errorf("message must name the operation: %q", f.Message)
	}
	if len(f.Evidence) == 0 {
		t.Error("a contract finding must carry the spec diff a reviewer can check")
	}
	for _, c := range res.Confirmations {
		if c.Rule == "api-no-breaking-change" {
			t.Error("must not confirm a clean contract while reporting a removal")
		}
	}
}

func TestRemovedResponseCodeIsBreaking(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", twoOps)
	base := r.commit("spec")
	r.write("api/openapi.yaml", strings.Replace(twoOps, "        \"404\":\n          description: missing\n", "", 1))

	res := r.diff(base)
	f := find(t, res, "api-operation-breaking")
	if f.Severity != findings.SeverityError {
		t.Errorf("severity = %q, want error", f.Severity)
	}
	if !strings.Contains(f.Message, "no longer returns 404") {
		t.Errorf("message must say what stopped being returned: %q", f.Message)
	}
}

// The change most easily missed in a diff: one word of indented YAML that makes
// every existing caller invalid.
func TestNewlyRequiredFieldIsBreaking(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", twoOps)
	base := r.commit("spec")
	r.write("api/openapi.yaml", strings.Replace(twoOps, "required: [name]", "required: [name, species]", 1))

	res := r.diff(base)
	f := find(t, res, "api-operation-breaking")
	if !strings.Contains(f.Message, "now requires") || !strings.Contains(f.Message, "species") {
		t.Errorf("message must name the new requirement: %q", f.Message)
	}
	if f.Severity != findings.SeverityError {
		t.Errorf("severity = %q, want error", f.Severity)
	}
}

func TestNewlyRequiredParameterIsBreaking(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", twoOps)
	base := r.commit("spec")
	r.write("api/openapi.yaml", strings.Replace(twoOps,
		"    get:\n      responses:",
		"    get:\n      parameters:\n        - name: tenant\n          in: query\n          required: true\n      responses:", 1))

	res := r.diff(base)
	f := find(t, res, "api-operation-breaking")
	if !strings.Contains(f.Message, "query tenant") {
		t.Errorf("message must name the parameter and where it goes: %q", f.Message)
	}
}

func TestRelaxingARequirementIsNotAnError(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", twoOps)
	base := r.commit("spec")
	r.write("api/openapi.yaml", strings.Replace(twoOps, "required: [name]", "required: []", 1))

	res := r.diff(base)
	f := find(t, res, "api-operation-relaxed")
	if f.Severity != findings.SeverityInfo {
		t.Errorf("severity = %q, want info: dropping a requirement breaks no caller", f.Severity)
	}
	if !strings.Contains(f.Message, "no longer requires") {
		t.Errorf("message: %q", f.Message)
	}
}

func TestDeletedSpecIsBreaking(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", twoOps)
	base := r.commit("spec")
	if err := os.Remove(filepath.Join(r.dir, "api/openapi.yaml")); err != nil {
		t.Fatal(err)
	}

	res := r.diff(base)
	f := find(t, res, "api-spec-removed")
	if f.Severity != findings.SeverityError {
		t.Errorf("severity = %q, want error", f.Severity)
	}
}

func TestAddedSpecHasNothingToBreak(t *testing.T) {
	r := newRepo(t)
	r.write("README.md", "hi\n")
	base := r.commit("init")
	r.write("api/openapi.yaml", twoOps)

	res := r.diff(base)
	if len(res.Findings) != 0 {
		t.Fatalf("a brand new spec breaks nothing, got %v", rules(res))
	}
	if len(res.Confirmations) == 0 {
		t.Fatal("expected a confirmation naming the new operations")
	}
}

// A spec mid-edit is a normal state for a pre-push tool to meet. It must be
// reported as undetermined, never as a contract that did not change.
func TestUnparseableSpecIsUndetermined(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", twoOps)
	base := r.commit("spec")
	r.write("api/openapi.yaml", "paths:\n  /pets:\n   get:\n  bad: [unclosed\n")

	res := r.diff(base)
	if len(res.Unknowns) == 0 {
		t.Fatal("an unparseable spec must be undetermined")
	}
	if len(res.Findings) != 0 {
		t.Fatalf("nothing can be claimed about a spec that would not parse, got %v", rules(res))
	}
	for _, c := range res.Confirmations {
		if c.Rule == "api-no-breaking-change" {
			t.Error("must not confirm a contract it could not read")
		}
	}
}

func TestJSONSpecParses(t *testing.T) {
	r := newRepo(t)
	r.write("api/openapi.yaml", `{"openapi":"3.0.0","paths":{"/pets":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	base := r.commit("spec")
	r.write("api/openapi.yaml", `{"openapi":"3.0.0","paths":{}}`)

	res := r.diff(base)
	if f := find(t, res, "api-operation-removed"); !strings.Contains(f.Message, "GET /pets") {
		t.Errorf("JSON specs must parse too: %q", f.Message)
	}
}
