package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/ccason/redline/internal/post"
	"github.com/ccason/redline/internal/run"
	"github.com/ccason/redline/internal/target"
)

// cmdPost submits the session's findings as one GitHub pull request review.
//
// This is the one Redline command that writes to GitHub, and it is never a side
// effect: `run`, `review`, and `ingest` stay read-only, and posting happens
// only when the operator types `redline post`. It refuses unless the session it
// loads is the PR named — merging a review of one change onto another PR under
// a reviewer's name is exactly the mistake worth a hard error.
func cmdPost(o opts) error {
	res, err := run.LoadSession(o.out)
	if err != nil {
		return err
	}
	tgt := res.Target
	if tgt == nil || tgt.Kind != target.KindPR || tgt.PR == nil {
		return fmt.Errorf("post needs a PR review session; run `redline review --pr N` first")
	}
	// A named target must be this PR. A non-PR target flag is a category error.
	switch {
	case o.pr != "":
		if err := tgt.Matches(o.target()); err != nil {
			return fmt.Errorf("post: %w", err)
		}
	case o.branch != "" || o.commit != "" || o.revRange != "":
		return fmt.Errorf("post targets the pull request this session reviewed; drop --branch/--commit/--range")
	}

	owner, repo, err := parseRepoURL(tgt.PR.URL)
	if err != nil {
		return err
	}
	num := tgt.PR.Number

	// The set of lines GitHub will accept a comment on. A finding pointing off
	// the diff must ride in the body, not as a line comment: the review API is
	// all-or-nothing, so one out-of-diff comment 422s the whole submission.
	commentable, err := prCommentable(o.dryRun, owner, repo, num)
	if err != nil {
		return err
	}
	payload := post.Build(&res.Report, tgt, o.reportURL, commentable)

	if o.dryRun {
		return emitJSON(reviewRequest(payload))
	}

	// Read what Redline already said on this PR so a re-post neither duplicates a
	// line comment nor re-reviews a commit it has already reviewed. Bodies are
	// scanned as one blob: fingerprint and head-SHA markers survive regardless of
	// how the newlines in each body fall.
	commentBlob, err := ghAPIField(owner, repo, num, "comments")
	if err != nil {
		return err
	}
	reviewBlob, err := ghAPIField(owner, repo, num, "reviews")
	if err != nil {
		return err
	}
	posted := post.Fingerprints([]string{commentBlob})
	payload = payload.Unposted(posted)
	alreadyReviewed := post.ReviewedAt([]string{reviewBlob}, tgt.Head)

	if len(payload.Comments) == 0 && alreadyReviewed {
		fmt.Fprintf(os.Stderr, "redline: already reviewed %s and no new findings; nothing to post\n", short(tgt.Head))
		return nil
	}

	if err := submitReview(owner, repo, num, payload); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Posted review to %s (%d new line comment(s))\n", tgt.PR.URL, len(payload.Comments))
	return nil
}

// ghReviewComment and ghReviewRequest mirror the GitHub "create a review" API.
type ghReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

type ghReviewRequest struct {
	CommitID string            `json:"commit_id,omitempty"`
	Body     string            `json:"body"`
	Event    string            `json:"event"`
	Comments []ghReviewComment `json:"comments,omitempty"`
}

func reviewRequest(p post.Payload) ghReviewRequest {
	req := ghReviewRequest{CommitID: p.CommitID, Body: p.Body, Event: post.Event}
	for _, c := range p.Comments {
		req.Comments = append(req.Comments, ghReviewComment{
			Path: c.Path, Line: c.Line, Side: "RIGHT", Body: c.Body,
		})
	}
	return req
}

// submitReview posts one review via `gh api`, handing gh the request as JSON on
// stdin. gh holds the user's credentials; Redline never handles a token.
func submitReview(owner, repo string, num int, p post.Payload) error {
	body, err := json.Marshal(reviewRequest(p))
	if err != nil {
		return err
	}
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, num)
	cmd := exec.Command("gh", "api", "--method", "POST", path, "--input", "-")
	cmd.Stdin = bytes.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh api POST %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

// prCommentable resolves the lines GitHub will accept comments on for this PR.
// A real post requires gh and the PR's file list; a dry-run degrades to nil (no
// filtering) when gh is absent or the fetch fails, so a payload can still be
// previewed offline.
func prCommentable(dryRun bool, owner, repo string, num int) (map[string]map[int]bool, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		if dryRun {
			return nil, nil
		}
		return nil, fmt.Errorf("posting a review needs the gh CLI on PATH: %w", err)
	}
	patches, err := ghPullFiles(owner, repo, num)
	if err != nil {
		if dryRun {
			fmt.Fprintf(os.Stderr, "redline: could not fetch the PR diff (%v); previewing without diff filtering\n", err)
			return nil, nil
		}
		return nil, err
	}
	return post.CommentableLines(patches), nil
}

// ghPullFiles returns the unified-diff patch for each file in the PR, keyed by
// path. A file with no patch (binary, or too large for GitHub to return) is
// omitted, so its findings fall to the body rather than risk an invalid
// comment. Up to 100 files are read in one page; on a larger PR the unlisted
// files' findings simply ride in the body, which is safe, never a 422.
func ghPullFiles(owner, repo string, num int) (map[string]string, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, num)
	cmd := exec.Command("gh", "api", path)
	out, err := cmd.Output()
	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("gh api %s: %s", path, detail)
	}
	var raw []struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(raw))
	for _, f := range raw {
		if f.Patch != "" {
			m[f.Filename] = f.Patch
		}
	}
	return m, nil
}

// ghAPIField returns the concatenated `body` fields of every item under a PR
// sub-resource (comments or reviews), paginated. Bodies are joined into one
// string because the caller only scans them for markers.
func ghAPIField(owner, repo string, num int, sub string) (string, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/%s", owner, repo, num, sub)
	cmd := exec.Command("gh", "api", "--paginate", "-q", ".[].body", path)
	out, err := cmd.Output()
	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("gh api %s: %s", path, detail)
	}
	return string(out), nil
}

// parseRepoURL pulls owner and repo from a PR HTML URL like
// https://github.com/owner/repo/pull/7.
func parseRepoURL(raw string) (owner, repo string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Path == "" {
		return "", "", fmt.Errorf("cannot read owner/repo from PR URL %q", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot read owner/repo from PR URL %q", raw)
	}
	return parts[0], parts[1], nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
