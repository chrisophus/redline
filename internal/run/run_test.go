package run_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
	"github.com/ccason/redline/internal/report"
	"github.com/ccason/redline/internal/run"
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

func (r *repo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-m", msg)
}

func (r *repo) run(opts run.Options) *run.Result {
	r.t.Helper()
	opts.Dir = r.dir
	res, err := run.Run(opts)
	if err != nil {
		r.t.Fatalf("run: %v", err)
	}
	return res
}

func rules(rep findings.Report) []string {
	var out []string
	for _, f := range rep.Findings {
		out = append(out, f.Rule)
	}
	return out
}

func hasRule(rep findings.Report, rule string) bool {
	for _, f := range rep.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// baseline builds a repo with one merged migration on main and a branch off it.
func baseline(t *testing.T) *repo {
	r := newRepo(t)
	r.write("migrations/000001_init.up.sql", "CREATE TABLE users (id int);\n")
	r.write("migrations/000001_init.down.sql", "DROP TABLE users;\n")
	r.commit("init")
	r.git("branch", "upstream")
	r.git("checkout", "-b", "feature")
	return r
}

func TestCleanNewMigrationHasNoFindings(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email text;\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if len(rep.Findings) != 0 {
		t.Fatalf("expected no findings, got %v", rules(rep))
	}
	if len(rep.Confirmations) == 0 {
		t.Fatal("expected confirmations: a clean check is the deliverable, not silence")
	}
	if len(rep.Unknowns) != 0 {
		t.Fatalf("expected nothing undetermined, got %+v", rep.Unknowns)
	}
	if rep.Substrates[0].State != findings.SubstrateRan {
		t.Fatalf("pane should have run, got %q", rep.Substrates[0].State)
	}
}

// Check 1: a migration that exists at merge-base was edited.
func TestModifiedMergedMigration(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000001_init.up.sql", "CREATE TABLE users (id bigint);\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if !hasRule(rep, "migration-modified-after-merge") {
		t.Fatalf("expected modification finding, got %v", rules(rep))
	}
	f := rep.Findings[0]
	if f.Severity != findings.SeverityError || f.Category != findings.CategorySchema {
		t.Fatalf("unexpected severity/category: %+v", f)
	}
	if f.Anchor == nil || f.Anchor.ID != "000001" {
		t.Fatalf("expected migration anchor 000001, got %+v", f.Anchor)
	}
	if f.Source != findings.SourceDeterministic {
		t.Fatalf("expected deterministic source, got %q", f.Source)
	}
	if !f.New || f.Fingerprint == "" {
		t.Fatalf("diff-based findings are always new and fingerprinted: %+v", f)
	}
}

// Uncommitted edits count: Redline is pre-push.
func TestModificationDetectedInWorkingTree(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000001_init.up.sql", "CREATE TABLE users (id bigint);\n")
	// deliberately not committed
	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if !hasRule(rep, "migration-modified-after-merge") {
		t.Fatalf("expected uncommitted edit to be seen, got %v", rules(rep))
	}
}

func TestDeletedMergedMigration(t *testing.T) {
	r := baseline(t)
	if err := os.Remove(filepath.Join(r.dir, "migrations/000001_init.up.sql")); err != nil {
		t.Fatal(err)
	}
	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if !hasRule(rep, "migration-deleted-after-merge") {
		t.Fatalf("expected deletion finding, got %v", rules(rep))
	}
}

// Check 2: a new migration reuses a version already published upstream.
func TestVersionCollisionWithUpstream(t *testing.T) {
	r := baseline(t)
	// Someone else's 000002 lands upstream.
	r.git("checkout", "upstream")
	r.write("migrations/000002_add_name.up.sql", "ALTER TABLE users ADD COLUMN name text;\n")
	r.commit("upstream 000002")
	r.git("checkout", "feature")
	// We pick the same number.
	r.write("migrations/000002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email text;\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if !hasRule(rep, "migration-version-collision") {
		t.Fatalf("expected collision finding, got %v", rules(rep))
	}
	f := rep.Findings[0]
	if !strings.Contains(f.Observed, "000002_add_name.up.sql") {
		t.Fatalf("finding should name the colliding upstream file: %q", f.Observed)
	}
}

// The same migration also being upstream is not a collision — it is the same
// file, and check 1 owns whether its contents drifted.
func TestSameFileUpstreamIsNotACollision(t *testing.T) {
	r := baseline(t)
	r.git("checkout", "upstream")
	r.write("migrations/000002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email text;\n")
	r.commit("upstream 000002")
	r.git("checkout", "feature")
	r.write("migrations/000002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email text;\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if hasRule(rep, "migration-version-collision") {
		t.Fatalf("same path upstream must not read as a collision: %v", rules(rep))
	}
}

// A missing upstream ref must render as undetermined, never as a pass.
func TestMissingUpstreamIsUndetermined(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email text;\n")

	rep := r.run(run.Options{Base: "main", Upstream: "origin/nope"}).Report
	if len(rep.Unknowns) == 0 {
		t.Fatal("unreadable upstream must be reported as undetermined")
	}
	for _, c := range rep.Confirmations {
		if c.Rule == "migration-version-unique" {
			t.Fatal("must not confirm uniqueness against an upstream it could not read")
		}
	}
}

func TestPaneSkippedWhenNoMigrationsTouched(t *testing.T) {
	r := baseline(t)
	r.write("main.go", "package main\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if rep.Substrates[0].State != findings.SubstrateSkipped {
		t.Fatalf("expected skipped, got %q", rep.Substrates[0].State)
	}
}

func TestMigrationsDirFilter(t *testing.T) {
	r := baseline(t)
	r.write("vendor/other/000001_init.up.sql", "SELECT 1;\n")
	r.commit("vendored migration")
	r.write("vendor/other/000001_init.up.sql", "SELECT 2;\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream", MigDir: "migrations"}).Report
	if len(rep.Findings) != 0 {
		t.Fatalf("--migrations should exclude other directories, got %v", rules(rep))
	}
}

func TestFingerprintStableAcrossRuns(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000001_init.up.sql", "CREATE TABLE users (id bigint);\n")

	first := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report.Findings[0].Fingerprint
	r.write("unrelated.go", "package main\n")
	second := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report.Findings[0].Fingerprint
	if first != second {
		t.Fatalf("fingerprint is the join key for comment state and must be stable: %q vs %q", first, second)
	}
}

// The failure mode that destroys trust: a change nothing looked at reading as
// a clean review.
func TestUnexaminedChangeIsNotACleanReview(t *testing.T) {
	r := baseline(t)
	r.write("main.go", "package main\n")

	res := r.run(run.Options{Base: "main", Upstream: "upstream"})
	rep := res.Report
	if rep.Coverage.ChangedFiles == 0 || rep.Coverage.ExaminedFiles != 0 {
		t.Fatalf("expected a changed-but-unexamined tree, got %+v", rep.Coverage)
	}
	if len(rep.Unknowns) == 0 {
		t.Fatal("an entirely unexamined change must be reported as unexamined")
	}
	md := report.Markdown(&rep, res.Renders, res.Evidence)
	if !strings.Contains(md, "examined none of this change") {
		t.Fatalf("report must say so at the top:\n%s", md)
	}
}

func TestCoverageCountsExaminedFiles(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email text;\n")
	r.write("README.md", "hi\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if rep.Coverage.ChangedFiles != 2 || rep.Coverage.ExaminedFiles != 1 {
		t.Fatalf("expected 1 of 2 examined, got %+v", rep.Coverage)
	}
	if len(rep.Coverage.Unexamined) != 1 || rep.Coverage.Unexamined[0] != "README.md" {
		t.Fatalf("unexamined files must be named, got %v", rep.Coverage.Unexamined)
	}
}

// A finding a reviewer cannot check for themselves is inference wearing
// evidence's clothes: the edit's SQL must be captured and shown.
func TestModificationCapturesTheSQLDiff(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000001_init.up.sql", "CREATE TABLE users (id bigint);\n")

	res := r.run(run.Options{Base: "main", Upstream: "upstream"})
	f := res.Report.Findings[0]
	var artifact string
	for _, id := range f.Evidence {
		if a, ok := res.Evidence[id]; ok && a.Kind == "diff" {
			artifact = a.Content
		}
	}
	if !strings.Contains(artifact, "-CREATE TABLE users (id int);") {
		t.Fatalf("expected the removed SQL in the captured diff, got %q", artifact)
	}
	md := report.Markdown(&res.Report, res.Renders, res.Evidence)
	if !strings.Contains(md, "+CREATE TABLE users (id bigint);") {
		t.Fatal("the report must show the evidence beside the claim")
	}
}

// "1 file unchanged" beside a modified sibling reads as reassurance.
func TestPartialImmutabilityIsNotConfirmed(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000001_init.up.sql", "CREATE TABLE users (id bigint);\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	for _, c := range rep.Confirmations {
		if c.Rule == "migration-immutable" {
			t.Fatalf("must not confirm immutability when a merged migration changed: %q", c.Message)
		}
	}
	var found bool
	for _, u := range rep.Unknowns {
		if strings.Contains(u.Message, "of 2 migration file(s) merged at the base were changed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the denominator to be stated, got %+v", rep.Unknowns)
	}
}

// Untracked files are in the change (pre-push) but `git diff REV -- path`
// emits nothing for them. The packet must still carry the new file so a
// reviewer can read it.
func TestUntrackedFileHasDiffInPacket(t *testing.T) {
	r := newRepo(t)
	r.write("keep.go", "package keep\n")
	r.commit("init")
	r.write("new_test.go", "package keep\n\nfunc TestX() {}\n")

	res := r.run(run.Options{})
	var found *packet.FileChange
	for i := range res.Packet.Files {
		if res.Packet.Files[i].Path == "new_test.go" {
			found = &res.Packet.Files[i]
			break
		}
	}
	if found == nil {
		t.Fatal("untracked file must appear in the packet")
	}
	if found.Status != "added" {
		t.Fatalf("untracked file status: got %q, want added", found.Status)
	}
	if !strings.Contains(found.Diff, "+package keep") {
		t.Fatalf("packet diff for untracked file was empty: %q", found.Diff)
	}
	if found.Added == 0 {
		t.Fatalf("untracked add must have a line count, got %+v", found)
	}
}

// A custom --out directory is Redline's own evidence, same as .redline/.
// Leaving it in scope makes the next run review its own previous report.
func TestCustomOutIsExcluded(t *testing.T) {
	r := newRepo(t)
	r.write("keep.go", "package keep\n")
	r.commit("init")
	r.write("artifacts/report.html", "<html>prior run</html>\n")
	r.write("real.go", "package real\n")

	res := r.run(run.Options{Out: "artifacts"})
	for _, path := range res.Report.Scope {
		if path == "artifacts/report.html" || strings.HasPrefix(path, "artifacts/") {
			t.Fatalf("custom --out leaked into scope: %v", res.Report.Scope)
		}
	}
	var sawReal bool
	for _, path := range res.Report.Scope {
		if path == "real.go" {
			sawReal = true
		}
	}
	if !sawReal {
		t.Fatalf("real change was dropped with the out dir: %v", res.Report.Scope)
	}
}
