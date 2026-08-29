package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ccason/redline/internal/reviewer"
)

// status prints what a reviewer is doing while it runs.
//
// A reviewer is an external process that can take ten minutes and typically
// prints nothing until it is finished. Waiting on a silent terminal, the only
// honest reading is "I do not know whether this is working", and the usual
// response is to kill it — which is the wrong answer most of the time.
//
// So: say what started, tick while it runs, and say what it produced. The tick
// carries elapsed time and how much the reviewer has written, because that pair
// is what actually separates slow from stuck.
type status struct {
	w io.Writer
	// tty is whether the destination is a terminal. On a terminal the tick
	// rewrites one line in place; in a pipe or a log it prints a line at a much
	// lower rate, since \r spam in CI output is worse than no output at all.
	tty      bool
	interval time.Duration
	// dirty is whether an in-place line is currently on screen and needs
	// clearing before anything else is written.
	dirty bool
}

func newStatus(w *os.File) *status {
	s := &status{w: w, tty: isTerminal(w), interval: 30 * time.Second}
	if s.tty {
		s.interval = time.Second
	}
	return s
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// update is the reviewer.Progress callback. Elapsed zero is the start.
func (s *status) update(u reviewer.Update) {
	if u.Elapsed == 0 {
		s.clear()
		fmt.Fprintf(s.w, "redline: %s is reviewing…\n", u.Name)
		return
	}
	line := fmt.Sprintf("redline: %s reviewing — %s elapsed, %s",
		u.Name, short(u.Elapsed), describeOutput(u.Bytes))
	if u.Last != "" {
		line += " · " + firstLine(u.Last, 60)
	}
	if s.tty {
		// \r and no newline: the line is replaced next tick rather than
		// scrolling a minute of near-identical lines past the reviewer.
		fmt.Fprintf(s.w, "\r\033[K%s", line)
		s.dirty = true
		return
	}
	fmt.Fprintln(s.w, line)
}

func (s *status) done(name string, elapsed time.Duration, found int) {
	s.clear()
	fmt.Fprintf(s.w, "redline: %s finished in %s — %d finding%s\n",
		name, short(elapsed), found, plural(found))
}

func (s *status) fail(name string, elapsed time.Duration, err error) {
	s.clear()
	fmt.Fprintf(s.w, "redline: %s failed after %s: %s\n", name, short(elapsed), explain(err))
}

// explain replaces the one Go runtime error a reviewer can produce that means
// nothing to the person reading it. "WaitDelay expired before I/O complete" is
// an accurate description of an os/exec internal and a useless description of
// what went wrong, which was: the reviewer left something running that held its
// output open, so Redline stopped waiting.
func explain(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "WaitDelay expired") {
		return strings.ReplaceAll(msg, "exec: WaitDelay expired before I/O complete",
			"it left a process running that held its output open, so Redline stopped waiting for it")
	}
	return msg
}

// clear removes an in-place progress line before other output is written, so a
// half-overwritten tick never ends up interleaved with a real message.
func (s *status) clear() {
	if s.dirty {
		fmt.Fprint(s.w, "\r\033[K")
		s.dirty = false
	}
}

// describeOutput says how much the reviewer has emitted. Silence is stated
// plainly rather than as "0 B", because silence is the thing the reader is
// trying to interpret.
func describeOutput(n int) string {
	switch {
	case n == 0:
		return "no output yet"
	case n < 1024:
		return fmt.Sprintf("%d B out", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KB out", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB out", float64(n)/(1024*1024))
	}
}

// short renders a duration the way a person waiting on it would read it.
func short(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func firstLine(s string, max int) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
