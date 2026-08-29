// Package gitx is the thin git layer Redline's panes run on: revision
// resolution, blob listing, diffs, and (for PR/branch targets) fetch and
// detached worktrees. Fetch and AddWorktree mutate refs and worktrees
// outside the user's checkout; they never move HEAD of the repo under review.
package gitx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Repo is a git working copy rooted at Root.
type Repo struct {
	Root string
}

// Open locates the repository containing dir.
func Open(dir string) (*Repo, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}
	return &Repo{Root: strings.TrimSpace(out)}, nil
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

func (r *Repo) git(args ...string) (string, error) { return run(r.Root, args...) }

// Resolve returns the commit SHA a revision names.
func (r *Repo) Resolve(rev string) (string, error) {
	out, err := r.git("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("cannot resolve revision %q", rev)
	}
	return strings.TrimSpace(out), nil
}

// Exists reports whether a revision resolves.
func (r *Repo) Exists(rev string) bool {
	_, err := r.Resolve(rev)
	return err == nil
}

// MergeBase returns the merge base of rev and HEAD. If the two have no common
// ancestor, rev itself is returned — the diff is then the whole history, which
// is the honest answer rather than an error.
func (r *Repo) MergeBase(rev string) (string, error) {
	out, err := r.git("merge-base", rev, "HEAD")
	if err != nil {
		return r.Resolve(rev)
	}
	return strings.TrimSpace(out), nil
}

// Blobs returns path → blob SHA for every file in the tree at rev.
func (r *Repo) Blobs(rev string) (map[string]string, error) {
	out, err := r.git("ls-tree", "-r", "-z", "--format=%(objectname) %(path)", rev)
	if err != nil {
		return nil, err
	}
	blobs := map[string]string{}
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		sha, path, ok := strings.Cut(rec, " ")
		if !ok {
			continue
		}
		blobs[path] = sha
	}
	return blobs, nil
}

// WorktreeBlobs returns path → content SHA for every tracked file plus every
// untracked non-ignored file, hashed as it exists on disk. This is the
// "working tree as a revision" view the execution model diffs against the base:
// Redline is pre-push, so uncommitted work must be in scope.
func (r *Repo) WorktreeBlobs() (map[string]string, error) {
	out, err := r.git("ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	hashes, err := r.hashObjects(paths)
	if err != nil {
		return nil, err
	}
	return hashes, nil
}

// hashObjects hashes files on disk in one batch. git hash-object
// --stdin-paths emits one hash per line, in input order. Paths that have
// disappeared (deleted but still in the index) are dropped first rather than
// failing the whole run.
func (r *Repo) hashObjects(paths []string) (map[string]string, error) {
	var live []string
	for _, p := range paths {
		if st, err := os.Stat(filepath.Join(r.Root, p)); err == nil && st.Mode().IsRegular() {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		return map[string]string{}, nil
	}
	cmd := exec.Command("git", "hash-object", "--stdin-paths")
	cmd.Dir = r.Root
	cmd.Stdin = strings.NewReader(strings.Join(live, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git hash-object: %s", strings.TrimSpace(stderr.String()))
	}
	lines := strings.Fields(stdout.String())
	if len(lines) != len(live) {
		return nil, fmt.Errorf("git hash-object: got %d hashes for %d paths", len(lines), len(live))
	}
	out := make(map[string]string, len(live))
	for i, p := range live {
		out[p] = lines[i]
	}
	return out, nil
}

// Fetch records a remote ref locally. It does not update the current branch.
func (r *Repo) Fetch(remote, refspec string) error {
	_, err := r.git("fetch", "--quiet", remote, refspec)
	return err
}

// AddWorktree materializes a detached worktree at rev, cached by repository
// identity and commit SHA. A second run against the same commit reuses it.
// Reviewing a branch or a PR must never move the user off their own checkout.
//
// Worktrees persist under ~/.redline/worktrees/<repo>/<sha> as a content-
// addressed cache. They are not removed after a run: the revision is
// immutable, so the checkout is reusable. A leftover directory that does
// not belong to this repository is discarded and recreated.
func (r *Repo) AddWorktree(rev string) (string, error) {
	root, err := worktreeRoot()
	if err != nil {
		return "", err
	}
	common, err := r.commonDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, repoKey(common), rev)
	if r.worktreeReusable(dir, common) {
		return dir, nil
	}
	if _, err := os.Stat(dir); err == nil {
		r.dropWorktree(dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if _, err := r.git("worktree", "add", "--detach", "--quiet", dir, rev); err != nil {
		// Another process may have won the race; reuse if the result is ours.
		if r.worktreeReusable(dir, common) {
			return dir, nil
		}
		return "", fmt.Errorf("materializing worktree at %s: %w", short(rev), err)
	}
	return dir, nil
}

func (r *Repo) commonDir() (string, error) {
	out, err := r.git("rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.Root, dir)
	}
	return absClean(dir), nil
}

func repoKey(commonDir string) string {
	sum := sha256.Sum256([]byte(commonDir))
	return hex.EncodeToString(sum[:8])
}

func (r *Repo) worktreeReusable(dir, common string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	got, err := run(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return false
	}
	gotDir := strings.TrimSpace(got)
	if !filepath.IsAbs(gotDir) {
		gotDir = filepath.Join(dir, gotDir)
	}
	return absClean(gotDir) == absClean(common)
}

func (r *Repo) dropWorktree(dir string) {
	_, _ = r.git("worktree", "remove", "--force", dir)
	_ = os.RemoveAll(dir)
	_, _ = r.git("worktree", "prune")
}

func absClean(p string) string {
	p = filepath.Clean(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// worktreeRoot is where detached worktrees live. Outside the repository, so a
// review never appears as untracked files in the tree being reviewed.
// REDLINE_WORKTREE_ROOT overrides the location (tests).
func worktreeRoot() (string, error) {
	if d := os.Getenv("REDLINE_WORKTREE_ROOT"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "redline-worktrees"), nil
	}
	return filepath.Join(home, ".redline", "worktrees"), nil
}

// Head returns the commit HEAD points at.
func (r *Repo) Head() (string, error) { return r.Resolve("HEAD") }

// Parent returns the first parent of rev. A root commit has none.
func (r *Repo) Parent(rev string) (string, error) {
	sha, err := r.Resolve(rev)
	if err != nil {
		return "", err
	}
	out, err := r.git("rev-parse", "--verify", "--quiet", sha+"^")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("revision %s has no parent", short(sha))
	}
	return strings.TrimSpace(out), nil
}

// Log returns the commits on rev that are not on base, newest first.
func (r *Repo) Log(base, rev string) ([]Commit, error) {
	out, err := r.git("log", "--no-color", "--format=%H%x1f%an%x1f%s%x1f%b%x1e", base+".."+rev)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		parts := strings.Split(rec, "\x1f")
		if len(parts) < 3 {
			continue
		}
		c := Commit{SHA: parts[0], Author: parts[1], Subject: parts[2]}
		if len(parts) > 3 {
			c.Body = strings.TrimSpace(parts[3])
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// Commit is one commit in the change under review.
type Commit struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Subject string `json:"subject"`
	Body    string `json:"body,omitempty"`
}

// DiffStat is the per-file added/removed line count between two revisions.
type DiffStat struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Stat returns per-file line counts between rev and head. An empty head means
// the working tree.
func (r *Repo) Stat(rev, head string) ([]DiffStat, error) {
	args := []string{"diff", "--numstat", "-z", "--no-color", rev}
	if head != "" {
		args = append(args, head)
	}
	out, err := r.git(args...)
	if err != nil {
		return nil, err
	}
	var stats []DiffStat
	seen := map[string]bool{}
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		st, ok := parseNumstat(rec)
		if !ok {
			continue
		}
		stats = append(stats, st)
		seen[st.Path] = true
	}
	if head != "" {
		return stats, nil
	}
	// Untracked files are absent from numstat. Count their lines from the
	// same diff the packet will show, so the +N −0 in the report matches.
	changed, err := r.ChangedPaths(rev)
	if err != nil {
		return stats, nil
	}
	for _, path := range changed {
		if seen[path] {
			continue
		}
		added, removed := countDiffLines(r.DiffPath(rev, path))
		stats = append(stats, DiffStat{Path: path, Added: added, Removed: removed})
	}
	return stats, nil
}

func countDiffLines(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return
}

func parseNumstat(rec string) (DiffStat, bool) {
	added, rest, ok := strings.Cut(rec, "\t")
	if !ok {
		return DiffStat{}, false
	}
	removed, path, ok := strings.Cut(rest, "\t")
	if !ok || path == "" {
		return DiffStat{}, false
	}
	return DiffStat{Path: path, Added: atoi(added), Removed: atoi(removed)}, true
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// short abbreviates a SHA for messages.
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// DiffPath returns the unified diff of one path between rev and the working
// tree. Best-effort: evidence capture must never fail a run.
//
// `git diff REV -- path` is empty for untracked files. Those still belong in
// the change — Redline is pre-push — so they are compared against /dev/null.
func (r *Repo) DiffPath(rev, path string) string {
	out, err := r.git("diff", "--no-color", "-U3", rev, "--", path)
	if err == nil && strings.TrimSpace(out) != "" {
		return out
	}
	full := filepath.Join(r.Root, path)
	if _, statErr := os.Stat(full); statErr != nil {
		if out != "" {
			return out
		}
		return ""
	}
	ni, niErr := r.diffNoIndex(path)
	if niErr != nil || strings.TrimSpace(ni) == "" {
		return out
	}
	return ni
}

// diffNoIndex compares path to an empty file. git exits 1 when the files
// differ, which is the successful "here is the diff" case.
func (r *Repo) diffNoIndex(path string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-color", "-U3", "--no-index", "--", os.DevNull, path)
	cmd.Dir = r.Root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return stdout.String(), nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return "", fmt.Errorf("git diff --no-index: %s", msg)
}

// File returns one path's contents at a revision. The empty revision, or the
// Worktree sentinel a pane passes for the head side, reads from disk instead —
// uncommitted work is part of the change Redline reviews.
//
// A missing path is not an error: a spec that does not exist at the base is
// exactly how an added spec looks, and the caller distinguishes the two by the
// empty result.
func (r *Repo) File(rev, path string) string {
	if rev == "" {
		buf, err := os.ReadFile(filepath.Join(r.Root, path))
		if err != nil {
			return ""
		}
		return string(buf)
	}
	out, err := r.git("show", rev+":"+path)
	if err != nil {
		return ""
	}
	return out
}

// AttrSet returns the paths for which a git attribute is explicitly set.
//
// `git check-attr` is asked rather than .gitattributes parsed because the file
// is not the whole answer: attributes come from nested .gitattributes, from
// $GIT_DIR/info/attributes, and later patterns override earlier ones. A
// hand-rolled matcher gets the precedence wrong on exactly the repositories
// that bothered to configure this.
//
// Best-effort: a repository with no attributes at all is the common case, and
// failing the run over it would be absurd.
func (r *Repo) AttrSet(attr string, paths []string) map[string]bool {
	set := map[string]bool{}
	if attr == "" || len(paths) == 0 {
		return set
	}
	args := append([]string{"check-attr", "-z", attr, "--"}, paths...)
	out, err := r.git(args...)
	if err != nil {
		return set
	}
	// -z output is NUL-separated triples: path, attribute, value.
	fields := strings.Split(out, "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		if fields[i+2] == "set" || fields[i+2] == "true" {
			set[fields[i]] = true
		}
	}
	return set
}

// ChangedPaths returns the paths that differ between rev and the working tree,
// including untracked non-ignored files.
func (r *Repo) ChangedPaths(rev string) ([]string, error) {
	base, err := r.Blobs(rev)
	if err != nil {
		return nil, err
	}
	work, err := r.WorktreeBlobs()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for p, sha := range work {
		if base[p] != sha {
			seen[p] = true
			out = append(out, p)
		}
	}
	for p := range base {
		if _, ok := work[p]; !ok && !seen[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}
