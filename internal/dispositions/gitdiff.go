package dispositions

import (
	"context"
	"strings"
)

// GitDiffer implements Differ with `git diff` against a local checkout. The commits must be present: fetch
// refs/pull/<n>/head first (see FetchPRCommits).
type GitDiffer struct {
	git     func(args ...string) ([]byte, error)
	changes map[string]FileChange
	// statuses holds each commit's file statuses, read once per commit.
	statuses map[string]map[string]fileStatus
}

// NewGitDiffer runs git through r, under ctx.
func NewGitDiffer(ctx context.Context, r Runner) *GitDiffer {
	return &GitDiffer{git: func(args ...string) ([]byte, error) { return r.Run(ctx, "git", args...) }}
}

// fileStatus is one path in `git diff --name-status`: its status letter and, for a rename, the new path.
type fileStatus struct {
	status byte
	to     string
}

// Change returns what commit did to path against its first parent. A commit git cannot diff (missing
// locally, or a root commit) is an error, never "no change": a silent empty diff would read every resolved
// finding as declined. A rename is followed, so a moved file does not read as deleted.
func (g *GitDiffer) Change(commit, path string) (FileChange, error) {
	key := commit + "|" + path
	if c, ok := g.changes[key]; ok {
		return c, nil
	}
	statuses, err := g.status(commit)
	if err != nil {
		return FileChange{}, err
	}
	var c FileChange
	st, touched := statuses[path]
	switch {
	case !touched:
	case st.status == 'D':
		c.Deleted = true
	default:
		args := []string{"diff", "-M", "-U0", "--no-color", commit + "^1", commit, "--", path}
		if st.status == 'R' {
			c.RenamedTo = st.to
			args = append(args, st.to)
		}
		diff, err := g.git(args...)
		if err != nil {
			return FileChange{}, err
		}
		c.Hunks = ParseHunks(string(diff))
	}
	if g.changes == nil {
		g.changes = map[string]FileChange{}
	}
	g.changes[key] = c
	return c, nil
}

// status reads every path the commit touched, by its old path. Rename detection needs the whole commit in
// view: limited to one path, git cannot see where the file went and reports a delete.
func (g *GitDiffer) status(commit string) (map[string]fileStatus, error) {
	if s, ok := g.statuses[commit]; ok {
		return s, nil
	}
	out, err := g.git("diff", "-M", "--name-status", "-z", commit+"^1", commit)
	if err != nil {
		return nil, err
	}
	s := parseNameStatus(string(out))
	if g.statuses == nil {
		g.statuses = map[string]map[string]fileStatus{}
	}
	g.statuses[commit] = s
	return s, nil
}

// parseNameStatus reads `git diff --name-status -z`: a status, then one path, or two for a rename or copy.
// -z leaves paths unquoted, so a name with spaces or non-ASCII letters reads as written.
func parseNameStatus(out string) map[string]fileStatus {
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	res := map[string]fileStatus{}
	for i := 0; i+1 < len(fields); {
		st := fields[i]
		if st == "" {
			i++
			continue
		}
		switch st[0] {
		case 'R', 'C':
			if i+2 >= len(fields) {
				return res
			}
			if st[0] == 'R' {
				res[fields[i+1]] = fileStatus{status: 'R', to: fields[i+2]}
			}
			i += 3
		default:
			res[fields[i+1]] = fileStatus{status: st[0]}
			i += 2
		}
	}
	return res
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
