// Package gitx is the thin git layer Redline's rung-1 panes run on: revision
// resolution, blob listing at a revision, and the working-tree equivalent.
// Everything here is read-only; Redline never mutates the repository.
package gitx

import (
	"bytes"
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

// Fetch runs a read-only fetch from a remote.
func (r *Repo) Fetch(remote, refspec string) error {
	_, err := r.git("fetch", "--quiet", remote, refspec)
	return err
}

// AddWorktree materializes a detached worktree at rev, content-addressed by
// the revision so a second run against the same commit reuses it. Reviewing a
// branch or a PR must never move the user off their own checkout.
func (r *Repo) AddWorktree(rev string) (string, error) {
	root, err := worktreeRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, rev)
	// A worktree for this exact commit is reusable: the revision is immutable,
	// so its checkout is content-addressed by definition.
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if _, err := r.git("worktree", "add", "--detach", "--quiet", dir, rev); err != nil {
		return "", fmt.Errorf("materializing worktree at %s: %w", short(rev), err)
	}
	return dir, nil
}

// worktreeRoot is where detached worktrees live. Outside the repository, so a
// review never appears as untracked files in the tree being reviewed.
func worktreeRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "redline-worktrees"), nil
	}
	return filepath.Join(home, ".redline", "worktrees"), nil
}

// Head returns the commit HEAD points at.
func (r *Repo) Head() (string, error) { return r.Resolve("HEAD") }

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
	args := []string{"diff", "--numstat", "--no-color", rev}
	if head != "" {
		args = append(args, head)
	}
	out, err := r.git(args...)
	if err != nil {
		return nil, err
	}
	var stats []DiffStat
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		stats = append(stats, DiffStat{Path: fields[2], Added: atoi(fields[0]), Removed: atoi(fields[1])})
	}
	return stats, nil
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
func (r *Repo) DiffPath(rev, path string) string {
	out, err := r.git("diff", "--no-color", "-U3", rev, "--", path)
	if err != nil {
		return ""
	}
	return out
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
