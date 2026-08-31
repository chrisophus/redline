// Package reviewer adapts external code review tools to Redline's review engine.
// A reviewer is a command-line tool that inspects a target, writes findings to a
// JSON file, and exits. Adapters are configuration, not code — a new tool is a
// config entry that names the command and timeout, with placeholders for file paths
// and other data that Redline substitutes at run time.
package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Duration is a time.Duration that reads and writes as "10m" in JSON.
// reviewers.json is hand-written, and Go's default duration encoding is an
// integer count of nanoseconds — a number nobody types correctly and nobody
// reads back.
type Duration time.Duration

// UnmarshalJSON accepts "90s" or a bare number of nanoseconds, so a file
// written by hand and one round-tripped through MarshalJSON both load.
func (d *Duration) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("timeout %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("timeout must be a duration string like \"10m\": %w", err)
	}
	*d = Duration(n)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Adapter configures an external reviewer tool.
type Adapter struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`

	// Prompt is the instruction handed to the tool as {prompt}. It belongs to
	// the adapter and not to this package because the point of a reviewer
	// adapter is to invoke the tool's own review — Claude Code's /code-review,
	// and whatever each later tool calls its equivalent — rather than a prompt
	// Redline wrote. A Redline-authored prompt would make every reviewer a
	// generic model call and throw away the review logic the vendor ships.
	// Placeholders: {target}, {out}, {schema}.
	Prompt string `json:"prompt"`

	Timeout Duration `json:"timeout"`
}

// Builtins returns the default adapters. Entries are identified by name and can
// be overridden by Load; built-ins exist if reviewers.json does not name them.
func Builtins() map[string]Adapter {
	return map[string]Adapter{
		"claude": {
			Name:    "claude",
			Command: []string{"claude", "-p", "{prompt}", "--output-format", "json", "--allowedTools", "Bash Read Grep Glob Write"},
			Prompt:  "/code-review {target}\n\n" + contract,
			Timeout: Duration(10 * time.Minute),
		},
		// cursor-agent exposes no review command of its own, so this asks for a
		// review in prose. When Cursor ships a headless Bugbot entry point this
		// adapter should call it instead — that is the whole reason Prompt is
		// per-adapter configuration.
		"cursor": {
			Name:    "cursor",
			Command: []string{"cursor-agent", "-p", "{prompt}", "--output-format", "text", "-f"},
			Prompt:  "Review the change at {target} for correctness bugs, security issues and contract breaks.\n\n" + contract,
			Timeout: Duration(10 * time.Minute),
		},
		// brief is the context-gathering pass, not a findings reviewer. It is
		// looked up by --brief, not --with; listing it here lets reviewers.json
		// override the command or model the same way the others do.
		"brief": briefAdapter(),
	}
}

// Load merges Builtins() with adapters from <dir>/reviewers.json if that file
// exists. File entries override built-ins by name. Missing file is not an error;
// malformed JSON is an error and names the file.
func Load(dir string) (map[string]Adapter, error) {
	adapters := Builtins()

	path := filepath.Join(dir, "reviewers.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return adapters, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var file struct {
		Reviewers map[string]Adapter `json:"reviewers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	for name, a := range file.Reviewers {
		a.Name = name
		adapters[name] = a
	}

	return adapters, nil
}

// Schema describes the JSON output contract for reviewers: a findings array with
// fields for location, severity, title, body, and confidence.
const Schema = `{
  "findings": [
    {
      "file": "string",
      "line": "int",
      "severity": "error|warning|info",
      "title": "string",
      "body": "string",
      "confidence": "high|medium|low"
    }
  ]
}`

// contract is appended to every adapter's prompt. Reviewers are told to write a
// file rather than print findings because stdout carries the tool's own
// conversational output, which no adapter can be expected to suppress.
const contract = `When the review is complete, write its findings to {out} as JSON matching this schema:

{schema}

Write the file with your file-writing tool. Output nothing else.`

// prompt expands the adapter's template for one run. An adapter with no Prompt
// gets the generic contract alone, which is enough for a tool whose command is
// already a review command.
func (a Adapter) prompt(target, out string) string {
	t := a.Prompt
	if t == "" {
		t = contract
	}
	return expand(t, target, out, "")
}

// argv expands the adapter's command line for one run. Run and its tests share
// it so that what the tests check is what Run executes.
func (a Adapter) argv(target, out string) []string {
	cmd := make([]string, len(a.Command))
	for i, part := range a.Command {
		cmd[i] = expand(part, target, out, a.prompt(target, out))
	}
	return cmd
}

func expand(s, target, out, prompt string) string {
	return strings.NewReplacer(
		"{prompt}", prompt,
		"{out}", out,
		"{target}", target,
		"{schema}", Schema,
	).Replace(s)
}

// Finding is a neutral parse target for reviewer output. Fields match the output
// schema.
type Finding struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Confidence string `json:"confidence"`
}

// tail returns the last n bytes of b, which is where a CLI puts the reason it
// gave up.
func tail(b []byte, n int) string {
	if len(b) > n {
		return string(b[len(b)-n:])
	}
	return string(b)
}

// tailRetained is how much of a reviewer's output is kept. Only the tail is
// ever reported, and a chatty reviewer should not be able to grow this process
// without bound.
const tailRetained = 8192

// waitDelay is how long a killed reviewer's process tree has to exit before its
// output pipes are closed regardless. Long enough that a well-behaved reviewer
// finishes flushing, short enough that a badly-behaved one does not become
// Redline's problem.
const waitDelay = 2 * time.Second

// tailWriter counts everything a reviewer writes, keeps the tail for error
// reporting, and remembers the last non-empty line so a progress line can show
// what the reviewer is doing. Written by the process, read by the ticker, hence
// the mutex.
type tailWriter struct {
	mu    sync.Mutex
	buf   []byte
	n     int
	last  string
	carry string
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n += len(p)

	w.buf = append(w.buf, p...)
	if len(w.buf) > tailRetained {
		w.buf = w.buf[len(w.buf)-tailRetained:]
	}

	// Track the last complete line. Partial writes are held in carry until
	// their newline arrives, so a line split across two writes is not reported
	// twice as two half lines.
	w.carry += string(p)
	if i := strings.LastIndexByte(w.carry, '\n'); i >= 0 {
		for _, line := range strings.Split(w.carry[:i], "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				w.last = trimmed
			}
		}
		w.carry = w.carry[i+1:]
	}
	return len(p), nil
}

func (w *tailWriter) progress() (int, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n, w.last
}

func (w *tailWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf...)
}

// watch reports progress until the returned function is called. It always
// reports once at zero elapsed, so something appears on screen the moment the
// reviewer starts rather than one interval later.
func watch(cfg runConfig, name string, started time.Time, w *tailWriter) func() {
	if cfg.progress == nil {
		return func() {}
	}
	cfg.progress(Update{Name: name})
	if cfg.interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(cfg.interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				n, last := w.progress()
				cfg.progress(Update{
					Name:    name,
					Elapsed: time.Since(started),
					Bytes:   n,
					Last:    last,
				})
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// Update is what a reviewer looks like from outside while it runs.
//
// A reviewer can take ten minutes and most of them print nothing until they are
// finished, so "no output yet" is the normal case rather than a symptom. What
// distinguishes working from stuck is elapsed time against how much the process
// has emitted, which is what this carries.
type Update struct {
	Name    string
	Elapsed time.Duration
	// Bytes is how much the reviewer has written so far. Zero after a long
	// while is the signal that something is wrong.
	Bytes int
	// Last is the most recent non-empty line it wrote, trimmed. Empty when it
	// has written nothing, or nothing with a newline in it.
	Last string
}

// RunOption configures Run. Options rather than parameters so that adding
// progress reporting did not have to touch every call site.
type RunOption func(*runConfig)

type runConfig struct {
	progress func(Update)
	interval time.Duration
}

// WithProgress calls fn once when the reviewer starts, then every interval
// until it exits. A zero or negative interval reports only the start.
func WithProgress(interval time.Duration, fn func(Update)) RunOption {
	return func(c *runConfig) {
		c.progress = fn
		c.interval = interval
	}
}

// Run executes adapter a and reads its findings. The output file path is
// <outDir>/reviewer-<name>.json. The stale-file removal ensures a reviewer
// that fails silently cannot pass off a previous run's findings as this one's.
// Placeholders in Command and Prompt are substituted: {prompt}, {out},
// {target}, {schema}.
// A timeout returns an error naming the reviewer and the timeout.
// A non-zero exit with a valid output file is NOT an error (reviewers exit
// non-zero for findings-found); a non-zero exit with no output file IS.
// Findings with empty Title are dropped; Severity and Confidence are normalized
// to lowercase, defaulting invalid values to "warning" and "medium".
func Run(ctx context.Context, a Adapter, repoDir, target, outDir string, opts ...RunOption) ([]Finding, error) {
	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	// The reviewer runs in the tree under review, which is not this process's
	// working directory. A relative --out would make it write its findings
	// beside the code it is reviewing, where nothing looks for them.
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(absOut, "reviewer-"+a.Name+".json")

	// Remove any stale output file. If a reviewer fails silently, it cannot
	// pass off a previous run's findings as this one's.
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale output %s: %w", out, err)
	}

	cmd := a.argv(target, out)

	// Apply timeout via context.
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.Timeout))
		defer cancel()
	}

	// Execute the reviewer, watching its output as it goes. CombinedOutput
	// would be shorter, but it returns nothing until the process exits, and a
	// ten-minute wait with an empty terminal is indistinguishable from a hang.
	execCmd := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	execCmd.Dir = repoDir
	execCmd.Stdin = nil
	w := &tailWriter{}
	execCmd.Stdout = w
	execCmd.Stderr = w
	// Without this, the timeout is not a timeout. Killing the reviewer does not
	// kill anything it spawned, and Wait blocks until every holder of the output
	// pipe closes it — so a reviewer whose helper outlives it hangs Redline
	// indefinitely, well past the deadline that was supposed to prevent exactly
	// that. WaitDelay gives the tree a moment to exit, then closes the pipes and
	// returns.
	execCmd.WaitDelay = waitDelay

	started := time.Now()
	execErr := execCmd.Start()
	if execErr == nil {
		stop := watch(cfg, a.Name, started, w)
		execErr = execCmd.Wait()
		stop()
	}
	output := w.bytes()

	if ctx.Err() == context.DeadlineExceeded {
		// Say what it managed to do before the deadline. "Timed out after 10m"
		// alone does not distinguish a reviewer that was working from one that
		// never started.
		n, last := w.progress()
		detail := fmt.Sprintf("it wrote %d byte(s)", n)
		if last != "" {
			detail += fmt.Sprintf("; last output was %q", trunc(last, 200))
		}
		return nil, fmt.Errorf("reviewer %s timed out after %v (%s)",
			a.Name, time.Duration(a.Timeout), detail)
	}

	// Read and parse the output file.
	data, err := os.ReadFile(out)
	if err != nil {
		if os.IsNotExist(err) {
			// No findings file. The exit error is the diagnosis in the most
			// common case by far — the tool is not installed — so lead with it
			// rather than with the output, which is empty when exec fails.
			why := "the reviewer wrote none"
			if execErr != nil {
				why = execErr.Error()
			}
			return nil, fmt.Errorf("reviewer %s produced no findings file at %s: %s\n%s",
				a.Name, out, why, tail(output, 500))
		}
		return nil, fmt.Errorf("read findings from %s: %w", out, err)
	}

	var result struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse findings from %s: %w", out, err)
	}

	// Normalize and filter findings.
	var findings []Finding
	for _, f := range result.Findings {
		if f.Title == "" {
			continue
		}
		f.Severity = strings.ToLower(f.Severity)
		switch f.Severity {
		case "error", "warning", "info":
		default:
			f.Severity = "warning"
		}
		f.Confidence = strings.ToLower(f.Confidence)
		switch f.Confidence {
		case "high", "medium", "low":
		default:
			f.Confidence = "medium"
		}
		findings = append(findings, f)
	}

	return findings, nil
}
