package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/change"
	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/post"
	"github.com/ccason/redline/internal/report"
	"github.com/ccason/redline/internal/run"
	"github.com/ccason/redline/internal/target"
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

// fakeGh puts a stand-in `gh` on PATH that answers the three reads post makes
// (files, comments, reviews) and refuses a write. commentBodies and reviewBodies
// are what `-q .[].body` would print: one body per line.
func fakeGh(t *testing.T, commentBodies, reviewBodies []string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	// The stand-in is a shell script so the test does not need to compile a
	// second Go binary. Arguments arrive as "$@"; the path is the last one.
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("set -e\n")
	b.WriteString("# Refuse a write: already-posted must never reach submitReview.\n")
	b.WriteString("for a in \"$@\"; do\n")
	b.WriteString("  case \"$a\" in POST|--method) echo 'fake gh: unexpected write' >&2; exit 2;; esac\n")
	b.WriteString("done\n")
	b.WriteString("path=\"\"\n")
	b.WriteString("for a in \"$@\"; do path=\"$a\"; done\n")
	b.WriteString("case \"$path\" in\n")
	b.WriteString("  *'/files'*)\n")
	// An empty page: every finding rides in the body. That is enough for the
	// already-posted gate, which keys on markers rather than on anchors.
	b.WriteString("    printf '%s\\n' '[[]]'\n")
	b.WriteString("    ;;\n")
	b.WriteString("  *'/comments'*)\n")
	for _, body := range commentBodies {
		fmt.Fprintf(&b, "    printf '%%s\\n' %q\n", body)
	}
	b.WriteString("    ;;\n")
	b.WriteString("  *'/reviews'*)\n")
	for _, body := range reviewBodies {
		fmt.Fprintf(&b, "    printf '%%s\\n' %q\n", body)
	}
	b.WriteString("    ;;\n")
	b.WriteString("  *) echo \"fake gh: unexpected path $path\" >&2; exit 3;;\n")
	b.WriteString("esac\n")
	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
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
	fakeGh(t, []string{payload.Comments[0].Body}, []string{payload.Body})

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
