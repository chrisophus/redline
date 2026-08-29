package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
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

// A review that produced a report must not exit non-zero because the port
// was unavailable. The agent driving the session reads a non-zero exit as a
// failed review and may run the whole loop again.
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

// withStdin runs fn with os.Stdin replaced by text.
func withStdin(t *testing.T, text string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.WriteString(text)
		_ = w.Close()
	}()
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig; _ = r.Close() }()
	fn()
}

// session writes an evidence directory recording a review of the worktree.
func worktreeSession(t *testing.T) string {
	t.Helper()
	dir := reportDir(t)
	res := &run.Result{
		Report: findings.Report{BaseRef: "origin/main", BaseSHA: "abc123"},
		Packet: &packet.Packet{Target: &target.Target{Kind: target.KindWorktree}},
	}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Ingest must not merge a review into a session that reviewed something else:
// `review --pr 123` then `ingest --pr 456` used to succeed silently.
func TestIngestRejectsAMismatchedTarget(t *testing.T) {
	dir := worktreeSession(t)
	var err error
	withStdin(t, `{"summary":"x"}`, func() {
		err = cmdIngest(opts{out: dir, pr: "456", port: 40300, noOpen: true})
	})
	if err == nil {
		t.Fatal("ingesting --pr 456 into a worktree session should fail")
	}
	if !strings.Contains(err.Error(), "not pr 456") {
		t.Fatalf("error should name the mismatch, got: %v", err)
	}
}

// The same review with no target flag names the session it is merging into,
// so it must go through.
func TestIngestAcceptsTheSessionsOwnTarget(t *testing.T) {
	dir := worktreeSession(t)
	t.Cleanup(func() { _ = report.Stop(dir) })
	var err error
	withStdin(t, `{"summary":"what this change does"}`, func() {
		err = cmdIngest(opts{out: dir, port: 40400, noOpen: true})
	})
	if err != nil {
		t.Fatal(err)
	}
	html, readErr := os.ReadFile(filepath.Join(dir, "report.html"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(html), "what this change does") {
		t.Fatal("the ingested summary is not in the report")
	}
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
	case "run", "review", "ingest", "open", "serve":
		return true
	}
	return false
}
