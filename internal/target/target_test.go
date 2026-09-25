package target

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/gitx"
)

func TestResolveRejectsMultipleSelectors(t *testing.T) {
	_, err := Resolve(Options{Dir: t.TempDir(), PR: "1", Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("expected only-one error, got %v", err)
	}
	_, err = Resolve(Options{Dir: t.TempDir(), Commit: "HEAD", Range: "a..b"})
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("expected only-one error, got %v", err)
	}
}

func TestResolveWorktree(t *testing.T) {
	dir := initRepo(t)
	tgt, err := Resolve(Options{Dir: dir, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Kind != KindWorktree {
		t.Fatalf("kind %q", tgt.Kind)
	}
	if !sameDir(tgt.Dir, dir) {
		t.Fatalf("dir %q want %q", tgt.Dir, dir)
	}
	if tgt.Head != "" {
		t.Fatalf("worktree head should be empty, got %q", tgt.Head)
	}
}

// A clean checkout already at the branch tip is the tree to observe: it
// holds the coverage profile and the rest of the harness output, and a
// detached worktree holds none of it.
func TestResolveBranchUsesTheCheckoutWhenItIsAlreadyThere(t *testing.T) {
	dir := initRepo(t)
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	tgt, err := Resolve(Options{Dir: dir, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Kind != KindBranch {
		t.Fatalf("kind %q", tgt.Kind)
	}
	if tgt.Detached {
		t.Error("a clean checkout on the branch tip was copied into a worktree anyway")
	}
	if !sameDir(tgt.Dir, dir) {
		t.Errorf("dir %q want the checkout %q", tgt.Dir, dir)
	}
	if tgt.Head == "" {
		t.Error("branch head SHA missing")
	}
}

// Anything in the tree that is not committed rules the checkout out: the
// panes observe a directory, so an uncommitted edit would be reported as part
// of the branch under review.
func TestResolveBranchDetachesWhenTheTreeIsDirty(t *testing.T) {
	dir := initRepo(t)
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgt, err := Resolve(Options{Dir: dir, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !tgt.Detached {
		t.Fatal("a dirty checkout was observed directly, so uncommitted work would read as the branch")
	}
	if sameDir(tgt.Dir, dir) {
		t.Error("the target is the caller's own tree")
	}
	if _, err := os.Stat(filepath.Join(tgt.Dir, "README")); err != nil {
		t.Fatal(err)
	}
}

// An untracked file counts as dirty even though no commit contains it, for
// the same reason: it is picked up as part of the change.
func TestResolveBranchDetachesForAnUntrackedFile(t *testing.T) {
	dir := initRepo(t)
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgt, err := Resolve(Options{Dir: dir, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !tgt.Detached {
		t.Fatal("an untracked file did not disqualify the checkout, so it would be reported as part of the branch")
	}
}

// The checkout is only usable for the revision it is actually on.
func TestResolveCommitDetachesForAnotherRevision(t *testing.T) {
	dir := initRepo(t)
	writeCommit(t, dir, "next.txt", "two\n", "second")
	writeCommit(t, dir, "third.txt", "three\n", "third")
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	tgt, err := Resolve(Options{Dir: dir, Commit: "HEAD~1"})
	if err != nil {
		t.Fatal(err)
	}
	if !tgt.Detached {
		t.Fatal("HEAD~1 was observed in a checkout sitting on HEAD")
	}
}

func TestResolveCommitUsesParentAsBase(t *testing.T) {
	dir := initRepo(t)
	writeCommit(t, dir, "next.txt", "two\n", "second")
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	tgt, err := Resolve(Options{Dir: dir, Commit: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Kind != KindCommit {
		t.Fatalf("kind %q", tgt.Kind)
	}
	parent, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD^").Output()
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Base != strings.TrimSpace(string(parent)) {
		t.Fatalf("base %q want parent %q", tgt.Base, strings.TrimSpace(string(parent)))
	}
}

func TestResolveRangeSplitsADotDotB(t *testing.T) {
	dir := initRepo(t)
	writeCommit(t, dir, "next.txt", "two\n", "second")
	t.Setenv("REDLINE_WORKTREE_ROOT", t.TempDir())
	tgt, err := Resolve(Options{Dir: dir, Range: "HEAD~1..HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Kind != KindRange {
		t.Fatalf("kind %q", tgt.Kind)
	}
	if tgt.Label != "HEAD~1..HEAD" {
		t.Fatalf("label %q", tgt.Label)
	}
	start, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD~1").Output()
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Base != strings.TrimSpace(string(start)) && tgt.Base != "HEAD~1" {
		t.Fatalf("base %q", tgt.Base)
	}
}

func TestParseRange(t *testing.T) {
	left, right, err := parseRange("abc..def")
	if err != nil || left != "abc" || right != "def" {
		t.Fatalf("got %q %q %v", left, right, err)
	}
	left, right, err = parseRange("abc..")
	if err != nil || left != "abc" || right != "HEAD" {
		t.Fatalf("open end: %q %q %v", left, right, err)
	}
	if _, _, err := parseRange("abc"); err == nil {
		t.Fatal("bare ref must fail")
	}
	if _, _, err := parseRange("..def"); err == nil {
		t.Fatal("missing start must fail")
	}
}

func writeCommit(t *testing.T, dir, path, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("add", "-A")
	run("commit", "-m", msg)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

func TestPRHeadRefPrefersExistingOriginBranch(t *testing.T) {
	dir := initRepo(t)
	writeCommit(t, dir, "feature.txt", "feat\n", "feature")
	featureSHA, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	headRefOid := strings.TrimSpace(string(featureSHA))
	runGit(t, dir, "update-ref", "refs/remotes/origin/feat/pr-branch", headRefOid)

	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := prHeadRef(repo, &PullRequest{
		Number:      1,
		HeadRefName: "feat/pr-branch",
		HeadRefOid:  headRefOid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if head != headRefOid {
		t.Fatalf("head %q want %q", head, headRefOid)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// sameDir reports whether a and b name the same directory after symlink
// resolution. macOS temp dirs are often /var vs /private/var.
func sameDir(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// `redline review --pr 123` followed by `redline ingest --pr 456` used to
// merge a review of 456 into the session for 123 and report success.
func TestMatchesRejectsADifferentTarget(t *testing.T) {
	pr := &Target{Kind: KindPR, PR: &PullRequest{Number: 123}}
	if err := pr.Matches(Options{PR: "123"}); err != nil {
		t.Fatalf("same PR rejected: %v", err)
	}
	if err := pr.Matches(Options{PR: "https://github.com/o/r/pull/123"}); err != nil {
		t.Fatalf("same PR by URL rejected: %v", err)
	}
	if err := pr.Matches(Options{PR: "456"}); err == nil {
		t.Fatal("a different PR was accepted")
	}
	if err := pr.Matches(Options{Branch: "feat/x"}); err == nil {
		t.Fatal("a branch was accepted against a PR session")
	}

	br := &Target{Kind: KindBranch, Label: "feat/x"}
	if err := br.Matches(Options{Branch: "feat/x"}); err != nil {
		t.Fatalf("same branch rejected: %v", err)
	}
	if err := br.Matches(Options{Branch: "feat/y"}); err == nil {
		t.Fatal("a different branch was accepted")
	}
}

func TestMatchesNormalisesRanges(t *testing.T) {
	rg := &Target{Kind: KindRange, Label: "abc..HEAD"}
	if err := rg.Matches(Options{Range: "abc.."}); err != nil {
		t.Fatalf("open-ended range rejected: %v", err)
	}
	if err := rg.Matches(Options{Range: "abc..def"}); err == nil {
		t.Fatal("a different range was accepted")
	}
}

func TestRequestedIsFalseForTheWorkingTree(t *testing.T) {
	if (Options{}).Requested() {
		t.Fatal("the working tree is not a requested target")
	}
	if !(Options{Commit: "HEAD"}).Requested() {
		t.Fatal("--commit is a requested target")
	}
}

// The base ref is fetched on every resolve, even when it is already present.
//
// The head checks itself: prHeadRef compares what it resolves against the
// HeadRefOid gh reported. The base has no sha beside its name, so a stale
// origin/<base> is used exactly as if it were current, and anyone who has not
// pulled since the last merge into the base branch has one. What that costs is
// a wrong diff: the merge base comes out behind, and the change under review
// carries the commits of whatever merged in between.
func TestThePRBaseIsFetchedEvenWhenTheRefIsPresent(t *testing.T) {
	origin := initRepo(t)
	clone := t.TempDir()
	runGit(t, "", "clone", "-q", origin, clone)

	// origin moves on after the clone: the state every stale checkout is in.
	writeCommit(t, origin, "b.go", "package b\n", "second")
	want := strings.TrimSpace(string(mustOutput(t, origin, "rev-parse", "HEAD")))

	repo, err := gitx.Open(clone)
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := repo.Resolve("origin/main")
	if stale == want {
		t.Fatal("the clone is already current; this test needs it stale")
	}

	base, err := prBaseRef(repo, Options{}, &PullRequest{BaseRefName: "main"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Resolve(base)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("base resolved to %s, want the current origin/main %s; it was left at %s",
			short(got), short(want), short(stale))
	}
}

// A fetch needs the network and observing a change does not, so a resolve that
// cannot reach the remote carries on with the ref it has and says so. Losing
// the ability to review offline would be a worse trade than the staleness.
func TestAnUnreachableRemoteFallsBackAndSaysSo(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, dir, "remote", "add", "origin", filepath.Join(dir, "no-such-remote"))

	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	base, err := prBaseRef(repo, Options{Warn: func(s string) { said = append(said, s) }},
		&PullRequest{BaseRefName: "main"})
	if err != nil {
		t.Fatalf("a present ref must survive an unreachable remote: %v", err)
	}
	if base != "origin/main" {
		t.Errorf("base = %q, want origin/main", base)
	}
	if len(said) != 1 || !strings.Contains(said[0], "may be behind") {
		t.Errorf("the fallback must say what it used and why it may be wrong: %v", said)
	}
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func mustOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}
