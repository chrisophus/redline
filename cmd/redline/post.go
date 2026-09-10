package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/post"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

// cmdPost submits the session's findings as one GitHub pull request review.
//
// This is the one Redline command that writes to GitHub, and it is never a side
// effect: `run` stays read-only, and posting happens only when the operator
// types `redline post`. It refuses unless the session it loads is the PR named
// — posting a review of one change onto another PR is exactly the mistake
// worth a hard error.
func cmdPost(o opts) error {
	res, err := run.LoadSession(o.out)
	if err != nil {
		return err
	}
	tgt := res.Target
	if tgt == nil || tgt.Kind != target.KindPR || tgt.PR == nil {
		return fmt.Errorf("post needs a PR session; run `redline run --pr N` first")
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

	owner, repo, err := tgt.PR.OwnerRepo()
	if err != nil {
		return err
	}
	num := tgt.PR.Number

	// A real post needs gh for its credentials, the pull request head and the
	// review bodies it already left. Fail before opening a connection rather
	// than part way through.
	if !o.dryRun {
		if _, err := exec.LookPath("gh"); err != nil {
			return fmt.Errorf("posting a review needs the gh CLI on PATH: %w", err)
		}
	}
	// The set of lines GitHub will accept a comment on, taken from the diff the
	// session recorded against its own head. The review anchors to that head
	// (payload.CommitID) and GitHub validates a comment's line against the
	// commit the review names, so the check is against the session's diff, not
	// the pull request's current diff: after a push the two differ and a line
	// valid on the new head can be one GitHub rejects on the commit this review
	// is for. A finding pointing off the diff rides in the body, not as a line
	// comment: the review API is all-or-nothing, so one out-of-diff comment
	// 422s the whole submission.
	commentable := sessionCommentable(res.Change)
	var prof *post.Profile
	if o.profile != "" {
		var perr error
		prof, perr = post.LoadProfile(o.profile)
		if perr != nil {
			return perr
		}
		if err := enforceProfile(o.dryRun, prof, tgt, owner, repo, num); err != nil {
			return err
		}
	}
	payload := post.BuildAttest(&res.Report, tgt, o.reportURL, commentable, prof, changedPaths(res.Change))
	if prof != nil && prof.BodyStyle == post.BodyWalkthrough {
		// The walkthrough body opens with who reviewed and the author's stated
		// intent, both from gh. Each degrades to empty offline, where the body
		// simply omits the line rather than rendering a broken one.
		reviewedBy, _ := ghLogin()
		payload = payload.WithMeta(statedIntent(owner, repo, num), reviewedBy)
	}

	// A session outlives the head it observed, so a review can be posted
	// against a commit that is no longer the tip. require_head refuses that,
	// but only when a profile asked for it; without one the review used to go
	// up silently describing code that had moved. The body is the artifact
	// that stays on the pull request, so the notice belongs there rather than
	// only on this terminal.
	if head, err := ghPRHead(owner, repo, num); err != nil {
		fmt.Fprintf(os.Stderr, "redline: could not read the PR head (%v); "+
			"cannot say whether this review describes the current commit\n", err)
	} else if head != tgt.Head {
		fmt.Fprintf(os.Stderr, "redline: this session reviewed %s and the PR head is now %s; "+
			"re-run `redline run --pr %d` for a review of the current commit\n",
			shortSHA(tgt.Head), shortSHA(head), num)
		payload.Body = fmt.Sprintf("> This review is of `%s`, which is no longer the head of this "+
			"pull request (`%s`). Findings below may already be addressed.\n\n",
			shortSHA(tgt.Head), shortSHA(head)) + payload.Body
	}

	if o.dryRun {
		return emitJSON(reviewRequest(payload))
	}

	// Read what Redline already said on this PR so a re-post neither duplicates
	// a line comment nor re-reviews a commit it has already reviewed. Only the
	// posting login's own comments count. The suppression markers are hidden
	// HTML comments, and anyone who can comment on a public pull request could
	// paste one; honouring a marker from another author would let them silence
	// a finding. The trusted author is the authenticated gh user this posts as.
	me, err := ghLogin()
	if err != nil {
		return err
	}
	comments, err := ghAuthoredField(owner, repo, num, "comments")
	if err != nil {
		return err
	}
	reviews, err := ghAuthoredField(owner, repo, num, "reviews")
	if err != nil {
		return err
	}
	commentBodies := trustedBodies(comments, me)
	reviewBodies := trustedBodies(reviews, me)
	// Both kinds of body, not just the comments: a finding with no line, or one
	// off the diff, was marked in the review body it rode in, and reading only
	// the comment bodies would offer it again on every re-post.
	posted := post.Fingerprints(append(append([]string{}, commentBodies...), reviewBodies...))
	payload = payload.Unposted(posted)
	alreadyReviewed := post.ReviewedAt(reviewBodies, tgt.Head)
	attestSame := true
	if prof != nil {
		prev := post.AttestedVerdict(reviewBodies, prof.ReviewMarker, tgt.Head)
		attestSame = prev == payload.GateVerdict
	}

	// NothingNew counts body findings as well as line comments. The earlier gate
	// looked only at the comments, so a session whose new finding could not be
	// anchored to a line was reported as nothing to post. A profiled pass after
	// a fail on the same HEAD still posts: the gate needs a new verdict.
	if payload.NothingNew() && alreadyReviewed && attestSame {
		fmt.Fprintf(os.Stderr, "redline: already reviewed %s and no new findings; nothing to post\n", shortSHA(tgt.Head))
		return nil
	}

	if err := submitReview(owner, repo, num, payload); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Posted review to %s (%d new line comment(s))", tgt.PR.URL, len(payload.Comments))
	if payload.GateVerdict != "" {
		fmt.Fprintf(os.Stderr, " verdict=%s", payload.GateVerdict)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// enforceProfile applies author-only and HEAD freshness. Dry-run without gh
// skips the network checks so the payload can still be previewed offline.
func enforceProfile(dryRun bool, prof *post.Profile, tgt *target.Target, owner, repo string, num int) error {
	if prof == nil {
		return nil
	}
	if _, err := exec.LookPath("gh"); err != nil {
		if dryRun {
			return nil
		}
		return fmt.Errorf("posting a profiled review needs the gh CLI on PATH: %w", err)
	}
	if prof.AuthorOnly {
		login, err := ghLogin()
		if err != nil {
			if dryRun {
				fmt.Fprintf(os.Stderr, "redline: could not read gh user (%v); skipping author check\n", err)
			} else {
				return err
			}
		} else {
			author := ""
			if tgt.PR != nil {
				author = tgt.PR.Author
			}
			if author == "" {
				return fmt.Errorf("profile author_only: session has no PR author; re-run `redline run --pr %d`", num)
			}
			if login != author {
				return fmt.Errorf("profile author_only: only the PR author (%s) may post (gh user: %s)", author, login)
			}
		}
	}
	if prof.RequireHead {
		head, err := ghPRHead(owner, repo, num)
		if err != nil {
			if dryRun {
				fmt.Fprintf(os.Stderr, "redline: could not read PR head (%v); skipping HEAD check\n", err)
			} else {
				return err
			}
		} else if head != tgt.Head {
			return fmt.Errorf("profile require_head: session reviewed %s but PR head is %s; re-run `redline run --pr %d`", shortSHA(tgt.Head), shortSHA(head), num)
		}
	}
	return nil
}

// ghLogin returns the GitHub login Redline posts as. User OAuth/PAT tokens
// answer GET /user; GitHub App installation tokens cannot (403) and GET /app
// requires a JWT. GraphQL viewer returns the bot login ({slug}[bot]) for both.
func ghLogin() (string, error) {
	if login := strings.TrimSpace(os.Getenv("REDLINE_GH_LOGIN")); login != "" {
		return login, nil
	}
	login, userErr := ghAPIJQ("user", ".login")
	if userErr == nil && login != "" {
		return login, nil
	}
	login, viewerErr := ghGraphQLViewerLogin()
	if viewerErr == nil && login != "" {
		return login, nil
	}
	if viewerErr != nil {
		return "", viewerErr
	}
	if userErr != nil {
		return "", userErr
	}
	return "", fmt.Errorf("resolve GitHub actor login: empty GraphQL viewer login")
}

func ghGraphQLViewerLogin() (string, error) {
	cmd := exec.Command("gh", "api", "graphql",
		"-f", "query=query { viewer { login } }",
		"--jq", ".data.viewer.login")
	out, err := cmd.Output()
	if err != nil {
		return "", ghError("graphql viewer", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func ghAPIJQ(path, jq string) (string, error) {
	cmd := exec.Command("gh", "api", path, "--jq", jq)
	out, err := cmd.Output()
	if err != nil {
		return "", ghError(path, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func ghPRHead(owner, repo string, num int) (string, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, num)
	cmd := exec.Command("gh", "api", path, "--jq", ".head.sha")
	out, err := cmd.Output()
	if err != nil {
		return "", ghError(path, err)
	}
	return strings.TrimSpace(string(out)), nil
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
		// Empty means the new file, which is where all but a comment on a
		// removed line belongs.
		side := c.Side
		if side == "" {
			side = "RIGHT"
		}
		gc := ghReviewComment{
			Path: c.Path, Line: c.Line, Side: side, Body: c.Body,
		}
		if c.StartLine > 0 && c.StartLine <= c.Line {
			gc.StartLine = c.StartLine
			gc.StartSide = side
		}
		req.Comments = append(req.Comments, gc)
	}
	return req
}

// submitReview posts one review via `gh api`.
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
	return nil
}

// sessionCommentable resolves the lines a review comment may anchor to from the
// diff the session recorded against its own head. That diff is what the review
// is posted against, so a line it shows is one GitHub accepts on the commit the
// review names. A file with no recorded diff contributes nothing, so its
// findings ride in the body; a session that recorded no diff at all resolves to
// nil, which the payload builder reads as "do not filter", the same as an
// offline preview.
func sessionCommentable(ch *change.Set) map[string]map[int]bool {
	if ch == nil || len(ch.Files) == 0 {
		return nil
	}
	patches := make(map[string]string, len(ch.Files))
	for _, f := range ch.Files {
		if f.Diff != "" {
			patches[f.Path] = f.Diff
		}
	}
	if len(patches) == 0 {
		return nil
	}
	return post.CommentableLines(patches)
}

// changedPaths is every path in the change, for the walkthrough table. It
// comes from the session the run wrote, the same file list the report's own
// walkthrough uses, so the posted body observes nothing.
func changedPaths(ch *change.Set) []string {
	if ch == nil {
		return nil
	}
	out := make([]string, 0, len(ch.Files))
	for _, f := range ch.Files {
		out = append(out, f.Path)
	}
	return out
}

// maxIntent bounds the stated-intent block so a long PR description does not
// crowd out the findings. The full body is on the pull request itself.
const maxIntent = 2000

// statedIntent is the PR title and sanitized body, for the walkthrough body's
// "Stated intent". Empty when gh cannot be reached, which the body renders as
// no intent line rather than a broken one.
func statedIntent(owner, repo string, num int) string {
	out, err := exec.Command("gh", "pr", "view", fmt.Sprintf("%d", num),
		"--repo", owner+"/"+repo, "--json", "title,body").Output()
	if err != nil {
		return ""
	}
	var v struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if json.Unmarshal(out, &v) != nil {
		return ""
	}
	intent := strings.TrimSpace(v.Title)
	if body := sanitizeIntent(v.Body); body != "" {
		intent += "\n\n" + body
	}
	if len(intent) > maxIntent {
		intent = intent[:maxIntent] + "…"
	}
	return intent
}

// sanitizeIntent drops the marker lines a PR body might carry, so a description
// that quotes a fake mct-agent-review or redline marker cannot smuggle one into
// the review body Redline signs. Whole HTML-comment lines go too.
func sanitizeIntent(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "<!--") || strings.Contains(l, "-->") {
			continue
		}
		if strings.Contains(l, "mct-agent-review") || strings.Contains(l, "mct-agent-finding") ||
			strings.Contains(l, "redline:") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ghAuthoredBody is one comment or review with the login that wrote it, so the
// caller can keep only the ones the posting login left.
type ghAuthoredBody struct {
	Login string
	Body  string
}

// ghAuthoredField returns every item's author login and body under a PR
// sub-resource (comments or reviews), paginated. The author travels with the
// body because the caller trusts a suppression marker only from its own login.
func ghAuthoredField(owner, repo string, num int, sub string) ([]ghAuthoredBody, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/%s", owner, repo, num, sub)
	// --paginate with --slurp returns one array across all pages. Without
	// --slurp gh concatenates a separate array per page, which is not valid JSON.
	cmd := exec.Command("gh", "api", "--paginate", "--slurp", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, ghError(path, err)
	}
	// Each element is one page's array of items.
	var pages [][]struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, err
	}
	var items []ghAuthoredBody
	for _, page := range pages {
		for _, it := range page {
			items = append(items, ghAuthoredBody{Login: it.User.Login, Body: it.Body})
		}
	}
	return items, nil
}

// trustedBodies keeps only the bodies authored by the posting login. The
// suppression markers Redline reads back are hidden comments anyone could
// paste, so a marker is honoured only when the tool itself wrote the comment
// carrying it.
func trustedBodies(items []ghAuthoredBody, login string) []string {
	var out []string
	for _, it := range items {
		if it.Login == login {
			out = append(out, it.Body)
		}
	}
	return out
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

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
