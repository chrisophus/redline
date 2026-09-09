package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/provider"
)

type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
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
	full := filepath.Join(r.dir, filepath.FromSlash(path))
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

// changed is one commit that edits the Go file the graph knows, with a
// migration sitting beside it that only the graph connects to the change.
func changed(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	r.write("internal/store/user.go",
		"package store\n\ntype Store struct{}\n\nfunc (s *Store) Insert(email string) error {\n\treturn nil\n}\n")
	r.write("db/migrations/0007_add_email.sql",
		"ALTER TABLE users\n\tADD COLUMN email TEXT NOT NULL;\n")
	base := r.commit("base")
	r.write("internal/store/user.go",
		"package store\n\ntype Store struct{}\n\nfunc (s *Store) Insert(email string) error {\n\treturn save(email)\n}\n")
	head := r.commit("change the insert")
	r.write("graphify-out/graph.json", `{
	  "built_at_commit": "`+head+`",
	  "nodes": [
	    {"id": "insert", "label": "Insert()", "file_type": "code", "_origin": "ast",
	     "source_file": "internal/store/user.go", "source_location": "L5"},
	    {"id": "users", "label": "users", "file_type": "code", "_origin": "ast",
	     "source_file": "db/migrations/0007_add_email.sql", "source_location": "L1"}
	  ],
	  "links": [
	    {"source": "insert", "target": "users", "relation": "references",
	     "confidence": "INFERRED", "_origin": "ast"}
	  ]
	}`)
	r.t.Setenv("REDLINE_TEST_BASE", base)
	return r
}

// The whole contract is that Redline can read what this writes, so the test
// reads it back the way Redline does rather than by unmarshalling it here.
func TestWritesAnEnvelopeRedlineCanParse(t *testing.T) {
	r := changed(t)
	var out bytes.Buffer
	if err := run([]string{"--changed", os.Getenv("REDLINE_TEST_BASE"), "--dir", r.dir}, &out); err != nil {
		t.Fatal(err)
	}
	env, err := provider.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("Redline could not read the envelope: %v\n%s", err, out.String())
	}
	if env.Provider.Name != "graphify" {
		t.Errorf("provider = %q", env.Provider.Name)
	}
	// The graph is the provider's own input. A repository that commits it
	// rather than ignoring it must not get it back as a changed file the
	// graph knows nothing about.
	if len(env.Files) != 1 || env.Files[0].Path != "internal/store/user.go" {
		t.Errorf("manifest = %+v, want the one changed file", env.Files)
	}
	var neighbor bool
	for _, x := range env.Expansions {
		if x.Role == "neighbor" && x.File == "db/migrations/0007_add_email.sql" {
			neighbor = true
		}
	}
	if !neighbor {
		t.Errorf("the migration beside the change was not sent:\n%s", out.String())
	}
}

func TestGraphIsFoundFromTheRepositoryRoot(t *testing.T) {
	r := changed(t)
	// Redline runs a provider in the tree under review, which for a pull
	// request is a detached worktree rather than wherever `graphify update`
	// last ran. A relative --graph resolves against the repository root.
	var out bytes.Buffer
	if err := run([]string{"--changed", os.Getenv("REDLINE_TEST_BASE"), "--dir", filepath.Join(r.dir, "internal", "store")}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\"graphify\"") {
		t.Errorf("no envelope from a subdirectory:\n%s", out.String())
	}
}

func TestMissingGraphIsAnError(t *testing.T) {
	r := changed(t)
	if err := os.Remove(filepath.Join(r.dir, "graphify-out", "graph.json")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"--changed", os.Getenv("REDLINE_TEST_BASE"), "--dir", r.dir}, &out)
	if err == nil {
		t.Fatal("a missing graph produced an envelope; an unexamined change must not read as an examined one")
	}
	if out.Len() > 0 {
		t.Errorf("wrote to stdout while failing:\n%s", out.String())
	}
}

func TestChangedIsRequired(t *testing.T) {
	var out bytes.Buffer
	if err := run(nil, &out); err == nil {
		t.Error("ran without a base revision")
	}
}

func TestDeferCallersSplitting(t *testing.T) {
	got := splitGlobs(" **/*.go , **/*.ts ,, ")
	want := []string{"**/*.go", "**/*.ts"}
	if len(got) != len(want) {
		t.Fatalf("splitGlobs = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitGlobs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
