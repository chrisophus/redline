// Package target resolves what Redline is being pointed at — the working
// tree, a branch, or a GitHub pull request — into a directory to observe and
// a base revision to observe it against.
//
// Everything here is read-only with respect to GitHub. Redline fetches; it
// never posts, approves, or blocks.
package target

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ccason/redline/internal/gitx"
)

// Kind is what the user pointed Redline at.
type Kind string

const (
	KindWorktree Kind = "worktree" // uncommitted local work — the pre-push case
	KindBranch   Kind = "branch"
	KindPR       Kind = "pr"
)

// Target is a resolved review subject.
type Target struct {
	Kind Kind   `json:"kind"`
	Dir  string `json:"dir"`  // directory the panes observe
	Head string `json:"head"` // revision under review; empty means the working tree
	Base string `json:"base"` // ref to compare against

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
	Files       []string `json:"files,omitempty"`
	Draft       bool     `json:"draft"`
}

// Options selects a target. At most one of PR and Branch may be set.
type Options struct {
	Dir    string
	PR     string // PR number or URL
	Branch string
	Base   string // explicit base ref, overriding the PR's own base
}

// Resolve turns options into a target, fetching from GitHub if needed.
func Resolve(opts Options) (*Target, error) {
	repo, err := gitx.Open(opts.Dir)
	if err != nil {
		return nil, err
	}
	switch {
	case opts.PR != "" && opts.Branch != "":
		return nil, fmt.Errorf("pass --pr or --branch, not both")
	case opts.PR != "":
		return resolvePR(repo, opts)
	case opts.Branch != "":
		return resolveBranch(repo, opts)
	default:
		return &Target{Kind: KindWorktree, Dir: repo.Root, Base: opts.Base}, nil
	}
}

// resolveBranch reviews a branch's tip rather than the working tree. The
// branch is observed in a detached worktree so the user's checkout is left
// exactly as it was — Redline is a reviewing tool and must never move someone
// off their own branch.
func resolveBranch(repo *gitx.Repo, opts Options) (*Target, error) {
	head, err := repo.Resolve(opts.Branch)
	if err != nil {
		return nil, fmt.Errorf("branch %q: %w", opts.Branch, err)
	}
	dir, err := repo.AddWorktree(head)
	if err != nil {
		return nil, err
	}
	return &Target{Kind: KindBranch, Dir: dir, Head: head, Base: opts.Base}, nil
}

// resolvePR fetches the pull request's head commit and its metadata. The
// branch is fetched, not checked out: reviewing someone's PR must not touch
// your own working tree.
func resolvePR(repo *gitx.Repo, opts Options) (*Target, error) {
	pr, err := fetchPR(repo.Root, opts.PR)
	if err != nil {
		return nil, err
	}
	ref := fmt.Sprintf("refs/pull/%d/head", pr.Number)
	if err := repo.Fetch("origin", ref+":refs/redline/pr/"+itoa(pr.Number)); err != nil {
		return nil, fmt.Errorf("fetching PR #%d: %w", pr.Number, err)
	}
	head, err := repo.Resolve("refs/redline/pr/" + itoa(pr.Number))
	if err != nil {
		return nil, err
	}
	base := opts.Base
	if base == "" && pr.BaseRefName != "" {
		base = "origin/" + pr.BaseRefName
		if !repo.Exists(base) {
			_ = repo.Fetch("origin", pr.BaseRefName)
		}
	}
	dir, err := repo.AddWorktree(head)
	if err != nil {
		return nil, err
	}
	return &Target{Kind: KindPR, Dir: dir, Head: head, Base: base, PR: pr}, nil
}

// fetchPR reads PR metadata through gh, which already holds the user's
// credentials. Redline never handles a token itself.
func fetchPR(dir, ref string) (*PullRequest, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("reviewing a PR needs the gh CLI on PATH: %w", err)
	}
	cmd := exec.Command("gh", "pr", "view", ref,
		"--json", "number,title,body,author,url,baseRefName,headRefName,files,isDraft")
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
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		Author      struct{ Login string } `json:"author"`
		URL         string `json:"url"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
		IsDraft     bool   `json:"isDraft"`
		Files       []struct{ Path string } `json:"files"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	pr := &PullRequest{
		Number: raw.Number, Title: raw.Title, Body: raw.Body,
		Author: raw.Author.Login, URL: raw.URL,
		BaseRefName: raw.BaseRefName, HeadRefName: raw.HeadRefName, Draft: raw.IsDraft,
	}
	for _, f := range raw.Files {
		pr.Files = append(pr.Files, f.Path)
	}
	return pr, nil
}

// Cleanup removes a worktree Redline materialized. Safe to call on a
// working-tree target, where it does nothing.
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
		return "branch at " + shortSHA(t.Head)
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

// WorktreeRoot is where Redline materializes detached worktrees.
func WorktreeRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "redline-worktrees")
	}
	return filepath.Join(home, ".redline", "worktrees")
}
