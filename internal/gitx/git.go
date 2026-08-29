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
