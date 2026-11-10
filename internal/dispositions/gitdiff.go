package dispositions

import (
	"context"
	"strings"
)

// GitDiffer implements Differ with `git diff` against a local checkout. The commits must be present: fetch
// refs/pull/<n>/head first (see FetchPRCommits).
type GitDiffer struct {
	git   func(args ...string) ([]byte, error)
	cache map[string]diffResult
}

// NewGitDiffer runs git through r, under ctx.
func NewGitDiffer(ctx context.Context, r Runner) *GitDiffer {
	return &GitDiffer{git: func(args ...string) ([]byte, error) { return r.Run(ctx, "git", args...) }}
}

type diffResult struct {
	hunks   []Hunk
	deleted bool
}

// Hunks returns what commit changed in path against its first parent. A commit git cannot diff (missing
// locally, or a root commit) is an error, never "no change": a silent empty diff would read every resolved
// finding as declined.
func (g *GitDiffer) Hunks(commit, path string) ([]Hunk, bool, error) {
	key := commit + "|" + path
	if r, ok := g.cache[key]; ok {
		return r.hunks, r.deleted, nil
	}
	// --no-renames makes a moved file a delete plus an add, so a rename reads as the file being deleted.
	status, err := g.git("diff", "--no-renames", "--name-status", commit+"^1", commit, "--", path)
	if err != nil {
		return nil, false, err
	}
	res := diffResult{deleted: strings.HasPrefix(strings.TrimSpace(string(status)), "D")}
	if !res.deleted {
		diff, err := g.git("diff", "--no-renames", "-U0", "--no-color", commit+"^1", commit, "--", path)
		if err != nil {
			return nil, false, err
		}
		res.hunks = ParseHunks(string(diff))
	}
	if g.cache == nil {
		g.cache = map[string]diffResult{}
	}
	g.cache[key] = res
	return res.hunks, res.deleted, nil
}

// FetchPRCommits makes a PR's commits available locally, including those of merged and force-pushed PRs.
// It writes objects to the repository's object store and nothing else: no branch or tag moves.
func FetchPRCommits(ctx context.Context, r Runner, repo, number string) error {
	_, err := r.Run(ctx, "git", "fetch", "--quiet", "--no-tags", fetchRemote(ctx, r, repo), "pull/"+number+"/head")
	return err
}

// fetchRemote is origin when origin is the repository being recorded, so its URL and credentials (SSH or
// HTTPS) are used as configured. Otherwise --repo names another repository and it is fetched over HTTPS.
func fetchRemote(ctx context.Context, r Runner, repo string) string {
	out, err := r.Run(ctx, "git", "remote", "get-url", "origin")
	if err == nil && remoteNames(strings.TrimSpace(string(out)), repo) {
		return "origin"
	}
	return "https://github.com/" + repo + ".git"
}

// remoteNames is whether a git remote URL (https://github.com/o/n.git, git@github.com:o/n.git or
// ssh://git@github.com/o/n) points at the GitHub repository repo.
func remoteNames(url, repo string) bool {
	u := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git"))
	want := strings.ToLower(repo)
	return strings.HasSuffix(u, "github.com/"+want) || strings.HasSuffix(u, "github.com:"+want)
}
