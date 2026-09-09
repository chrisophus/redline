package run_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/report"
	"github.com/chrisophus/redline/internal/run"
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

// A repository with no migrations and no API spec is not told that its
// migrations and API went unexamined. The pane is recorded as not applicable,
// which no renderer mentions, rather than skipped, which they list.
func TestPaneNotApplicableWhenRepositoryHasNoSuchFiles(t *testing.T) {
	r := newRepo(t)
	r.write("main.go", "package main\n")
	r.commit("init")
	r.git("checkout", "-b", "feature")
	r.write("main.go", "package main\n\nfunc main() {}\n")

	rep := r.run(run.Options{Base: "main"}).Report
	states := map[string]findings.SubstrateState{}
	for _, s := range rep.Substrates {
		states[s.Name] = s.State
	}
	for _, name := range []string{"redline/sql", "redline/api"} {
		if states[name] != findings.SubstrateNotApplicable {
			t.Errorf("%s: expected not-applicable in a repository with no such files, got %q", name, states[name])
		}
	}
	if rep.Coverage.CoverableFiles != 1 {
		t.Errorf("one Go file changed, coverableFiles = %d", rep.Coverage.CoverableFiles)
	}
	md := report.Markdown(&rep, nil, nil, nil)
	if strings.Contains(md, "redline/sql") || strings.Contains(md, "redline/api") {
		t.Errorf("the markdown report must not mention panes the repository has no files for:\n%s", md)
	}
}

// The same repository, with the change touching only a docs file: nothing is
// coverable, so coverage is left out rather than reported as unmeasured.
func TestCoverageNotMentionedWhenNothingIsCoverable(t *testing.T) {
	r := newRepo(t)
	r.write("README.md", "# hi\n")
	r.commit("init")
	r.git("checkout", "-b", "feature")
	r.write("README.md", "# hi\n\nmore\n")

	res := r.run(run.Options{Base: "main"})
	if res.Report.Coverage.CoverableFiles != 0 || res.Report.Coverage.Diff != nil {
		t.Fatalf("nothing coverable: %+v", res.Report.Coverage)
	}
	for _, u := range res.Report.Unknowns {
		if u.Substrate == "redline/tests" {
			t.Errorf("no coverage unknown should be raised for a change with no coverable file: %+v", u)
		}
	}
	md := report.Markdown(&res.Report, res.Renders, res.Evidence, res.Change)
	if strings.Contains(md, "coverage") {
		t.Errorf("markdown must not mention coverage:\n%s", md)
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
// a clean review. Markdown prose is a file no pane covers.
func TestUnexaminedChangeIsNotACleanReview(t *testing.T) {
	r := baseline(t)
	r.write("docs/notes.md", "notes\n")

	res := r.run(run.Options{Base: "main", Upstream: "upstream"})
	rep := res.Report
	if rep.Coverage.ChangedFiles == 0 || rep.Coverage.ExaminedFiles != 0 {
		t.Fatalf("expected a changed-but-unexamined tree, got %+v", rep.Coverage)
	}
	if len(rep.Unknowns) == 0 {
		t.Fatal("an entirely unexamined change must be reported as unexamined")
	}
	md := report.Markdown(&rep, res.Renders, res.Evidence, res.Change)
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

func TestDiffCoverageReadsAProfileFromTheTree(t *testing.T) {
	r := baseline(t)
	r.write("internal/x/x.go", "package x\n\nfunc A() int {\n\treturn 1\n}\n\nfunc B() int {\n\treturn 2\n}\n")
	// A() ran, B() did not.
	r.write("coverage.out", "mode: set\n"+
		"github.com/chrisophus/redline/internal/x/x.go:3.14,5.2 1 1\n"+
		"github.com/chrisophus/redline/internal/x/x.go:7.14,9.2 1 0\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	c := rep.Coverage.Diff
	if c == nil {
		t.Fatal("expected a diff coverage result")
	}
	if c.Profile != "coverage.out" {
		t.Errorf("Profile = %q", c.Profile)
	}
	if c.Lines == 0 {
		t.Fatal("expected coverable added lines")
	}
	if c.Covered == 0 || c.Covered == c.Lines {
		t.Errorf("expected a partial number, got %d of %d", c.Covered, c.Lines)
	}
}

// A committed target is reviewed in a pristine worktree that holds no coverage
// profile. When the reviewed revision is the checkout's own HEAD, the profile
// in the checkout describes exactly that code, so it stands in.
func TestDiffCoverageFallsBackToOriginCheckoutForCommitHead(t *testing.T) {
	r := newRepo(t)
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	r.write("internal/x/x.go", "package x\n\nfunc A() int {\n\treturn 1\n}\n")
	r.commit("one")
	r.write("internal/x/x.go", "package x\n\nfunc A() int {\n\treturn 1\n}\n\nfunc B() int {\n\treturn 2\n}\n")
	r.commit("two")
	// The profile lives in the developer's checkout, never in the worktree.
	r.write("coverage.out", "mode: set\n"+
		"github.com/chrisophus/redline/internal/x/x.go:7.14,9.2 1 1\n")

	res := r.run(run.Options{Commit: "HEAD"})
	if res.Target.Kind != "commit" {
		t.Fatalf("kind %q", res.Target.Kind)
	}
	c := res.Report.Coverage.Diff
	if c == nil {
		t.Fatal("expected the origin checkout profile to cover --commit HEAD")
	}
	if c.Profile != "coverage.out" || c.Lines == 0 {
		t.Fatalf("coverage not computed from the fallback: %+v", c)
	}
}

// The fallback is scoped to the checkout's HEAD. A different revision (here the
// middle commit) must not borrow a profile that does not describe it.
func TestDiffCoverageNoFallbackForARevisionThatIsNotHead(t *testing.T) {
	r := newRepo(t)
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	r.write("internal/x/x.go", "package x\n\nfunc A() int {\n\treturn 1\n}\n")
	r.commit("one")
	r.write("internal/x/x.go", "package x\n\nfunc A() int {\n\treturn 1\n}\n\nfunc B() int {\n\treturn 2\n}\n")
	r.commit("two")
	r.write("more.go", "package x\n")
	r.commit("three")
	r.write("coverage.out", "mode: set\n"+
		"github.com/chrisophus/redline/internal/x/x.go:7.14,9.2 1 1\n")

	rep := r.run(run.Options{Commit: "HEAD~1"}).Report
	if rep.Coverage.Diff != nil {
		t.Fatalf("older commit must not borrow the checkout profile: %+v", rep.Coverage.Diff)
	}
}

// Without a harness coverage profile, a missing profile is left off the report.
func TestMissingCoverageIgnoredWithoutHarness(t *testing.T) {
	r := baseline(t)
	r.write("internal/x/x.go", "package x\n\nfunc A() int {\n\treturn 1\n}\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if rep.Coverage.Diff != nil {
		t.Fatalf("expected no coverage result, got %+v", rep.Coverage.Diff)
	}
	for _, u := range rep.Unknowns {
		if u.Substrate == "redline/tests" {
			t.Fatalf("must not report coverage without harness config: %+v", u)
		}
	}
}

func TestAllowMissingCoverageContinuesWithHarness(t *testing.T) {
	r := baseline(t)
	r.write(".redline.yml", `harness:
  profiles:
    - id: go
      path: coverage.out
      produce: {command: true}
      when: stale
      scope: ["**/*.go"]
`)
	r.write("internal/x/x.go", "package x\n\nfunc A() int {\n\treturn 1\n}\n")

	res, err := run.Run(run.Options{Dir: r.dir, Base: "main", Upstream: "upstream", AllowMissingCoverage: true})
	if err != nil {
		t.Fatalf("allow-missing-coverage should continue: %v", err)
	}
	var found bool
	for _, u := range res.Report.Unknowns {
		if u.Substrate == "redline/tests" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected coverage unknown on the report when allowed to continue")
	}
}

func TestMissingHarnessProfileBalks(t *testing.T) {
	r := baseline(t)
	r.write(".redline.yml", `harness:
  profiles:
    - id: go
      path: coverage.out
      produce: {command: true}
      when: stale
      scope: ["**/*.go"]
`)
	r.write("internal/x/x.go", "package x\n\nfunc A() int {\n\treturn 1\n}\n")

	_, err := run.Run(run.Options{Dir: r.dir, Base: "main", Upstream: "upstream"})
	if err == nil {
		t.Fatal("expected an error when a configured profile is missing")
	}
	if !strings.Contains(err.Error(), `harness profile "go"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A change with no Go files should not claim anything about test coverage.
func TestNoGoFilesMeansNoCoverageClaim(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email text;\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if rep.Coverage.Diff != nil {
		t.Errorf("no Go file changed, got %+v", rep.Coverage.Diff)
	}
	for _, u := range rep.Unknowns {
		if u.Substrate == "redline/tests" {
			t.Errorf("must not report a coverage gap for a SQL-only change: %+v", u)
		}
	}
}

// Generated output must leave the change before anything counts it, or the
// coverage ratio measures files no reviewer would read.
func TestGeneratedFilesLeaveTheChangeAndAreNamed(t *testing.T) {
	r := baseline(t)
	r.write("migrations/000002_add_email.up.sql", "ALTER TABLE users ADD COLUMN email text;\n")
	r.write("internal/api/oas_schemas_gen.go", "// Code generated by ogen. DO NOT EDIT.\npackage api\n")
	r.write("go.sum", "example.com/x v1.0.0 h1:abc=\n")
	r.write("internal/api/handlers.go", "package api\n\nfunc Handle() {}\n")

	res := r.run(run.Options{Base: "main", Upstream: "upstream"})
	rep := res.Report

	// Two files a reviewer reads: the migration and the handler.
	if rep.Coverage.ChangedFiles != 2 {
		t.Fatalf("ChangedFiles = %d, want 2 after dropping generated output; scope %v",
			rep.Coverage.ChangedFiles, rep.Scope)
	}
	if len(rep.Coverage.Generated) != 2 {
		t.Fatalf("Generated = %v, want the ogen file and go.sum", rep.Coverage.Generated)
	}
	for _, path := range rep.Coverage.Generated {
		if path == "internal/api/handlers.go" {
			t.Fatal("a hand-written file was excluded as generated")
		}
	}
	// The change is what the report renders; generated text must not reach it
	// either.
	for _, f := range res.Change.Files {
		if strings.HasPrefix(f.Path, "internal/api/oas_") || f.Path == "go.sum" {
			t.Fatalf("%s reached the change", f.Path)
		}
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
	md := report.Markdown(&res.Report, res.Renders, res.Evidence, res.Change)
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
// emits nothing for them. The change must still carry the new file so a
// reviewer can read it.
func TestUntrackedFileHasDiff(t *testing.T) {
	r := newRepo(t)
	r.write("keep.go", "package keep\n")
	r.commit("init")
	r.write("new_test.go", "package keep\n\nfunc TestX() {}\n")

	res := r.run(run.Options{})
	var found *change.File
	for i := range res.Change.Files {
		if res.Change.Files[i].Path == "new_test.go" {
			found = &res.Change.Files[i]
			break
		}
	}
	if found == nil {
		t.Fatal("untracked file must appear in the change")
	}
	if found.Status != "added" {
		t.Fatalf("untracked file status: got %q, want added", found.Status)
	}
	if !strings.Contains(found.Diff, "+package keep") {
		t.Fatalf("diff for untracked file was empty: %q", found.Diff)
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

func TestCommitTargetIsOnlyThatCommit(t *testing.T) {
	r := newRepo(t)
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	r.write("one.txt", "1\n")
	r.commit("one")
	r.write("two.txt", "2\n")
	r.commit("two")
	r.write("dirty.txt", "uncommitted\n")

	res := r.run(run.Options{Commit: "HEAD"})
	if res.Target.Kind != "commit" {
		t.Fatalf("kind %q", res.Target.Kind)
	}
	if !hasPath(res.Report.Scope, "two.txt") {
		t.Fatalf("latest commit file missing: %v", res.Report.Scope)
	}
	if hasPath(res.Report.Scope, "one.txt") {
		t.Fatalf("earlier commit leaked into --commit HEAD: %v", res.Report.Scope)
	}
	if hasPath(res.Report.Scope, "dirty.txt") {
		t.Fatalf("working-tree dirt leaked into --commit: %v", res.Report.Scope)
	}
}

func TestRangeTargetCoversTheSpan(t *testing.T) {
	r := newRepo(t)
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	r.write("a.txt", "a\n")
	r.commit("a")
	r.write("b.txt", "b\n")
	r.commit("b")
	r.write("c.txt", "c\n")
	r.commit("c")

	res := r.run(run.Options{Range: "HEAD~2..HEAD"})
	if res.Target.Kind != "range" {
		t.Fatalf("kind %q", res.Target.Kind)
	}
	if !hasPath(res.Report.Scope, "b.txt") || !hasPath(res.Report.Scope, "c.txt") {
		t.Fatalf("range should include b and c: %v", res.Report.Scope)
	}
	if hasPath(res.Report.Scope, "a.txt") {
		t.Fatalf("range start tree leaked into the change: %v", res.Report.Scope)
	}
}

func hasPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// The suppression pane rides in the standard pane list: a directive the
// change adds must reach the report without any flag.
func TestAddedSuppressionReachesTheReport(t *testing.T) {
	r := baseline(t)
	r.write("internal/x/x.go", "package x\n\nvar v = f() //nolint:errcheck\n")

	rep := r.run(run.Options{Base: "main", Upstream: "upstream"}).Report
	if !hasRule(rep, "suppression-added") {
		t.Fatalf("expected a suppression finding, got %v", rules(rep))
	}
	var f findings.Finding
	for _, cand := range rep.Findings {
		if cand.Rule == "suppression-added" {
			f = cand
		}
	}
	if f.File != "internal/x/x.go" || f.Line != 3 || !strings.Contains(f.Message, "errcheck") {
		t.Fatalf("suppression finding misdescribed: %+v", f)
	}
	if f.Severity != findings.SeverityInfo {
		t.Fatalf("suppressions are info severity: %+v", f)
	}
}

// The two-pass loop: a run stamps fingerprints, the agent writes verdicts keyed
// by them, the next run merges them onto the findings. review.json is the
// agent's own state, read from the evidence dir and never overwritten.
func TestVerdictMergedFromReviewFile(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "package a\n")
	r.commit("base")
	r.write("a.go", "package a\n\nfunc A() {} //nolint:errcheck\n")
	out := filepath.Join(r.dir, ".redline")

	first := r.run(run.Options{Base: "main", Out: out}).Report
	var fp string
	for _, f := range first.Findings {
		if f.Rule == "suppression-added" {
			fp = f.Fingerprint
		}
	}
	if fp == "" {
		t.Fatalf("expected a suppression-added finding to judge, got %+v", first.Findings)
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(findings.Review{Verdicts: map[string]findings.Verdict{
		fp: {Ruling: "justified", Rationale: "seed"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "review.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	second := r.run(run.Options{Base: "main", Out: out}).Report
	var v *findings.Verdict
	for _, f := range second.Findings {
		if f.Rule == "suppression-added" {
			v = f.Verdict
		}
	}
	if v == nil || v.Ruling != "justified" {
		t.Fatalf("the second run must merge the agent verdict onto the finding, got %+v", v)
	}
	if v.Source != findings.SourceLLM {
		t.Errorf("a merged verdict is the agent's reading: source = %q, want llm", v.Source)
	}
}

// providerRepo commits a declared context provider on main, then branches, so
// the config itself is not part of the change under review.
func providerRepo(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)
	r.write(".redline.yml", "context:\n  - name: gofake\n    command: redline-test-no-such-provider\n    scope:\n      - \"**/*.go\"\n")
	r.commit("declare a context provider")
	r.git("checkout", "-b", "feature")
	return r
}

// A change that is half Go and half something no provider covers must say so.
// The context block would otherwise cover the Go half and report nothing about
// the rest, which reads exactly like a change that was fully resolved — the
// failure the whole unknown vocabulary exists to prevent.
//
// The provider's command does not exist, which is deliberate: scope decides
// what a provider speaks for and is answered before anything runs, so the gap
// is reported without depending on a binary being installed.
func TestChangedFilesOutsideEveryProviderScopeAreReported(t *testing.T) {
	r := providerRepo(t)
	r.write("internal/a/a.go", "package a\n\nfunc A() {}\n")
	r.write("web/app.ts", "export const app = 1;\n")

	rep := r.run(run.Options{Base: "main"}).Report

	var gap *findings.Unknown
	for i, u := range rep.Unknowns {
		if strings.Contains(u.Message, "outside every configured context provider") {
			gap = &rep.Unknowns[i]
		}
	}
	if gap == nil {
		t.Fatalf("the .ts file no provider claims must be reported as uncovered, got %+v", rep.Unknowns)
	}
	if !strings.Contains(gap.Reason, "web/app.ts") {
		t.Errorf("the gap must name the file: reason = %q", gap.Reason)
	}
	if strings.Contains(gap.Reason, "internal/a/a.go") {
		t.Errorf("the Go file is inside the provider's scope and must not be named: reason = %q", gap.Reason)
	}
}

// The same repository with nothing outside the provider's scope must not
// manufacture a gap: an unknown that fires on a fully covered change is noise,
// and noise in this channel is what teaches a reader to skip it.
func TestNoGapWhenEveryChangedFileIsClaimed(t *testing.T) {
	r := providerRepo(t)
	r.write("internal/a/a.go", "package a\n\nfunc A() {}\n")

	rep := r.run(run.Options{Base: "main"}).Report

	for _, u := range rep.Unknowns {
		if strings.Contains(u.Message, "outside every configured context provider") {
			t.Fatalf("no file is outside the scope, yet a gap was reported: %+v", u)
		}
	}
}

// A review written against another change must not be merged. review.json
// lives beside the session and outlives it, so the review of the last target
// is exactly what is sitting there when the next run writes over the session.
// This happened: one pull request's review rendered onto another, and `post`
// would have put it on the wrong pull request.
func TestAReviewFromAnotherChangeIsNotMerged(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "package a\n")
	r.commit("base")
	r.write("a.go", "package a\n\nfunc A() {}\n")
	out := filepath.Join(r.dir, ".redline")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(findings.Review{
		Revision: "deadbeef:cafebabe",
		Overview: "a review of something else entirely",
		Comments: []findings.ReviewComment{{
			File: "a.go", Line: 3, Body: "this remark belongs to another change",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "review.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := r.run(run.Options{Base: "main", Out: out}).Report
	for _, f := range rep.Findings {
		if f.Source == findings.SourceLLM {
			t.Errorf("a review of another change reached the findings: %+v", f)
		}
	}
	if rep.Agent != nil {
		t.Errorf("a review of another change reached the report's prose: %+v", rep.Agent)
	}
	// Refusing quietly would be the same bug wearing a different hat: the
	// reader has to be told a review was found and not used.
	var said bool
	for _, u := range rep.Unknowns {
		if strings.Contains(u.Message, "was written against") {
			said = true
		}
	}
	if !said {
		t.Errorf("the refusal is not stated anywhere: %+v", rep.Unknowns)
	}
}

// A review with no stamp is merged. Writing review.json by hand is the
// documented path for an agent and predates the stamp, so an unstamped file is
// not evidence of staleness.
func TestAnUnstampedReviewIsStillMerged(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "package a\n")
	r.commit("base")
	r.write("a.go", "package a\n\nfunc A() {}\n")
	out := filepath.Join(r.dir, ".redline")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(findings.Review{
		Overview: "written by hand, no stamp",
		Comments: []findings.ReviewComment{{File: "a.go", Line: 3, Body: "hand-written remark"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "review.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := r.run(run.Options{Base: "main", Out: out}).Report
	if rep.Agent == nil || rep.Agent.Overview == "" {
		t.Error("an unstamped review was refused; writing one by hand is the documented path")
	}
}

// The stamp `redline review` writes has to match what the next run computes,
// or every real review reads as stale. Same session, same identity, twice.
func TestTheStampMatchesTheChangeItWasWrittenFor(t *testing.T) {
	r := newRepo(t)
	r.write("a.go", "package a\n")
	r.commit("base")
	r.write("a.go", "package a\n\nfunc A() {}\n")
	out := filepath.Join(r.dir, ".redline")

	res := r.run(run.Options{Base: "main", Out: out})
	id := change.ReviewIdentity(res.Report.BaseSHA, res.Change)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(findings.Review{
		Revision: id,
		Overview: "a review of this very change",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "review.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := r.run(run.Options{Base: "main", Out: out}).Report
	if rep.Agent == nil {
		t.Fatalf("a review stamped with this change was refused; unknowns: %+v", rep.Unknowns)
	}
}
