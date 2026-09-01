package lint

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

// toolTimeout bounds one linter run. A linter that hangs must dark the pane
// with a message, not hang the review.
const toolTimeout = 5 * time.Minute

// tool is one linter Redline knows how to run and parse. Detection is by the
// tool's own config file: a repository that configured a linter has opted in,
// and its output format is the JSON the tool documents. The Makefile `lint`
// target was considered as the interface and rejected: its output has no
// structure to fingerprint, and a delta needs identity, not text.
type tool struct {
	name string
	// config is the config file that detected this tool, relative to the root.
	config string
}

// golangciConfigs and eslintConfigs are the filenames whose presence at the
// repository root opts the repo into each tool.
var golangciConfigs = []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}
var eslintConfigs = []string{
	".eslintrc", ".eslintrc.json", ".eslintrc.yaml", ".eslintrc.yml",
	".eslintrc.js", ".eslintrc.cjs",
	"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts",
}

// detect returns the tools the tree at root is configured for.
func detect(root string) []tool {
	var out []tool
	for _, name := range golangciConfigs {
		if fileExists(filepath.Join(root, name)) {
			out = append(out, tool{name: "golangci-lint", config: name})
			break
		}
	}
	for _, name := range eslintConfigs {
		if fileExists(filepath.Join(root, name)) {
			out = append(out, tool{name: "eslint", config: name})
			break
		}
	}
	return out
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// covers reports whether this tool has anything to say about a path.
func (t tool) covers(path string) bool {
	switch t.name {
	case "golangci-lint":
		return hasSuffixAny(path, ".go")
	case "eslint":
		return hasSuffixAny(path, ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".vue", ".svelte")
	}
	return false
}

// run executes the tool in dir and returns its issues, with file paths
// relative to dir. The tools exit non-zero when they find issues, so the exit
// code alone is not failure: failure is output that cannot be parsed, or an
// exit the tool documents as its own error.
func (t tool) run(dir string) ([]Issue, error) {
	switch t.name {
	case "golangci-lint":
		return runGolangci(dir)
	case "eslint":
		return runESLint(dir)
	}
	return nil, fmt.Errorf("unknown tool %q", t.name)
}

func runTool(dir string, name string, args ...string) (stdout string, stderr string, exit int, err error) {
	if _, lookErr := exec.LookPath(name); lookErr != nil {
		return "", "", 0, fmt.Errorf("%s is configured for this repository but not on PATH", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", 0, fmt.Errorf("%s did not finish within %s", name, toolTimeout)
	}
	exit = 0
	if ee, ok := runErr.(*exec.ExitError); ok {
		exit = ee.ExitCode()
		runErr = nil
	}
	if runErr != nil {
		return "", "", 0, fmt.Errorf("%s: %v", name, runErr)
	}
	return out.String(), errBuf.String(), exit, nil
}

// runGolangci runs golangci-lint with JSON output. Exit 0 is clean and exit 1
// is issues found; anything else is the tool failing. The output flag changed
// between major versions, so the v2 spelling is tried when v1's is refused.
func runGolangci(dir string) ([]Issue, error) {
	stdout, stderr, exit, err := runTool(dir, "golangci-lint", "run", "--out-format", "json", "./...")
	if err != nil {
		return nil, err
	}
	if exit > 1 && strings.Contains(stderr, "unknown flag") {
		stdout, stderr, exit, err = runTool(dir, "golangci-lint", "run", "--output.json.path", "stdout", "./...")
		if err != nil {
			return nil, err
		}
	}
	if exit > 1 {
		return nil, fmt.Errorf("golangci-lint exited %d: %s", exit, firstLine(stderr))
	}
	var parsed struct {
		Issues []struct {
			FromLinter string `json:"FromLinter"`
			Text       string `json:"Text"`
			Pos        struct {
				Filename string `json:"Filename"`
				Line     int    `json:"Line"`
			} `json:"Pos"`
		} `json:"Issues"`
	}
	if jsonErr := json.Unmarshal([]byte(stdout), &parsed); jsonErr != nil {
		return nil, fmt.Errorf("golangci-lint output was not its JSON format: %v: %s", jsonErr, firstLine(stdout))
	}
	out := make([]Issue, 0, len(parsed.Issues))
	for _, i := range parsed.Issues {
		out = append(out, Issue{
			Tool: "golangci-lint", File: filepath.ToSlash(i.Pos.Filename), Line: i.Pos.Line,
			Rule: i.FromLinter, Message: i.Text, Severity: "warning",
		})
	}
	return out, nil
}

// runESLint prefers the repository's own eslint (node_modules/.bin) over a
// global one, because rule sets are dependencies. Exit 0 is clean, 1 is
// problems found, 2 is eslint failing.
func runESLint(dir string) ([]Issue, error) {
	name := "eslint"
	if local := filepath.Join(dir, "node_modules", ".bin", "eslint"); fileExists(local) {
		name = local
	}
	stdout, stderr, exit, err := runTool(dir, name, "--format", "json", ".")
	if err != nil {
		return nil, err
	}
	if exit > 1 {
		return nil, fmt.Errorf("eslint exited %d: %s", exit, firstLine(stderr))
	}
	var parsed []struct {
		FilePath string `json:"filePath"`
		Messages []struct {
			RuleID   string `json:"ruleId"`
			Severity int    `json:"severity"`
			Message  string `json:"message"`
			Line     int    `json:"line"`
		} `json:"messages"`
	}
	if jsonErr := json.Unmarshal([]byte(stdout), &parsed); jsonErr != nil {
		return nil, fmt.Errorf("eslint output was not its JSON format: %v: %s", jsonErr, firstLine(stdout))
	}
	var out []Issue
	for _, f := range parsed {
		path := f.FilePath
		if rel, relErr := filepath.Rel(dir, path); relErr == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
		for _, m := range f.Messages {
			rule := m.RuleID
			if rule == "" {
				rule = "fatal"
			}
			sev := "info"
			if m.Severity >= 2 {
				sev = "warning"
			}
			out = append(out, Issue{
				Tool: "eslint", File: filepath.ToSlash(path), Line: m.Line,
				Rule: rule, Message: m.Message, Severity: sev,
			})
		}
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
