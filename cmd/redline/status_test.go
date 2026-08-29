package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ccason/redline/internal/reviewer"
)

// In a pipe or a log, progress must be plain lines. Carriage-return redraws are
// worse than nothing there: CI output becomes one unreadable line.
func newTestStatus() (*status, *bytes.Buffer) {
	var buf bytes.Buffer
	return &status{w: &buf, tty: false, interval: 30 * time.Second}, &buf
}

func TestStatusAnnouncesStartThenProgressThenResult(t *testing.T) {
	s, buf := newTestStatus()

	s.update(reviewer.Update{Name: "claude"})
	s.update(reviewer.Update{Name: "claude", Elapsed: 45 * time.Second, Bytes: 2048, Last: "reading internal/run/run.go"})
	s.done("claude", 92*time.Second, 6)

	out := buf.String()
	for _, want := range []string{
		"claude is reviewing",         // something appears at once
		"45s elapsed",                 // it is alive
		"2 KB out",                    // and doing something
		"reading internal/run/run.go", // specifically this
		"finished in 1m32s",           // and here is the outcome
		"6 findings",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q from:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\r") {
		t.Error("no carriage returns when the destination is not a terminal")
	}
}

// Silence is the state the reader is trying to interpret, so it is named rather
// than rendered as a zero.
func TestStatusNamesSilenceRatherThanShowingZero(t *testing.T) {
	s, buf := newTestStatus()
	s.update(reviewer.Update{Name: "cursor", Elapsed: 3 * time.Minute})

	out := buf.String()
	if !strings.Contains(out, "no output yet") {
		t.Errorf("silence should be stated plainly, got:\n%s", out)
	}
	if strings.Contains(out, "0 B") {
		t.Errorf("silence rendered as a byte count:\n%s", out)
	}
	if !strings.Contains(out, "3m00s elapsed") {
		t.Errorf("elapsed time is what makes silence interpretable, got:\n%s", out)
	}
}

func TestStatusReportsAFailedReviewerWithItsElapsedTimeAndReason(t *testing.T) {
	s, buf := newTestStatus()
	s.fail("claude", 10*time.Minute, errors.New("no findings file"))

	out := buf.String()
	if !strings.Contains(out, "failed after 10m00s") {
		t.Errorf("the wait itself is worth reporting, got %q", out)
	}
	if !strings.Contains(out, "no findings file") {
		t.Errorf("the reason must survive, got %q", out)
	}
	if lines := strings.Count(out, "\n"); lines != 1 {
		t.Errorf("one failure, one line; got %d:\n%s", lines, out)
	}
}

// os/exec's wording for this is accurate about its own internals and useless
// about what happened.
func TestWaitDelayErrorIsTranslated(t *testing.T) {
	err := errors.New(`reviewer claude produced no findings file at /x/y.json: exec: WaitDelay expired before I/O complete`)
	got := explain(err)

	if strings.Contains(got, "WaitDelay") {
		t.Errorf("still leaking os/exec internals: %q", got)
	}
	if !strings.Contains(got, "held its output open") {
		t.Errorf("should say what actually happened, got %q", got)
	}
	// The useful part of the original is kept.
	if !strings.Contains(got, "no findings file") {
		t.Errorf("dropped the context, got %q", got)
	}
	// Anything else passes through untouched.
	plain := errors.New("command not found")
	if explain(plain) != "command not found" {
		t.Errorf("unrelated errors must pass through, got %q", explain(plain))
	}
}

// On a terminal the tick rewrites one line. Anything printed afterwards has to
// clear it first, or a half-overwritten tick ends up spliced into a real message.
func TestTerminalProgressIsClearedBeforeOtherOutput(t *testing.T) {
	var buf bytes.Buffer
	s := &status{w: &buf, tty: true, interval: time.Second}

	s.update(reviewer.Update{Name: "claude", Elapsed: time.Second, Bytes: 10})
	if !s.dirty {
		t.Fatal("an in-place line should be marked as needing a clear")
	}
	s.done("claude", 2*time.Second, 1)

	out := buf.String()
	if strings.Count(out, "\033[K") < 2 {
		t.Errorf("expected the tick to be written then cleared, got %q", out)
	}
	if s.dirty {
		t.Error("dirty should be reset once cleared")
	}
	if !strings.HasSuffix(out, "1 finding\n") {
		t.Errorf("the result should end the line properly, got %q", out)
	}
}

func TestDurationsReadTheWayAPersonWaitingReadsThem(t *testing.T) {
	cases := map[time.Duration]string{
		0:                         "0s",
		999 * time.Millisecond:    "1s",
		45 * time.Second:          "45s",
		60 * time.Second:          "1m00s",
		92 * time.Second:          "1m32s",
		10 * time.Minute:          "10m00s",
		time.Hour + 5*time.Second: "60m05s",
	}
	for d, want := range cases {
		if got := short(d); got != want {
			t.Errorf("short(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestOutputSizesAreHumanReadable(t *testing.T) {
	cases := map[int]string{
		0:       "no output yet",
		1:       "1 B out",
		1023:    "1023 B out",
		2048:    "2 KB out",
		5242880: "5.0 MB out",
	}
	for n, want := range cases {
		if got := describeOutput(n); got != want {
			t.Errorf("describeOutput(%d) = %q, want %q", n, got, want)
		}
	}
}

// A reviewer's output line can be long or carry a newline; the progress line
// must stay one line.
func TestProgressLineStaysOneLine(t *testing.T) {
	long := strings.Repeat("x", 200)
	// Runes, not bytes: the ellipsis is three bytes and one column.
	if got := firstLine(long, 60); utf8.RuneCountInString(got) > 61 {
		t.Errorf("line not truncated: %d runes", utf8.RuneCountInString(got))
	}
	if got := firstLine("first\nsecond", 60); got != "first" {
		t.Errorf("got %q, want the first line only", got)
	}
	if got := firstLine("  padded  ", 60); got != "padded" {
		t.Errorf("got %q", got)
	}
}
