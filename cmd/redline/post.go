package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/post"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

// cmdPost submits the session's findings as one GitHub pull request review.
//
// This is the one Redline command that writes to GitHub, and it is never a side
// effect: `run` stays read-only, and posting happens only when the operator
// types `redline post`. It refuses unless the session it loads is the PR
// named: posting a review of one change onto another PR is exactly the
// mistake worth a hard error.
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
	// review bodies it already left. This check happens before opening a
	// connection, so a missing gh command fails immediately instead of
	// partway through the post.
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
		if err := enforceProfile(o.dryRun, prof, tgt, num); err != nil {
			return err
		}
	}
	payload := post.BuildAttest(&res.Report, tgt, o.reportURL, commentable, prof, changedFiles(res.Change))
	walkthrough := prof != nil && prof.BodyStyle == post.BodyWalkthrough
	if walkthrough {
		// The walkthrough body opens with who reviewed and, when the profile
		// asks for it, the author's stated intent, both from gh. Each degrades
		// to empty offline, where the body simply omits the line rather than
		// rendering a broken one.
		reviewedBy, _ := ghLogin()
		intent := ""
		if prof.Includes("intent") {
			intent = statedIntent(owner, repo, num)
		}
		payload = payload.WithMeta(intent, reviewedBy)
	}

	// A session outlives the head it observed, so a review can be posted
	// against a commit that is no longer the tip. In CI that is a race: a
	// push can happen between the review step and the post step, so this is
	// not a mistake. The
	// review still posts, with a notice in the body, because the body is what
	// stays on the pull request and the review is already paid for. What a
	// profile's require_head withholds is the gate verdict, so a stale review
	// cannot satisfy a gate that wants one covering the current commit. It
	// used to refuse the whole post, which threw the review away.
	head, headErr := ghPRHead(owner, repo, num)
	switch {
	case headErr != nil && prof != nil && prof.RequireHead && !o.dryRun:
		return fmt.Errorf("profile require_head: could not read the PR head: %w", headErr)
	case headErr != nil:
		fmt.Fprintf(os.Stderr, "redline: could not read the PR head (%v); "+
			"cannot say whether this review describes the current commit\n", headErr)
	case head != tgt.Head:
		payload = payload.Stale(head)
		fmt.Fprintf(os.Stderr, "redline: this session reviewed %s and the PR head is now %s; "+
			"posting with a notice, re-run `redline run --pr %d` for a review of the current commit\n",
			shortSHA(tgt.Head), shortSHA(head), num)
		if prof != nil && !payload.Attested() {
			fmt.Fprintf(os.Stderr, "redline: profile require_head: the review carries no gate verdict, "+
				"since it is not of the PR head\n")
		}
	}

	if o.dryRun {
		// No earlier reviews offline, so a recap needs its commit from the
		// session or from --since.
		payload, err = withRecap(o, payload, res.Report.Agent, walkthrough, nil, "", tgt.Head)
		if err != nil {
			return err
		}
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
	payload, err = withRecap(o, payload, res.Report.Agent, walkthrough, reviews, me, tgt.Head)
	if err != nil {
		return err
	}
	alreadyReviewed := post.ReviewedAt(reviewBodies, tgt.Head)
	attestSame := true
	if payload.Attested() {
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
	switch {
	case payload.Attested():
		fmt.Fprintf(os.Stderr, " verdict=%s", payload.GateVerdict)
	case payload.GateVerdict != "":
		fmt.Fprint(os.Stderr, " verdict withheld: not of the PR head")
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// withRecap opens the body with what changed since the previous review in
// place of the overview, when the session has that paragraph.
//
// It happens without --recap on a walkthrough body whenever it can, because
// the paragraph exists only when `redline review --since` was asked for it.
// --recap makes it required: every reason it cannot happen is then an error
// rather than a quiet fall back to the overview, since a recap that was asked
// for and is not there reads as the review having nothing to say about what
// moved.
//
// The commit comes from the session first, since that is the one the
// paragraph was written against; then --since, for a session from before the
// commit was stored; then the marker on the latest earlier review.
func withRecap(o opts, payload post.Payload, agent *findings.AgentReview, walkthrough bool,
	reviews []ghAuthoredBody, me, head string) (post.Payload, error) {
	recap, stored := "", ""
	var files []string
	if agent != nil {
		recap, stored, files = agent.Recap, agent.RecapSince, agent.RecapFiles
	}
	if o.since != "" && stored != "" && !strings.HasPrefix(stored, o.since) {
		return payload, fmt.Errorf("--since %s: this session's recap was written against %s; "+
			"drop --since, or re-run `redline review --since %s`", o.since, shortSHA(stored), o.since)
	}
	since := cmp.Or(stored, o.since)
	if since == "" {
		since = latestReviewedHead(reviews, me, head)
	}
	if !o.recap {
		if !walkthrough || recap == "" || since == "" {
			return payload, nil
		}
		return payload.WithRecap(recap, since, files), nil
	}
	switch {
	case !walkthrough:
		// WithRecap only reaches the body buildBodyWalkthrough writes, so on
		// the evidence body the flag would change nothing.
		return payload, fmt.Errorf("--recap replaces the overview, and this post writes the evidence body, "+
			"which has none; post with a profile whose body_style is %s", post.BodyWalkthrough)
	case since == "":
		return payload, fmt.Errorf("--recap replaces the overview with what changed since the previous review, " +
			"and no earlier Redline review was found on this pull request; drop --recap, or pass --since COMMIT")
	case recap == "":
		return payload, fmt.Errorf("--recap needs the paragraph the describing call writes, and this session has none; "+
			"re-run `redline review --since %s` first", shortSHA(since))
	}
	return payload.WithRecap(recap, since, files), nil
}

// enforceProfile applies author-only. HEAD freshness is not a refusal: a
// review of a commit the PR has moved past still posts, and cmdPost withholds
// its gate verdict when the profile requires the head. Dry-run without gh
// skips the network checks so the payload can still be previewed offline.
func enforceProfile(dryRun bool, prof *post.Profile, tgt *target.Target, num int) error {
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

// changedPaths is every path in the change, for callers that want the paths
// alone: the answering scout walks up from each one to find the guideline
// files nearest the code a question is about.
func changedPaths(ch *change.Set) []string {
	files := changedFiles(ch)
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// changedFiles is every file in the change, for the walkthrough table and the
// composition table. It comes from the session the run wrote, the same file
// list the HTML report groups, so the posted body observes nothing.
func changedFiles(ch *change.Set) []change.File {
	if ch == nil {
		return nil
	}
	return ch.Files
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
// that quotes a fake verdict marker cannot smuggle one into the review body
// Redline signs. Whole HTML-comment lines go too, which is where a marker
// normally sits and so is the check that does most of the work.
//
// Marker names are repository policy and this function is not told which ones
// are configured, so the backstop behind that matches the shape the names share
// rather than naming one repository's. It used to test for a single gate's two
// markers, which meant every other repository's markers passed straight
// through. A repository whose markers share no shape with these still relies on
// the HTML-comment rule above.
func sanitizeIntent(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "<!--") || strings.Contains(l, "-->") {
			continue
		}
		if strings.Contains(l, "-agent-review") || strings.Contains(l, "-agent-finding") ||
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
	// At is when the item was submitted or written, as GitHub's own RFC 3339
	// string, which sorts correctly as text. Carried because the order the
	// API returns reviews in is not something to rest a choice on: picking
	// the previous review by position is right only while that order holds,
	// and a recap measured from the wrong baseline says nothing about it.
	At string
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
		// A review carries submitted_at and a comment created_at.
		SubmittedAt string `json:"submitted_at"`
		CreatedAt   string `json:"created_at"`
	}
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, err
	}
	var items []ghAuthoredBody
	for _, page := range pages {
		for _, it := range page {
			items = append(items, ghAuthoredBody{
				Login: it.User.Login, Body: it.Body, At: cmp.Or(it.SubmittedAt, it.CreatedAt),
			})
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

// latestReviewedHead is the commit the most recent earlier review ran against,
// read from the marker each posted review carries.
//
// By submitted time rather than by position in the list. The GitHub API does
// return reviews oldest first, and reading the last one worked because of
// that, but nothing here checks it and a recap measured from the wrong
// baseline describes the wrong change without saying anything is amiss. The
// timestamps are RFC 3339 from one clock, so they sort as text; where two
// match, or where a body carries none, the later item still wins, which is
// the order the list came in.
//
// A review at the head being posted now is skipped: that is this review being
// re-posted, not a previous one to measure against.
func latestReviewedHead(reviews []ghAuthoredBody, login, head string) string {
	var bestAt, best string
	for _, r := range reviews {
		if r.Login != login {
			continue
		}
		for _, h := range post.ReviewedHeads([]string{r.Body}) {
			if h == head {
				continue
			}
			if best == "" || r.At >= bestAt {
				bestAt, best = r.At, h
			}
		}
	}
	return best
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
