// Package target resolves what Redline is being pointed at — the working
// tree, a commit, a commit range, a branch, or a GitHub pull request — into
// a directory to observe and a base revision to observe it against.
//
// Everything here is read-only with respect to GitHub. Redline fetches; it
// never posts, approves, or blocks.
package target

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/chrisophus/redline/internal/gitx"
)

// Kind is what the user pointed Redline at.
type Kind string

const (
	KindWorktree Kind = "worktree" // uncommitted local work — the pre-push case
	KindCommit   Kind = "commit"   // one commit against its parent
	KindRange    Kind = "range"    // A..B
	KindBranch   Kind = "branch"
	KindPR       Kind = "pr"
)

// Target is a resolved review subject.
type Target struct {
	Kind  Kind   `json:"kind"`
	Dir   string `json:"dir"`             // directory the panes observe
	Head  string `json:"head"`            // revision under review; empty means the working tree
	Base  string `json:"base"`            // ref to compare against
	Label string `json:"label,omitempty"` // as the user named it: HEAD, feat/x, abc..def
	// Detached is true when Dir is a throwaway worktree Redline created
	// rather than the caller's own checkout.
	//
	// It decides more than a log line. A detached worktree has none of the
	// artifacts the harness reads -- no coverage profile, no mutants report,
	// no graph -- so `--prepare` has to produce them there, and anything the
	// caller produced by hand in their own tree is invisible. When the
	// checkout already sits on the revision under review, observing it
	// directly is both faster and the only way those artifacts are seen.
	Detached bool `json:"detached,omitempty"`

	// PR metadata, present for KindPR. The description is part of the review:
	// a change that does not do what its author says it does is a finding no
	// amount of code reading will surface.
	PR *PullRequest `json:"pr,omitempty"`
}

// PullRequest is the subset of PR metadata a review needs.
type PullRequest struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Author      string   `json:"author"`
	URL         string   `json:"url"`
	BaseRefName string   `json:"baseRefName"`
	HeadRefName string   `json:"headRefName"`
	HeadRefOid  string   `json:"headRefOid,omitempty"`
	Files       []string `json:"files,omitempty"`
	Draft       bool     `json:"draft"`
}

// Options selects a target. At most one of PR, Branch, Commit, and Range.
type Options struct {
	Dir    string
	PR     string // PR number or URL
	Branch string
	Commit string // a single commit, reviewed against its parent
	Range  string // A..B (B defaults to HEAD)
	Base   string // explicit base ref, overriding the implied one
}

// Resolve turns options into a target, fetching from GitHub if needed.
func Resolve(opts Options) (*Target, error) {
	if err := opts.exclusive(); err != nil {
		return nil, err
	}
	repo, err := gitx.Open(opts.Dir)
	if err != nil {
		return nil, err
	}
	switch {
	case opts.PR != "":
		return resolvePR(repo, opts)
	case opts.Branch != "":
		return resolveBranch(repo, opts)
	case opts.Commit != "":
		return resolveCommit(repo, opts)
	case opts.Range != "":
		return resolveRange(repo, opts)
	default:
		return &Target{Kind: KindWorktree, Dir: repo.Root, Base: opts.Base}, nil
	}
}

func (o Options) exclusive() error {
	var n int
	for _, v := range []string{o.PR, o.Branch, o.Commit, o.Range} {
		if v != "" {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("pass only one of --pr, --branch, --commit, --range")
	}
	return nil
}

// resolveBranch reviews a branch's tip rather than the working tree. The
// branch is observed in a detached worktree so the user's checkout is left
// exactly as it was — Redline is a reviewing tool and must never move someone
// off their own branch.
func resolveBranch(repo *gitx.Repo, opts Options) (*Target, error) {
	return detach(repo, opts.Branch, opts.Base, KindBranch, opts.Branch, "branch")
}

// resolveCommit reviews the tree at REF against its first parent — the
// change that commit introduced — not the whole branch vs origin/main.
func resolveCommit(repo *gitx.Repo, opts Options) (*Target, error) {
	head, err := repo.Resolve(opts.Commit)
	if err != nil {
		return nil, fmt.Errorf("commit %q: %w", opts.Commit, err)
	}
	base := opts.Base
	if base == "" {
		parent, err := repo.Parent(head)
		if err != nil {
			return nil, fmt.Errorf("commit %s has no parent; pass --base REF", shortSHA(head))
		}
		base = parent
	}
	return detach(repo, head, base, KindCommit, opts.Commit, "commit")
}

// resolveRange reviews the tree at B against A. A..B is the set of commits
// reachable from B but not A; the report is the tree diff of that span.
func resolveRange(repo *gitx.Repo, opts Options) (*Target, error) {
	left, right, err := parseRange(opts.Range)
	if err != nil {
		return nil, err
	}
	if _, err := repo.Resolve(left); err != nil {
		return nil, fmt.Errorf("range start %q: %w", left, err)
	}
	base := opts.Base
	if base == "" {
		base = left
	}
	return detach(repo, right, base, KindRange, left+".."+right, "range")
}

func parseRange(s string) (left, right string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("--range needs A..B")
	}
	if i := strings.Index(s, "..."); i >= 0 {
		left, right = s[:i], s[i+3:]
	} else if i := strings.Index(s, ".."); i >= 0 {
		left, right = s[:i], s[i+2:]
	} else {
		return "", "", fmt.Errorf("--range needs A..B (got %q)", s)
	}
	if left == "" {
		return "", "", fmt.Errorf("--range %q: missing start revision", s)
	}
	if right == "" {
		right = "HEAD"
	}
	return left, right, nil
}

// detach resolves headRev and returns the tree to observe it in.
//
// The caller's own checkout is used when it already sits on that revision
// with nothing modified. Redline is a reviewing tool and must never move
// someone off their branch, which is what the worktree was for -- but when
// the checkout is already on the commit under review, copying it into a
// throwaway worktree changes nothing about what is observed and loses
// everything around it: the coverage profile, the mutants report and the
// graph all live in the caller's tree, and a detached worktree has none of
// them. `redline run --pr N` on the branch you are already on was reading a
// bare checkout and reporting the artifacts as missing.
//
// Anything modified or untracked rules it out. The tree would then differ
// from the
// revision the report names, which is a worse failure than a slow one:
// findings from uncommitted edits attributed to a pull request. Ignored
// files are fine; git leaves them out of the question.
func detach(repo *gitx.Repo, headRev, base string, kind Kind, label, what string) (*Target, error) {
	head, err := repo.Resolve(headRev)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", what, headRev, err)
	}
	if dir, ok := localCheckout(repo, head); ok {
		return &Target{Kind: kind, Dir: dir, Head: head, Base: base, Label: label}, nil
	}
	dir, err := repo.AddWorktree(head)
	if err != nil {
		return nil, err
	}
	return &Target{Kind: kind, Dir: dir, Head: head, Base: base, Label: label, Detached: true}, nil
}

// localCheckout reports the caller's tree when it is already on head and
// clean.
func localCheckout(repo *gitx.Repo, head string) (string, bool) {
	local, err := repo.Head()
	if err != nil || local != head {
		return "", false
	}
	clean, err := repo.Clean()
	if err != nil || !clean {
		return "", false
	}
	return repo.Root, true
}

// resolvePR resolves the pull request head and its metadata. When the caller
// already checked out the PR tip (typical in CI), it reuses that tree instead
// of fetching refs/pull/N/head, which GITHUB_TOKEN often cannot read via git.
func resolvePR(repo *gitx.Repo, opts Options) (*Target, error) {
	pr, err := fetchPR(repo.Root, opts.PR)
	if err != nil {
		return nil, err
	}
	base, err := prBaseRef(repo, opts, pr)
	if err != nil {
		return nil, err
	}
	head, err := prHeadRef(repo, pr)
	if err != nil {
		return nil, err
	}
	if dir, ok := localCheckout(repo, head); ok {
		return &Target{Kind: KindPR, Dir: dir, Head: head, Base: base, PR: pr}, nil
	}
	dir, err := repo.AddWorktree(head)
	if err != nil {
		return nil, err
	}
	return &Target{Kind: KindPR, Dir: dir, Head: head, Base: base, PR: pr, Detached: true}, nil
}

func prBaseRef(repo *gitx.Repo, opts Options, pr *PullRequest) (string, error) {
	base := opts.Base
	if base != "" || pr.BaseRefName == "" {
		return base, nil
	}
	base = "origin/" + pr.BaseRefName
	if !repo.Exists(base) {
		if err := repo.Fetch("origin", pr.BaseRefName); err != nil {
			return "", fmt.Errorf("fetching PR base %s: %w", pr.BaseRefName, err)
		}
	}
	return base, nil
}

func prHeadRef(repo *gitx.Repo, pr *PullRequest) (string, error) {
	if pr.HeadRefOid != "" {
		if _, ok := localCheckout(repo, pr.HeadRefOid); ok {
			return pr.HeadRefOid, nil
		}
	}
	if pr.HeadRefName != "" {
		branchRef := "origin/" + pr.HeadRefName
		if !repo.Exists(branchRef) {
			if err := repo.Fetch("origin", pr.HeadRefName); err != nil {
				return "", fmt.Errorf("fetching PR head branch %s: %w", pr.HeadRefName, err)
			}
		}
		if repo.Exists(branchRef) {
			head, err := repo.Resolve(branchRef)
			if err != nil {
				return "", err
			}
			if pr.HeadRefOid == "" || head == pr.HeadRefOid {
				return head, nil
			}
		}
	}
	ref := fmt.Sprintf("refs/pull/%d/head", pr.Number)
	localRef := "refs/redline/pr/" + itoa(pr.Number)
	if err := repo.Fetch("origin", ref+":"+localRef); err != nil {
		return "", fmt.Errorf("fetching PR #%d: %w", pr.Number, err)
	}
	return repo.Resolve(localRef)
}

// fetchPR reads PR metadata through gh, which already holds the user's
// credentials. Redline never handles a token itself.
func fetchPR(dir, ref string) (*PullRequest, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("reviewing a PR needs the gh CLI on PATH: %w", err)
	}
	cmd := exec.Command("gh", "pr", "view", ref,
		"--json", "number,title,body,author,url,baseRefName,headRefName,headRefOid,files,isDraft")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("gh pr view %s: %s", ref, detail)
	}
	var raw struct {
		Number      int                     `json:"number"`
		Title       string                  `json:"title"`
		Body        string                  `json:"body"`
		Author      struct{ Login string }  `json:"author"`
		URL         string                  `json:"url"`
		BaseRefName string                  `json:"baseRefName"`
		HeadRefName string                  `json:"headRefName"`
		HeadRefOid  string                  `json:"headRefOid"`
		IsDraft     bool                    `json:"isDraft"`
		Files       []struct{ Path string } `json:"files"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	pr := &PullRequest{
		Number: raw.Number, Title: raw.Title, Body: raw.Body,
		Author: raw.Author.Login, URL: raw.URL,
		BaseRefName: raw.BaseRefName, HeadRefName: raw.HeadRefName,
		HeadRefOid: raw.HeadRefOid, Draft: raw.IsDraft,
	}
	for _, f := range raw.Files {
		pr.Files = append(pr.Files, f.Path)
	}
	return pr, nil
}

// Cleanup removes a worktree Redline materialized. Safe to call on a
// working-tree target, where it does nothing.
//
// Runs do not call this: detached worktrees are a cache keyed by repository
// and commit SHA (see gitx.AddWorktree). Call Cleanup only when you want to
// drop a worktree you no longer need.
func (t *Target) Cleanup(repoRoot string) error {
	if t.Kind == KindWorktree || t.Dir == "" || t.Dir == repoRoot {
		return nil
	}
	cmd := exec.Command("git", "worktree", "remove", "--force", t.Dir)
	cmd.Dir = repoRoot
	if err := cmd.Run(); err != nil {
		return os.RemoveAll(t.Dir)
	}
	return nil
}

// Describe renders the target for the report header.
func (t *Target) Describe() string {
	switch t.Kind {
	case KindPR:
		return fmt.Sprintf("PR #%d — %s", t.PR.Number, t.PR.Title)
	case KindBranch:
		if t.Label != "" && t.Label != t.Head {
			return "branch " + t.Label + " at " + shortSHA(t.Head)
		}
		return "branch at " + shortSHA(t.Head)
	case KindCommit:
		if t.Label != "" && t.Label != t.Head {
			return "commit " + t.Label + " (" + shortSHA(t.Head) + ")"
		}
		return "commit " + shortSHA(t.Head)
	case KindRange:
		if t.Label != "" {
			return "range " + t.Label
		}
		return "range ending at " + shortSHA(t.Head)
	default:
		return "working tree"
	}
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// Requested reports whether opts name something other than the working tree.
func (o Options) Requested() bool {
	return o.PR != "" || o.Branch != "" || o.Commit != "" || o.Range != ""
}

// Describe names the target opts ask for, without resolving anything.
func (o Options) Describe() string {
	kind, label, err := o.kindLabel()
	if err != nil {
		return o.Range
	}
	if label == "" {
		return string(kind)
	}
	return string(kind) + " " + label
}

func (o Options) kindLabel() (Kind, string, error) {
	switch {
	case o.PR != "":
		return KindPR, o.PR, nil
	case o.Branch != "":
		return KindBranch, o.Branch, nil
	case o.Commit != "":
		return KindCommit, o.Commit, nil
	case o.Range != "":
		left, right, err := parseRange(o.Range)
		if err != nil {
			return "", "", err
		}
		return KindRange, left + ".." + right, nil
	}
	return KindWorktree, "", nil
}

// Matches reports whether opts name this target. It compares what the user
// typed rather than resolved SHAs: the caller is `redline post`, which has
// no repository open, and the mistake worth catching is `run --pr 123`
// followed by `post --pr 456`.
func (t *Target) Matches(o Options) error {
	if err := o.exclusive(); err != nil {
		return err
	}
	kind, label, err := o.kindLabel()
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("this run recorded no target, so --%s cannot be checked against it", string(kind))
	}
	if t.Kind != kind {
		return fmt.Errorf("this run reviewed %s, not %s", t.describeSelf(), o.Describe())
	}
	if kind == KindPR {
		if t.PR == nil || !samePR(o.PR, t.PR) {
			return fmt.Errorf("this run reviewed %s, not %s", t.describeSelf(), o.Describe())
		}
		return nil
	}
	if label != "" && t.Label != "" && label != t.Label {
		return fmt.Errorf("this run reviewed %s, not %s", t.describeSelf(), o.Describe())
	}
	return nil
}

func (t *Target) describeSelf() string {
	if t.Kind == KindPR && t.PR != nil {
		return fmt.Sprintf("pr %d", t.PR.Number)
	}
	if t.Label != "" {
		return string(t.Kind) + " " + t.Label
	}
	return string(t.Kind)
}

// OwnerRepo reads the owner and repository out of the pull request's own URL.
//
// It lives here because two callers need it and the URL is the target's, not
// either caller's: `post` addresses the GitHub API with it, and `run` reads
// the pull request's own review threads back with it. A second copy of this
// parse is a second thing to keep in step with whatever GitHub Enterprise
// path a user turns out to have.
func (p *PullRequest) OwnerRepo() (owner, repo string, err error) {
	if p == nil {
		return "", "", fmt.Errorf("no pull request metadata")
	}
	u, err := url.Parse(strings.TrimSpace(p.URL))
	if err != nil || u.Path == "" {
		return "", "", fmt.Errorf("cannot read owner/repo from PR URL %q", p.URL)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot read owner/repo from PR URL %q", p.URL)
	}
	return parts[0], parts[1], nil
}

// samePR accepts the number or any URL ending in it, matching what gh takes.
func samePR(ref string, pr *PullRequest) bool {
	ref = strings.TrimSpace(ref)
	if ref == itoa(pr.Number) {
		return true
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return strings.TrimSpace(ref[i+1:]) == itoa(pr.Number)
	}
	return false
}
