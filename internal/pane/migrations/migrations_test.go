package migrations

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
func runPane(t *testing.T, p *Pane, baseSHA string) pane.Result {
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

func find(t *testing.T, res pane.Result, rule string) findings.Finding {
	t.Helper()
	for _, f := range res.Findings {
		if f.Rule == rule {
			return f
		}
	}
	t.Fatalf("no %q finding; got %v", rule, rules(res.Findings))
	return findings.Finding{}
}

// --- check 1: modified-after-merge ---

// The README's third dogfooding lesson: a blob-hash-only message was not
// enough to answer "what did the edit change?" — the unified SQL diff must
// be attached to the finding as evidence.
func TestModifiedMigrationEmitsFindingWithSQLDiff(t *testing.T) {
	r := newRepo(t)
	r.write("migrations/0001_init.up.sql", "CREATE TABLE users (id INT);\n")
	baseSHA := r.commit("init")

	r.write("migrations/0001_init.up.sql", "CREATE TABLE users (id INT, email TEXT);\n")

	p := &Pane{Repo: r.open()}
	res := runPane(t, p, baseSHA)

	f := find(t, res, "migration-modified-after-merge")
	if f.Substrate != Substrate {
		t.Errorf("Substrate = %q, want %q", f.Substrate, Substrate)
	}
	if f.Severity != findings.SeverityError {
		t.Errorf("Severity = %q, want %q", f.Severity, findings.SeverityError)
	}
	if f.File != "migrations/0001_init.up.sql" {
		t.Errorf("File = %q", f.File)
	}

	var diffID string
	for _, id := range f.Evidence {
		if strings.HasPrefix(id, "diff:") {
			diffID = id
		}
	}
	if diffID == "" {
		t.Fatalf("finding evidence must reference a diff artifact, got %v", f.Evidence)
	}
	artifact, ok := res.Evidence[diffID]
	if !ok {
		t.Fatalf("no artifact recorded for %q", diffID)
	}
	if artifact.Kind != "diff" {
		t.Errorf("artifact Kind = %q, want %q", artifact.Kind, "diff")
	}
	if !strings.Contains(artifact.Content, "-CREATE TABLE users (id INT);") ||
		!strings.Contains(artifact.Content, "+CREATE TABLE users (id INT, email TEXT);") {
		t.Fatalf("artifact must carry the unified SQL diff, got:\n%s", artifact.Content)
	}
}

// A migration this change introduces, and one it never touches, must not be
// mistaken for a modification of a merged migration.
func TestAddedAndUntouchedMigrationsProduceNoModificationFinding(t *testing.T) {
	r := newRepo(t)
	r.write("migrations/0001_init.up.sql", "CREATE TABLE users (id INT);\n")
	baseSHA := r.commit("init")

	// 0001 is left byte-identical; 0002 is newly added by this branch.
	r.write("migrations/0002_add_role.up.sql", "ALTER TABLE users ADD COLUMN role TEXT;\n")

	p := &Pane{Repo: r.open()}
	res := runPane(t, p, baseSHA)

	for _, f := range res.Findings {
		if f.Rule == "migration-modified-after-merge" || f.Rule == "migration-deleted-after-merge" {
			t.Errorf("unexpected modification finding for an added/untouched migration: %+v", f)
		}
	}
}

// --- check 2: version collision with upstream ---

func TestNewMigrationCollidesWithUpstreamVersion(t *testing.T) {
	r := newRepo(t)
	r.write("migrations/0001_init.up.sql", "CREATE TABLE users (id INT);\n")
	baseSHA := r.commit("init")

	// Upstream already merged its own 0002 migration under a different name.
	r.git("checkout", "-b", "upstream")
	r.write("migrations/0002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email TEXT;\n")
	r.commit("upstream migration")
	r.git("checkout", "main")

	// This branch, unaware of upstream, picked the same version prefix.
	r.write("migrations/0002_add_role.up.sql", "ALTER TABLE users ADD COLUMN role TEXT;\n")

	p := &Pane{Repo: r.open(), UpstreamRef: "upstream"}
	res := runPane(t, p, baseSHA)

	f := find(t, res, "migration-version-collision")
	if !strings.Contains(f.Message, "0002") {
		t.Errorf("collision message must name the colliding version: %q", f.Message)
	}
	if !strings.Contains(f.Message, "migrations/0002_add_email.up.sql") {
		t.Errorf("collision message must name the colliding upstream path: %q", f.Message)
	}
	if f.Anchor == nil || f.Anchor.ID != "0002" {
		t.Errorf("Anchor must name version 0002, got %+v", f.Anchor)
	}
}

func TestNewMigrationFreshVersionIsConfirmed(t *testing.T) {
	r := newRepo(t)
	r.write("migrations/0001_init.up.sql", "CREATE TABLE users (id INT);\n")
	baseSHA := r.commit("init")

	r.git("checkout", "-b", "upstream")
	r.write("migrations/0002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email TEXT;\n")
	r.commit("upstream migration")
	r.git("checkout", "main")

	// 0003 is not claimed anywhere upstream.
	r.write("migrations/0003_add_role.up.sql", "ALTER TABLE users ADD COLUMN role TEXT;\n")

	p := &Pane{Repo: r.open(), UpstreamRef: "upstream"}
	res := runPane(t, p, baseSHA)

	var found bool
	for _, c := range res.Confirmations {
		if c.Rule == "migration-version-unique" && strings.Contains(c.Message, "0003") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a migration-version-unique confirmation naming 0003, got %+v", res.Confirmations)
	}
	for _, f := range res.Findings {
		if f.Rule == "migration-version-collision" {
			t.Errorf("a fresh version must not report a collision: %+v", f)
		}
	}
}

// --- confirmation honesty: partial denominator is an unknown, not a pass ---

// README: "A confirmation said '1 migration file is byte-identical' while its
// sibling was flagged as modified. Confirmations now require the whole set
// to hold; a partial result is an unknown with the denominator stated."
func TestPartialImmutabilityIsUnknownNotWholeSetConfirmation(t *testing.T) {
	r := newRepo(t)
	r.write("migrations/0001_init.up.sql", "CREATE TABLE users (id INT);\n")
	r.write("migrations/0002_add_role.up.sql", "ALTER TABLE users ADD COLUMN role TEXT;\n")
	baseSHA := r.commit("init")

	// Only one of the two pre-existing migrations is touched.
	r.write("migrations/0002_add_role.up.sql", "ALTER TABLE users ADD COLUMN role TEXT DEFAULT '';\n")

	p := &Pane{Repo: r.open()}
	res := runPane(t, p, baseSHA)

	for _, c := range res.Confirmations {
		if c.Rule == "migration-immutable" {
			t.Fatalf("a partial result must never render as a whole-set confirmation: %+v", c)
		}
	}
	var found bool
	for _, u := range res.Unknowns {
		if u.Substrate == Substrate && strings.Contains(u.Message, "1 of 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unknown stating the denominator '1 of 2', got %+v", res.Unknowns)
	}
}

// --- no migrations: not applicable, not a pass ---

func TestNoMigrationsDoesNotApply(t *testing.T) {
	r := newRepo(t)
	r.write("README.md", "hello\n")
	baseSHA := r.commit("init")
	r.write("README.md", "hello again\n")

	p := &Pane{Repo: r.open()}

	// Scope is how internal/run decides a pane did not apply to this change
	// (SubstrateSkipped) rather than that it ran and found nothing.
	if scope := p.Scope([]string{"README.md"}); len(scope) != 0 {
		t.Fatalf("Scope must report no applicable files for a non-migration change, got %v", scope)
	}

	res := runPane(t, p, baseSHA)
	if len(res.Findings) != 0 || len(res.Confirmations) != 0 || len(res.Unknowns) != 0 {
		t.Fatalf("a change with no migration files must report neither a pass nor a finding, got %+v", res)
	}
}
