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
func Run(ctx context.Context, a Adapter, repoDir, target, outDir string) ([]Finding, error) {
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

	// Execute the reviewer. Capture stderr for error reporting.
	execCmd := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	execCmd.Dir = repoDir
	execCmd.Stdin = nil
	output, execErr := execCmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("reviewer %s timed out after %v", a.Name, time.Duration(a.Timeout))
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
