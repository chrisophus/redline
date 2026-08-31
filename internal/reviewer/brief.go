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

	"github.com/ccason/redline/internal/packet"
)

// BriefSchema is the JSON the context-gathering pass must write. It is the
// shape of packet.Brief, kept here as a prompt string so an adapter's
// {schema} placeholder expands to something a model can follow without
// importing the Go type into the prompt.
const BriefSchema = `{
  "repoShape": "One paragraph: what this repo is and how it is laid out.",
  "references": [
    {"symbol": "ExportedName", "definedIn": "path/to/def.go", "alsoIn": ["path/to/caller.go"]}
  ],
  "docsNaming": [
    {"path": "docs/or/skill.md", "line": 1, "quote": "short quote", "about": "the command or flag this names"}
  ],
  "testsCovering": [
    {"path": "path/to/test.go", "line": 1, "note": "what it covers, or the gap"}
  ],
  "filesRead": ["every path you actually opened"]
}`

// briefPrompt is the instruction for the context pass. It encodes what
// Copilot's context-search agent did on a real review: grep for the
// identifiers the diff introduces, read the docs that name the changed
// commands, find the tests that cover them, and list every file opened.
const briefPrompt = `Build a context brief for reviewing the change at {target}.

Changed files in this change:
{files}

Do this with read-only tools (Read, Grep, Glob). Do not run the build or the tests.

1. Read enough of the repository layout (README, top-level dirs, go.mod or
   package manifests) to write one paragraph as repoShape.
2. From the diffs of the changed files, collect the identifiers this change
   introduces or renames: exported funcs, types, methods, CLI commands, flags,
   JSON field names that other packages read. For each, Grep the rest of the
   tree and record definedIn and alsoIn. An empty alsoIn means you looked and
   found nothing elsewhere — say so by including the reference.
3. Grep docs/, skills/, README*, AGENTS.md, and *.md under .github/ for the
   command names and flags this change adds or alters. Record each hit as
   docsNaming with a short quote. A doc that still describes the old behaviour
   is exactly the kind of thing the reviewing agent must see.
4. Find tests that exercise the changed behaviour (or the dispatch table that
   should route to a new command and does not). Record them as testsCovering.
5. List every path you opened in filesRead. Paths you only saw in Grep output
   and never opened do not count.

Write the brief to {out} as JSON matching this schema:

{schema}

Write the file with your file-writing tool. Output nothing else.`

// briefAdapter is the built-in context-gathering pass. Cheap model, read-only
// tools: Copilot found its out-of-diff findings without a shell, and so does
// this. Bash is deliberately absent.
func briefAdapter() Adapter {
	return Adapter{
		Name: "brief",
		Command: []string{
			"claude", "-p", "{prompt}",
			"--model", "haiku",
			"--allowedTools", "Read Grep Glob Write",
		},
		Prompt:  briefPrompt,
		Timeout: Duration(5 * time.Minute),
	}
}

// argvBrief expands the adapter for a brief run: {schema} becomes BriefSchema
// and {files} becomes the changed-path list, one per line.
func (a Adapter) argvBrief(target, out, files string) []string {
	prompt := a.promptBrief(target, out, files)
	cmd := make([]string, len(a.Command))
	for i, part := range a.Command {
		cmd[i] = expandBrief(part, target, out, prompt, files)
	}
	return cmd
}

func (a Adapter) promptBrief(target, out, files string) string {
	t := a.Prompt
	if t == "" {
		t = briefPrompt
	}
	return expandBrief(t, target, out, "", files)
}

func expandBrief(s, target, out, prompt, files string) string {
	return strings.NewReplacer(
		"{prompt}", prompt,
		"{out}", out,
		"{target}", target,
		"{schema}", BriefSchema,
		"{files}", files,
	).Replace(s)
}

// RunBrief executes adapter a as a context-gathering pass and reads the brief
// it wrote. The output file is <outDir>/brief-<name>.json. files is the
// changed-path list, one path per line, substituted into {files}.
//
// A timeout, a missing output file, or JSON that does not parse are errors
// naming the adapter. An empty brief (no references, docs, tests, or files
// read) is still returned: the caller records that the pass ran and found
// nothing worth listing, which is different from the pass not running.
func RunBrief(ctx context.Context, a Adapter, repoDir, target, outDir, files string, opts ...RunOption) (*packet.Brief, error) {
	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(absOut, "brief-"+a.Name+".json")

	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale brief %s: %w", out, err)
	}

	cmd := a.argvBrief(target, out, files)

	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.Timeout))
		defer cancel()
	}

	execCmd := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	execCmd.Dir = repoDir
	execCmd.Stdin = nil
	w := &tailWriter{}
	execCmd.Stdout = w
	execCmd.Stderr = w
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
		n, last := w.progress()
		detail := fmt.Sprintf("it wrote %d byte(s)", n)
		if last != "" {
			detail += fmt.Sprintf("; last output was %q", trunc(last, 200))
		}
		return nil, fmt.Errorf("brief %s timed out after %v (%s)",
			a.Name, time.Duration(a.Timeout), detail)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		if os.IsNotExist(err) {
			why := "the brief wrote none"
			if execErr != nil {
				why = execErr.Error()
			}
			return nil, fmt.Errorf("brief %s produced no file at %s: %s\n%s",
				a.Name, out, why, tail(output, 500))
		}
		return nil, fmt.Errorf("read brief from %s: %w", out, err)
	}

	var brief packet.Brief
	if err := json.Unmarshal(data, &brief); err != nil {
		return nil, fmt.Errorf("parse brief from %s: %w", out, err)
	}
	return &brief, nil
}
