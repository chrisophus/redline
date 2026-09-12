package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/post"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/report"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/scout"
	"github.com/chrisophus/redline/internal/target"
)

// hold binds every port announce would try, so serving cannot succeed. Each
// listener accepts and immediately hangs up rather than going silent: a
// silent listener costs the probe its full timeout, which would make this
// test pay eight seconds for a thing it is not testing.
func hold(t *testing.T, port, span int) {
	t.Helper()
	for p := port; p < port+span; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func(ln net.Listener) {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}(ln)
	}
}

func reportDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html>Redline</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A run that produced a report must not exit non-zero because the port
// was unavailable. The agent driving the session reads a non-zero exit as a
// failed run and may start the whole loop again.
func TestAnnounceSurvivesAnUnavailablePort(t *testing.T) {
	dir := reportDir(t)
	// 40000 is outside the default span, so this does not fight a real server.
	hold(t, 40000, 40)

	if err := announce(dir, 40000, false); err != nil {
		t.Fatalf("a written report reported failure: %v", err)
	}
}

// The opposite case must still fail: no report means the run produced nothing.
func TestAnnounceFailsWhenThereIsNoReport(t *testing.T) {
	if err := announce(t.TempDir(), 40100, false); err == nil {
		t.Fatal("a missing report should be an error")
	}
}

func TestAnnounceServesWhenItCan(t *testing.T) {
	dir := reportDir(t)
	t.Cleanup(func() { _ = report.Stop(dir) })

	// Under go test, startServe will not re-exec this binary. announce
	// must still succeed: the report is on disk even if nothing is listening.
	if err := announce(dir, 40200, false); err != nil {
		t.Fatal(err)
	}
}

// worktreeSession writes an evidence directory recording a run on the worktree.
func worktreeSession(t *testing.T) string {
	t.Helper()
	dir := reportDir(t)
	res := &run.Result{
		Report: findings.Report{BaseRef: "origin/main", BaseSHA: "abc123"},
		Change: &change.Set{Target: &target.Target{Kind: target.KindWorktree}},
	}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	return dir
}

// prSession writes an evidence directory recording a run on PR 7, with one
// located finding so post has a line comment to build.
func prSession(t *testing.T) string {
	t.Helper()
	dir := reportDir(t)
	rep := findings.Report{
		BaseRef: "origin/main", BaseSHA: "abc123",
		Findings: []findings.Finding{{
			File: "a.go", Line: 12, Rule: "migration-modified-after-merge",
			Substrate: "migrations", Category: findings.CategorySchema,
			Severity: findings.SeverityError, Message: "merged migration edited",
		}},
		Substrates: []findings.SubstrateStatus{{Name: "migrations", State: findings.SubstrateRan}},
	}
	rep.Finalize()
	res := &run.Result{
		Report: rep,
		Change: &change.Set{Target: prSessionTarget()},
	}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	return dir
}

func prSessionTarget() *target.Target {
	return &target.Target{
		Kind: target.KindPR, Head: "deadbeef",
		PR: &target.PullRequest{Number: 7, URL: "https://github.com/o/r/pull/7"},
	}
}

// captureStdout runs fn with os.Stdout replaced and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	os.Stdout = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// post is a write. Every one of these tests must reach the point of deciding
// what to send without sending it, so none of them may find gh on PATH: an
// empty PATH makes --dry-run take its documented offline path and makes a real
// post fail before it opens a connection.
func TestPostDryRunEmitsThePayloadWithoutPosting(t *testing.T) {
	t.Setenv("PATH", "")
	dir := prSession(t)

	var err error
	out := captureStdout(t, func() {
		err = cmdPost(opts{out: dir, pr: "7", dryRun: true, port: 40500, noOpen: true})
	})
	if err != nil {
		t.Fatalf("dry run should succeed offline: %v", err)
	}

	var req ghReviewRequest
	if jsonErr := json.Unmarshal([]byte(out), &req); jsonErr != nil {
		t.Fatalf("dry run should print the review request as JSON: %v\n%s", jsonErr, out)
	}
	if req.Event != post.Event {
		t.Fatalf("a posted review reports and does not gate, got event %q", req.Event)
	}
	if req.CommitID != "deadbeef" {
		t.Fatalf("the review anchors to the session head, got %q", req.CommitID)
	}
	if len(req.Comments) != 1 || req.Comments[0].Path != "a.go" {
		t.Fatalf("the located finding should be a line comment: %+v", req.Comments)
	}
	if !strings.Contains(req.Body, "| migrations | ran — 1 finding(s) |") {
		t.Fatalf("the body should carry the evidence table:\n%s", req.Body)
	}
}

func writeCmdProfile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "redline-review.yml")
	body := "review_marker: mct-agent-review:v1\nfinding_marker: mct-agent-finding:v1\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func passSession(t *testing.T) string {
	t.Helper()
	dir := reportDir(t)
	rep := findings.Report{BaseRef: "origin/main", BaseSHA: "abc123"}
	rep.Finalize()
	res := &run.Result{
		Report: rep,
		Change: &change.Set{Target: prSessionTarget()},
	}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPostDryRunProfileEmitsFailMarker(t *testing.T) {
	t.Setenv("PATH", "")
	dir := prSession(t)
	var err error
	out := captureStdout(t, func() {
		err = cmdPost(opts{out: dir, pr: "7", profile: writeCmdProfile(t), dryRun: true, port: 41200, noOpen: true})
	})
	if err != nil {
		t.Fatalf("dry run with profile should succeed offline: %v", err)
	}
	if !strings.Contains(out, "mct-agent-review:v1 verdict=fail head=deadbeef") {
		t.Fatalf("profiled fail should stamp the gate marker:\n%s", out)
	}
	if !strings.Contains(out, "mct-agent-finding:v1 severity=high") {
		t.Fatalf("blocking finding should stamp the finding marker:\n%s", out)
	}
}

func TestPostDryRunProfilePassWithNoFindings(t *testing.T) {
	t.Setenv("PATH", "")
	dir := passSession(t)
	var err error
	out := captureStdout(t, func() {
		err = cmdPost(opts{out: dir, pr: "7", profile: writeCmdProfile(t), dryRun: true, port: 41300, noOpen: true})
	})
	if err != nil {
		t.Fatalf("a profiled pass still posts: %v", err)
	}
	var req ghReviewRequest
	if jsonErr := json.Unmarshal([]byte(out), &req); jsonErr != nil {
		t.Fatalf("dry run JSON: %v\n%s", jsonErr, out)
	}
	if len(req.Comments) != 0 {
		t.Fatalf("pass has no inline findings: %+v", req.Comments)
	}
	if !strings.Contains(req.Body, "verdict=pass head=deadbeef") {
		t.Fatalf("pass marker missing:\n%s", req.Body)
	}
}

// Posting a review of one change onto another pull request puts words in the
// reviewer's mouth. It is a hard error, not a warning.
func TestPostRejectsAMismatchedTarget(t *testing.T) {
	t.Setenv("PATH", "")
	dir := prSession(t)

	err := cmdPost(opts{out: dir, pr: "456", dryRun: true, port: 40600, noOpen: true})
	if err == nil {
		t.Fatal("posting a PR 7 session to PR 456 should fail")
	}
	if !strings.Contains(err.Error(), "not pr 456") {
		t.Fatalf("error should name the mismatch, got: %v", err)
	}
}

// A worktree session has no pull request to post to.
func TestPostRejectsANonPRSession(t *testing.T) {
	t.Setenv("PATH", "")
	dir := worktreeSession(t)

	err := cmdPost(opts{out: dir, dryRun: true, port: 40700, noOpen: true})
	if err == nil {
		t.Fatal("posting a worktree session should fail")
	}
	if !strings.Contains(err.Error(), "needs a PR session") {
		t.Fatalf("error should say what post needs, got: %v", err)
	}
}

// post follows the session's own target. Naming a different kind of target is a
// category error rather than something to resolve.
func TestPostRejectsANonPRTargetFlag(t *testing.T) {
	t.Setenv("PATH", "")
	dir := prSession(t)

	err := cmdPost(opts{out: dir, branch: "feature", dryRun: true, port: 40800, noOpen: true})
	if err == nil {
		t.Fatal("post --branch should fail")
	}
	if !strings.Contains(err.Error(), "drop --branch") {
		t.Fatalf("error should say which flags to drop, got: %v", err)
	}
}

// A real post needs gh for the credentials and for the PR's changed lines. It
// must fail before opening a connection rather than part way through.
func TestPostRequiresGhForARealPost(t *testing.T) {
	t.Setenv("PATH", "")
	dir := prSession(t)

	err := cmdPost(opts{out: dir, pr: "7", port: 41000, noOpen: true})
	if err == nil {
		t.Fatal("a real post without gh should fail")
	}
	if !strings.Contains(err.Error(), "gh CLI") {
		t.Fatalf("error should name the missing dependency, got: %v", err)
	}
}

// ghItem is one comment or review with the login that wrote it.
type ghItem struct {
	login string
	body  string
}

// fakeGh puts a stand-in `gh` on PATH that answers the reads post makes: the
// authenticated user's login, the PR head, an empty file list, and the comment
// and review bodies with their authors. login is who the review posts as; only
// items by that login carry a marker post will honour. It refuses a write, so
// an already-posted payload must never reach submitReview.
func fakeGh(t *testing.T, login string, comments, reviews []ghItem) {
	t.Helper()
	bin := t.TempDir()
	// The stand-in is a shell script so the test does not need to compile a
	// second Go binary. It classifies the call by scanning every argument, then
	// answers with a shell builtin only: PATH is set to this directory alone, so
	// no external command (not even cat) is reachable.
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("set -e\n")
	b.WriteString("for a in \"$@\"; do\n")
	b.WriteString("  case \"$a\" in POST|--method) echo 'fake gh: unexpected write' >&2; exit 2;; esac\n")
	b.WriteString("done\n")
	b.WriteString("sub=\"\"\n")
	b.WriteString("for a in \"$@\"; do\n")
	b.WriteString("  case \"$a\" in\n")
	b.WriteString("    user) sub=user;;\n")
	b.WriteString("    *'/comments'*) sub=comments;;\n")
	b.WriteString("    *'/reviews'*) sub=reviews;;\n")
	b.WriteString("    *'/files'*) sub=files;;\n")
	b.WriteString("    *'/pulls/'*) [ -z \"$sub\" ] && sub=head;;\n")
	b.WriteString("  esac\n")
	b.WriteString("done\n")
	b.WriteString("case \"$sub\" in\n")
	fmt.Fprintf(&b, "  user) printf '%%s\\n' %s;;\n", shQuote(login))
	fmt.Fprintf(&b, "  head) printf '%%s\\n' %s;;\n", shQuote(prSessionTarget().Head))
	b.WriteString("  files) printf '%s\\n' '[[]]';;\n")
	fmt.Fprintf(&b, "  comments) printf '%%s' %s;;\n", shQuote(ghPagesJSON(t, comments)))
	fmt.Fprintf(&b, "  reviews) printf '%%s' %s;;\n", shQuote(ghPagesJSON(t, reviews)))
	b.WriteString("  *) echo \"fake gh: unexpected args $*\" >&2; exit 3;;\n")
	b.WriteString("esac\n")
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// shQuote wraps a value in single quotes for the fake gh script, so its content
// is literal to the shell: no expansion, whatever bytes a body carries.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ghPagesJSON renders items as the paginated JSON gh returns for a PR
// sub-resource: one page holding each item's author login and body.
func ghPagesJSON(t *testing.T, items []ghItem) string {
	t.Helper()
	type user struct {
		Login string `json:"login"`
	}
	type item struct {
		User user   `json:"user"`
		Body string `json:"body"`
	}
	page := make([]item, 0, len(items))
	for _, it := range items {
		page = append(page, item{User: user{Login: it.login}, Body: it.body})
	}
	out, err := json.Marshal([][]item{page})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A re-post against a commit Redline has already reviewed, with every finding
// already marked in those bodies, must exit cleanly and must not call
// submitReview.
func TestPostSkipsWhenAlreadyReviewedWithNoNewFindings(t *testing.T) {
	dir := prSession(t)

	// Build the same payload post would, then feed its markers back through the
	// stand-in so the gate sees exactly what a prior successful post left.
	res, err := run.LoadSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	payload := post.Build(&res.Report, res.Target, "", nil)
	if len(payload.Comments) != 1 {
		t.Fatalf("precondition: one line comment, got %d", len(payload.Comments))
	}
	const login = "redline-bot"
	fakeGh(t, login,
		[]ghItem{{login, payload.Comments[0].Body}},
		[]ghItem{{login, payload.Body}})

	stderr := captureStderr(t, func() {
		err = cmdPost(opts{out: dir, pr: "7", port: 41100, noOpen: true})
	})
	if err != nil {
		t.Fatalf("already-posted should succeed with nothing to do: %v", err)
	}
	if !strings.Contains(stderr, "nothing to post") {
		t.Fatalf("stderr should say nothing to post, got: %q", stderr)
	}
}

// captureStderr runs fn with os.Stderr replaced and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// startServe refuses to re-exec this binary. If a child is invoked anyway —
// `redline.test serve …` — Go's flag parser would stop at "serve", ignore
// the flags, and run the suite again. Route those arguments to runMain so
// the child is a real server and nothing recurses.
func TestMain(m *testing.M) {
	if args := os.Args[1:]; len(args) > 0 && isSubcommand(args[0]) {
		if err := runMain(args); err != nil {
			fmt.Fprintln(os.Stderr, "redline:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func isSubcommand(arg string) bool {
	switch arg {
	case "run", "post", "open", "serve":
		return true
	}
	return false
}

// openFile is the no-server path: it points straight at report.html on disk,
// so a report that lands in .redline (a directory the file picker hides) is
// reachable without a server or a picker.
func TestOpenFilePrintsFileURLWithoutServing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		if err := openFile(dir, false); err != nil {
			t.Fatalf("openFile: %v", err)
		}
	})
	want := "file://" + filepath.Join(dir, "report.html")
	if !strings.Contains(stderr, want) {
		t.Errorf("must print the file URL %q, got %q", want, stderr)
	}
}

func TestOpenFileMissingReportIsAnError(t *testing.T) {
	if err := openFile(t.TempDir(), false); err == nil {
		t.Fatal("a missing report must be an error, not a silent success")
	}
}

// A dry run answers exactly one question: what would be sent. The system
// block is half the request — the harness instructions plus every provider's
// language fragment, which come from another repository entirely — so a dry
// run that printed only the user turn answered the question wrong while
// looking complete. It calls nothing, so this needs no key and no network.
func TestReviewDryRunPrintsTheSystemBlockAndTheUserTurn(t *testing.T) {
	dir := worktreeSession(t)

	var err error
	out := captureStdout(t, func() {
		err = cmdReview(opts{out: dir, dryRun: true, noOpen: true})
	})
	if err != nil {
		t.Fatalf("a dry run calls nothing and should succeed: %v", err)
	}
	system, prompt, ok := strings.Cut(out, "--- prompt ---")
	if !ok {
		t.Fatalf("the two halves of the request must be distinguishable:\n%s", out)
	}
	system = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(system), "--- system ---"))
	if system == "" {
		t.Fatalf("the dry run printed no system block, so it did not show what would be sent:\n%s", out)
	}
	if !strings.Contains(prompt, "## The change") {
		t.Fatalf("the user turn must still be printed after the system block:\n%s", out)
	}
}

// A comment carrying side LEFT must post LEFT. Every comment used to go up
// RIGHT, which lands a remark about a removed line on the new-file line of the
// same number: different code.
func TestReviewRequestHonorsCommentSide(t *testing.T) {
	p := post.Payload{Comments: []post.Comment{
		{Path: "a.go", Line: 3, Side: "LEFT", Body: "the removed guard mattered"},
		{Path: "b.go", Line: 5, Body: "new code"},
	}}
	req := reviewRequest(p)
	if len(req.Comments) != 2 {
		t.Fatalf("want two comments: %+v", req.Comments)
	}
	if req.Comments[0].Side != "LEFT" {
		t.Fatalf("a LEFT comment posted %q", req.Comments[0].Side)
	}
	if req.Comments[1].Side != "RIGHT" {
		t.Fatalf("an unnamed side must default to RIGHT, got %q", req.Comments[1].Side)
	}
}

// A suppression marker is a hidden HTML comment anyone who can comment on a
// public PR could paste. Only the posting login's own comments carry a marker
// Redline honours, so a marker from another author must not suppress a finding.
func TestSuppressionMarkersFromOtherAuthorsAreIgnored(t *testing.T) {
	rep := findings.Report{Findings: []findings.Finding{{
		File: "a.go", Line: 12, Rule: "migration-modified-after-merge", Substrate: "migrations",
		Category: findings.CategorySchema, Severity: findings.SeverityError, Message: "merged migration edited",
	}}}
	rep.Finalize()
	p := post.Build(&rep, prSessionTarget(), "", map[string]map[int]bool{"a.go": {12: true}})
	if len(p.Comments) != 1 {
		t.Fatalf("precondition: one comment carrying a marker, got %d", len(p.Comments))
	}
	body := p.Comments[0].Body
	const me = "redline-bot"

	fromOther := trustedBodies([]ghAuthoredBody{{Login: "mallory", Body: body}}, me)
	if got := post.Fingerprints(fromOther); len(got) != 0 {
		t.Fatalf("a marker from another author was honoured: %v", got)
	}
	fromMe := trustedBodies([]ghAuthoredBody{{Login: me, Body: body}}, me)
	if got := post.Fingerprints(fromMe); len(got) != 1 {
		t.Fatalf("the posting login's own marker must be honoured, got %d", len(got))
	}
}

func TestGhLoginHonoursREDLINE_GH_LOGIN(t *testing.T) {
	t.Setenv("REDLINE_GH_LOGIN", "marketplace-review-bot[bot]")
	login, err := ghLogin()
	if err != nil {
		t.Fatal(err)
	}
	if login != "marketplace-review-bot[bot]" {
		t.Fatalf("got %q", login)
	}
}

// The review anchors to the session head, so its line comments have to be
// validated against that head's diff, not the pull request's current one.
// Commentable lines come from the session's own recorded diff: a finding on a
// line the session diff shows becomes a comment, one that is not rides in the
// body, even offline with no PR diff fetched.
func TestPostAnchorsCommentsToTheSessionDiff(t *testing.T) {
	t.Setenv("PATH", "")
	dir := reportDir(t)
	rep := findings.Report{
		BaseRef: "origin/main", BaseSHA: "abc123",
		Findings: []findings.Finding{
			{File: "a.go", Line: 12, Rule: "r1", Substrate: "s", Category: findings.CategorySchema,
				Severity: findings.SeverityError, Message: "in the session diff"},
			{File: "a.go", Line: 99, Rule: "r2", Substrate: "s", Category: findings.CategorySchema,
				Severity: findings.SeverityError, Message: "not in the session diff"},
		},
		Substrates: []findings.SubstrateStatus{{Name: "s", State: findings.SubstrateRan}},
	}
	rep.Finalize()
	diff := "@@ -10,3 +12,3 @@\n ctx 12\n ctx 13\n ctx 14\n"
	res := &run.Result{
		Report: rep,
		Change: &change.Set{
			Target: prSessionTarget(),
			Files:  []change.File{{Path: "a.go", Status: "modified", Diff: diff}},
		},
	}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	var err error
	out := captureStdout(t, func() {
		err = cmdPost(opts{out: dir, pr: "7", dryRun: true, port: 40900, noOpen: true})
	})
	if err != nil {
		t.Fatalf("dry run should succeed offline: %v", err)
	}
	var req ghReviewRequest
	if e := json.Unmarshal([]byte(out), &req); e != nil {
		t.Fatalf("dry run JSON: %v\n%s", e, out)
	}
	if len(req.Comments) != 1 || req.Comments[0].Line != 12 {
		t.Fatalf("only the line in the session diff is a comment: %+v", req.Comments)
	}
	if !strings.Contains(req.Body, "not in the session diff") {
		t.Fatalf("the off-diff finding should ride in the body:\n%s", req.Body)
	}
}

// The scout speaks either wire now, so on the OpenAI wire the checking pass is
// wired to run rather than skipped, over the same OpenAI credentials the
// review used. A session with no working tree still has nothing to look up.
func TestScoutRunsOnTheOpenAIWire(t *testing.T) {
	res := &run.Result{Target: &target.Target{Dir: t.TempDir()}}
	if scoutAnswerer(res, review.Options{API: review.APIOpenAI, APIKey: "sk-openai"}, scoutSettings{}, nil) == nil {
		t.Fatal("the scout should be wired to run on the OpenAI wire")
	}
	gone := &run.Result{Target: &target.Target{Dir: filepath.Join(t.TempDir(), "missing")}}
	if scoutAnswerer(gone, review.Options{API: review.APIOpenAI, APIKey: "sk-openai"}, scoutSettings{}, nil) != nil {
		t.Fatal("with no working tree there is nothing to look up")
	}
}

// --model and --effort used to stop at stage one, so a review run on another
// model checked its findings with the scout's defaults. The lookups take the
// review's flags, and an unset flag stays unset so the scout's own defaults
// apply to it.
func TestTheLookupsRunOnTheReviewsModelAndEffort(t *testing.T) {
	res := &run.Result{}
	ropts := review.Options{Model: "claude-opus-5", Effort: "medium", API: review.APIAnthropic, BaseURL: "http://proxy"}
	settings := opts{model: "claude-opus-5", effort: "medium"}.scoutSettings()
	got := answerOptions("/tree", res, ropts, settings, []review.Question{{ID: "c1", Kind: "precedent", Subject: "Insert"}})
	if got.Model != "claude-opus-5" || got.Effort != "medium" {
		t.Errorf("model/effort = %q/%q, want the review's", got.Model, got.Effort)
	}
	if got.BaseURL != "http://proxy" || got.API != review.APIAnthropic {
		t.Errorf("the lookups are not on the review's wire: %+v", got)
	}
	if len(got.Questions) != 1 || got.Questions[0].ID != "c1" {
		t.Errorf("questions = %+v, want the one asked", got.Questions)
	}
	if unset := answerOptions("/tree", res, review.Options{}, opts{}.scoutSettings(), nil); unset.Model != "" || unset.Effort != "" {
		t.Errorf("an unset flag reached the scout as %q/%q; it should stay empty for the scout's defaults", unset.Model, unset.Effort)
	}
}

// The answering scout resolves guideline files by walking up from each
// changed path, so an empty Changed leaves it reading the repository's
// root-level rules and nothing nearer. A question about a rule is then
// answered against the most general rules in the tree.
func TestTheLookupsAreToldWhichPathsChanged(t *testing.T) {
	res := &run.Result{Change: &change.Set{Files: []change.File{
		{Path: "internal/store/user.go"},
		{Path: "internal/queue/q.go"},
	}}}
	got := answerOptions("/tree", res, review.Options{}, scoutSettings{}, nil)
	if len(got.Changed) != 2 || got.Changed[0] != "internal/store/user.go" {
		t.Errorf("Changed = %v, want the change's paths so nested rules resolve", got.Changed)
	}
}

// --scout-model and --scout-effort win over --model and --effort for the
// checking pass, each on its own, so the review can run on one model and
// check its findings on a cheaper one.
func TestTheScoutFlagsWinForTheCheckingPass(t *testing.T) {
	both := opts{model: "claude-opus-5", effort: "high", scoutModel: "claude-haiku-4-5", scoutEffort: "low"}.scoutSettings()
	if both.Model != "claude-haiku-4-5" || both.Effort != "low" {
		t.Errorf("settings = %+v, want the scout flags", both)
	}
	one := opts{model: "claude-opus-5", effort: "high", scoutModel: "claude-haiku-4-5"}.scoutSettings()
	if one.Model != "claude-haiku-4-5" || one.Effort != "high" {
		t.Errorf("settings = %+v, want the scout model with the review's effort", one)
	}
}

// The trace is what `redline review` leaves behind for `redline postmortem`,
// and writeTrace is the one place the three stages are collected: the findings
// stage one proposed, the questions they named, and what the search did about
// them.
func TestTheTraceRecordsAllThreeStages(t *testing.T) {
	dir := worktreeSession(t)
	res, err := run.LoadSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	proposed := findings.Review{Comments: []findings.ReviewComment{
		{File: "a.go", Line: 1, Body: "the lock is never released",
			Question: findings.Question{Kind: findings.QuestionCaller, Subject: "Lock"}},
	}}
	out := &review.Result{
		API: "anthropic", Model: "claude-sonnet-5", Verified: true,
		Review:     proposed,
		Candidates: review.Candidates(proposed),
		Questions:  []review.Question{{ID: "c1", Kind: "caller", Subject: "Lock"}},
	}
	tally := &scoutTally{ran: true, known: true, turns: 2, model: "claude-sonnet-5", effort: "low"}
	tally.log.Calls = []scout.Call{{Turn: 1, Tool: "grep", Args: `{"pattern":"Lock"}`, Result: "0 bytes"}}
	tally.log.Notes = []string{"nothing in the tree calls Lock"}

	if err := writeTrace(opts{out: dir}, res, out, tally); err != nil {
		t.Fatal(err)
	}
	tr, err := postmortem.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Findings) != 1 || !tr.Findings[0].Asked {
		t.Fatalf("findings = %+v, want the proposed one and that it was looked up", tr.Findings)
	}
	if tr.Revision == "" {
		t.Error("the trace does not say which change it is about, so a later run cannot tell it is stale")
	}
	if len(tr.Lookup.Calls) != 1 || tr.Lookup.Turns != 2 {
		t.Errorf("the search was not recorded: %+v", tr.Lookup)
	}

	printed := captureStdout(t, func() {
		if err := cmdPostmortem(opts{out: dir, format: "report"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(printed, "the lock is never released") {
		t.Errorf("the command did not render the review it was given:\n%s", printed)
	}
	if !strings.Contains(printed, "nothing was filed against this question") {
		t.Errorf("the command did not say the lookups answered nothing:\n%s", printed)
	}
}

// Nothing to look back on is the normal state of a directory nobody has
// reviewed in, and the error says which command produces one.
func TestPostmortemWithoutAReviewSaysSo(t *testing.T) {
	err := cmdPostmortem(opts{out: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "redline review") {
		t.Fatalf("err = %v, want the command that would write a trace", err)
	}
}

// The whole pipeline, end to end over a stub endpoint: the reviewer proposes a
// finding, the scout answers the question it named out of a real tree, and the
// ruling decides with that answer in front of it. What the trace has to hold is
// the part no other file keeps, which is everything between the first call and
// the last.
func TestAReviewLeavesATraceOfAllThreeStages(t *testing.T) {
	tree := t.TempDir()
	const source = "package store\n\nfunc (u *User) Name() string { return u.name }\n"
	if err := os.WriteFile(filepath.Join(tree, "user.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := reportDir(t)
	res := &run.Result{
		Report: findings.Report{BaseRef: "origin/main", BaseSHA: "abc123"},
		Change: &change.Set{
			Target: &target.Target{Kind: target.KindWorktree, Dir: tree},
			Files:  []change.File{{Path: "user.go", Diff: "@@ -0,0 +1,3 @@\n+func (u *User) Name() string { return u.name }\n"}},
		},
	}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	// Read back the way `redline review` does, because that is what fills in
	// the target the lookups are run against.
	loaded, err := run.LoadSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	res = loaded

	var scoutTurn int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch forcedFunction(t, r) {
		case "review":
			writeOACall(w, "review", `{"overview":"o","files":[],"comments":[
				{"file":"user.go","line":3,"severity":"info","confidence":"high","category":"review",
				 "relatedFindings":[],"body":"a getter is not this repository's style",
				 "question":{"kind":"precedent","ask":"does this repository write getters elsewhere?","subject":"getters"}}],
				"verdicts":[]}`)
		case "rulings":
			writeOACall(w, "rulings", `{"rulings":[{"analysis":"the tree has one","finding":"c1",
				"verdict":"kept","evidence":"func (u *User) Name() string { return u.name }",
				"why":"the range the lookup filed is a getter"}]}`)
		default:
			// The scout's own loop: file the range, then finish.
			scoutTurn++
			if scoutTurn == 1 {
				writeOACall(w, "record", `{"role":"caller","file":"user.go","start_line":1,"end_line":3,
					"symbol":"Name","found_via":"grep","answers":"c1"}`)
				return
			}
			writeOACall(w, "done", `{"notes":["only the one getter in the tree"]}`)
		}
	}))
	defer srv.Close()

	ropts := review.Options{
		API: review.APIOpenAI, BaseURL: srv.URL, APIKey: "sk-test",
		Model: "gpt-5", Verify: true,
	}
	tally := &scoutTally{known: true}
	ropts.Answer = scoutAnswerer(res, ropts, scoutSettings{}, tally)
	out, err := review.Run(context.Background(), review.Input{Report: &res.Report, Change: res.Change}, ropts)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTrace(opts{out: dir}, res, out, tally); err != nil {
		t.Fatal(err)
	}

	tr, err := postmortem.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Findings) != 1 {
		t.Fatalf("findings = %+v, want the one the reviewer proposed", tr.Findings)
	}
	got := tr.Findings[0]
	if got.Body != "a getter is not this repository's style" || !got.Asked {
		t.Errorf("finding = %+v, want stage one's wording and that it was looked up", got)
	}
	if got.Ruling.Verdict != findings.VerifiedKept || !got.Ruling.Grounded {
		t.Errorf("ruling = %+v, want it kept on evidence found in what it was shown", got.Ruling)
	}
	if len(tr.Lookup.Calls) != 2 || tr.Lookup.Calls[0].Tool != "record" {
		t.Fatalf("calls = %+v, want the record and the done", tr.Lookup.Calls)
	}
	if len(tr.Lookup.Resolved) != 1 || tr.Lookup.Resolved[0].Answers != "c1" {
		t.Fatalf("resolved = %+v, want the range tied to the finding it answers", tr.Lookup.Resolved)
	}
	if !tr.Lookup.Ran || tr.Lookup.Turns != 2 {
		t.Errorf("lookup = %+v, want the two turns it took", tr.Lookup)
	}
	rendered := tr.Render()
	if got.EvidenceFrom != postmortem.FromLookup {
		t.Errorf("evidence came from %q, want the range the lookups filed", got.EvidenceFrom)
	}
	for _, want := range []string{"caller user.go:1-3", "kept and posted",
		"from a range the lookups filed", "only the one getter"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the postmortem does not say %q:\n%s", want, rendered)
		}
	}
}

// forcedFunction is which call this is. The review and the ruling each force
// one function by name; the scout offers its whole toolset and forces nothing.
func forcedFunction(t *testing.T, r *http.Request) string {
	t.Helper()
	var body struct {
		ToolChoice struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_choice"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("the stub endpoint was sent something it could not read: %v", err)
	}
	return body.ToolChoice.Function.Name
}

// writeOACall answers one chat completion with a single tool call, which is
// how all three stages return their structured output on this wire.
func writeOACall(w http.ResponseWriter, name, args string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
	})
}
