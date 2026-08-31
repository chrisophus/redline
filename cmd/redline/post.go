package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/ccason/redline/internal/packet"
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
	nar := narrative(res.Review)
	if res.Packet != nil && res.Packet.Brief != nil {
		nar.Coverage.BriefFiles = len(res.Packet.Brief.FilesRead)
	}
	payload := post.Build(&res.Report, tgt, nar, o.reportURL, commentable)

	// A review nobody can read anything in is worse than no review: it appears
	// on the pull request under the reviewer's name and says nothing. This is
	// what `redline review --pr N && redline post` produced, with no ingest in
	// between to supply the summary and the walkthrough.
	if payload.Blank() {
		return fmt.Errorf("this session has nothing to post: no findings, no summary and no walkthrough\n" +
			"run the agent review and `redline ingest` first, or pass --report-url to post a link to the report")
	}

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
	// Both blobs, not just the comments: a finding with no line, or one off the
	// diff, was marked in the review body it rode in, and reading only the
	// comment bodies would offer it again on every re-post.
	posted := post.Fingerprints([]string{commentBlob, reviewBlob})
	payload = payload.Unposted(posted)
	alreadyReviewed := post.ReviewedAt([]string{reviewBlob}, tgt.Head)

	// NothingNew counts body findings as well as line comments. The earlier gate
	// looked only at the comments, so a session whose new finding could not be
	// anchored to a line was reported as nothing to post.
	if payload.NothingNew() && alreadyReviewed {
		fmt.Fprintf(os.Stderr, "redline: already reviewed %s and no new findings; nothing to post\n", shortSHA(tgt.Head))
		return nil
	}

	if err := submitReview(owner, repo, num, payload); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Posted review to %s (%d new line comment(s))\n", tgt.PR.URL, len(payload.Comments))
	return nil
}

// narrative lifts the agent's prose — summary, actual, walkthrough, and
// discrepancies — out of the ingested review. Stated intent is not here: Build
// reads it off the pull request Redline already fetched. A session posted
// before any ingest has no review, and the body degrades to the findings alone.
func narrative(r *packet.Review) post.Narrative {
	if r == nil {
		return post.Narrative{}
	}
	nar := post.Narrative{Summary: r.Summary, Actual: r.Actual}
	for _, f := range r.Files {
		nar.Files = append(nar.Files, post.FileNote{Path: f.Path, Summary: f.Summary})
	}
	for _, d := range r.Discrepancies {
		nar.Discrepancies = append(nar.Discrepancies, post.DiscrepancyNote{
			Claim:  d.Claim,
			Actual: d.Actual,
		})
	}
	return nar
}

// ghReviewComment and ghReviewRequest mirror the GitHub "create a review" API.
type ghReviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
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
		gc := ghReviewComment{
			Path: c.Path, Line: c.Line, Side: "RIGHT", Body: c.Body,
		}
		if c.StartLine > 0 && c.StartLine <= c.Line {
			gc.StartLine = c.StartLine
			gc.StartSide = "RIGHT"
		}
		req.Comments = append(req.Comments, gc)
	}
	return req
}

// submitReview posts one review via `gh api`, then patches the body so
// discrepancy placeholders become permalinks to the inline comments. Comment
// IDs do not exist until the review is created, so the overview cannot thread
// them in one shot. A failed PATCH leaves the posted review intact and warns;
// retrying into a second review would duplicate comments.
func submitReview(owner, repo string, num int, p post.Payload) error {
	body, err := json.Marshal(reviewRequest(p))
	if err != nil {
		return err
	}
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, num)
	cmd := exec.Command("gh", "api", "--method", "POST", path, "--input", "-")
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh api POST %s: %s", path, strings.TrimSpace(string(out)))
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if jsonErr := json.Unmarshal(out, &created); jsonErr != nil || created.ID == 0 {
		// The review landed; we just cannot thread. Say so and stop.
		fmt.Fprintf(os.Stderr, "redline: posted review but could not read its id; discrepancy links were not threaded\n")
		return nil
	}
	if err := threadReview(owner, repo, num, created.ID, p); err != nil {
		fmt.Fprintf(os.Stderr, "redline: posted review #%d but could not thread discrepancy links: %v\n", created.ID, err)
	}
	return nil
}

// threadReview fetches the comments on a just-created review, maps them to
// finding fingerprints, and patches the review body with permalinks.
func threadReview(owner, repo string, num int, reviewID int64, p post.Payload) error {
	ids, err := reviewCommentIDs(owner, repo, num, reviewID)
	if err != nil {
		return err
	}
	threaded := post.ThreadBody(p.Body, ids)
	if threaded == p.Body {
		return nil
	}
	patch, err := json.Marshal(map[string]string{"body": threaded})
	if err != nil {
		return err
	}
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews/%d", owner, repo, num, reviewID)
	cmd := exec.Command("gh", "api", "--method", "PUT", path, "--input", "-")
	cmd.Stdin = bytes.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh api PUT %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

// reviewCommentIDs maps finding fingerprint to GitHub review-comment id for the
// comments belonging to one review.
func reviewCommentIDs(owner, repo string, num int, reviewID int64) (map[string]int64, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews/%d/comments", owner, repo, num, reviewID)
	cmd := exec.Command("gh", "api", "--paginate", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, ghError(path, err)
	}
	var comments []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(out, &comments); err != nil {
		return nil, err
	}
	ids := map[string]int64{}
	for _, c := range comments {
		for fp := range post.Fingerprints([]string{c.Body}) {
			// Fingerprints keys are head\x00fingerprint; recover the fingerprint.
			if i := strings.IndexByte(fp, 0); i >= 0 {
				ids[fp[i+1:]] = c.ID
			}
		}
	}
	return ids, nil
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
// omitted, so its findings fall to the body rather than risk an invalid comment.
//
// Every page is read. Stopping at the first hundred was safe, because an
// unlisted file's findings ride in the body and that never 422s, but on a large
// pull request it demoted findings that GitHub would have accepted inline.
func ghPullFiles(owner, repo string, num int) (map[string]string, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, num)
	// --paginate with --slurp returns one array across all pages. Without
	// --slurp gh concatenates a separate array per page, which is not valid JSON.
	cmd := exec.Command("gh", "api", "--paginate", "--slurp", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, ghError(path, err)
	}
	// Each element is one page's array of files.
	var pages [][]struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	}
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, err
	}
	var raw []struct {
		Filename string `json:"filename"`
		Patch    string `json:"patch"`
	}
	for _, page := range pages {
		raw = append(raw, page...)
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
		return "", ghError(path, err)
	}
	return string(out), nil
}

// ghError explains a failed `gh api` call. gh writes its diagnosis to stderr,
// which only an *exec.ExitError carries, so anything else (gh missing from PATH,
// a signal, a pipe that could not be opened) has to report the error itself.
// Reading only ExitError left those cases with a message that named the path and
// then said nothing, and wrapped no error to unwrap.
func ghError(path string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if detail := strings.TrimSpace(string(ee.Stderr)); detail != "" {
			return fmt.Errorf("gh api %s: %s", path, detail)
		}
	}
	return fmt.Errorf("gh api %s: %w", path, err)
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

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
