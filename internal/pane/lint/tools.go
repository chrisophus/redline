package lint

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// config is the config file that detected a built-in tool, relative to
	// the root; empty for a tool declared in .redline.yml.
	config string
	// custom is non-nil for a tool declared in .redline.yml — the built-in
	// name/config/covers/run paths are bypassed for it.
	custom *ToolConfig
}

// golangciConfigs, eslintConfigs, and gorefactorConfigs are the filenames
// whose presence at the repository root opts the repo into each tool.
//
// gorefactor (github.com/chrisophus/gorefactor) is a separate project's
// structural linter, not a Redline dependency — the same optional-tool
// shape as golangci-lint and eslint: a repository that carries its config
// opted in, and a missing binary degrades the pane rather than erasing it.
var golangciConfigs = []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}
var eslintConfigs = []string{
	".eslintrc", ".eslintrc.json", ".eslintrc.yaml", ".eslintrc.yml",
	".eslintrc.js", ".eslintrc.cjs",
	"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts",
}
var gorefactorConfigs = []string{".gorefactor.yaml", ".gorefactor.yml"}

// detect returns the tools the tree at root is configured for: the built-in
// three by config-file presence, plus every tool declared in .redline.yml.
// A .redline.yml that exists but does not parse is an error — a misconfigured
// tool must dark the pane, not vanish from it.
func detect(root string) ([]tool, error) {
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
	for _, name := range gorefactorConfigs {
		if fileExists(filepath.Join(root, name)) {
			out = append(out, tool{name: "gorefactor", config: name})
			break
		}
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		for i := range cfg.Tools {
			tc := cfg.Tools[i]
			out = append(out, tool{name: tc.Name, custom: &tc})
		}
	}
	return out, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// covers reports whether this tool has anything to say about a path.
func (t tool) covers(path string) bool {
	if t.custom != nil {
		return matchesAnyGlob(t.custom.Scope, path)
	}
	switch t.name {
	case "golangci-lint", "gorefactor":
		return hasSuffixAny(path, ".go")
	case "eslint":
		return hasSuffixAny(path, ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".vue", ".svelte")
	}
	return false
}

// run executes a linter-kind tool in dir and returns its issues. Differ-kind
// tools do not run here — they need both revisions at once and are run from
// Diff instead.
func (t tool) run(dir string) ([]Issue, error) {
	if t.custom != nil {
		return runConfiguredLinter(dir, *t.custom)
	}
	switch t.name {
	case "golangci-lint":
		return runGolangci(dir)
	case "eslint":
		return runESLint(dir)
	case "gorefactor":
		return runGorefactor(dir)
	}
	return nil, fmt.Errorf("unknown tool %q", t.name)
}

// isDiffer reports whether a configured tool computes its own base-vs-head
// delta (oasdiff), rather than linting one snapshot.
func (t tool) isDiffer() bool {
	return t.custom != nil && t.custom.Kind == "differ"
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
	// golangci-lint v2 writes its JSON payload to stdout and then, with no
	// flag combination that turns it off, an unconditional "N issues."
	// summary line after it — a Decoder reads the one JSON value at the
	// start and ignores what follows, where Unmarshal would reject the
	// whole stdout as malformed.
	if jsonErr := json.NewDecoder(strings.NewReader(stdout)).Decode(&parsed); jsonErr != nil {
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

// runGorefactor runs gorefactor's own structural lint (a separate project,
// github.com/chrisophus/gorefactor — not a Redline dependency) with JSON
// output. Exit 0 is clean, exit 1 is an issue at or above its fail-on
// threshold; either way stdout is the JSON to parse. Anything else is the
// tool failing.
func runGorefactor(dir string) ([]Issue, error) {
	stdout, stderr, exit, err := runTool(dir, "gorefactor", "lint", ".", "--json")
	if err != nil {
		return nil, err
	}
	if exit > 1 {
		return nil, fmt.Errorf("gorefactor exited %d: %s", exit, firstLine(stderr))
	}
	var parsed struct {
		Issues []struct {
			File     string `json:"file"`
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"issues"`
	}
	if jsonErr := json.NewDecoder(strings.NewReader(stdout)).Decode(&parsed); jsonErr != nil {
		return nil, fmt.Errorf("gorefactor output was not its JSON format: %v: %s", jsonErr, firstLine(stdout))
	}
	mod := moduleImportPath(dir)
	out := make([]Issue, 0, len(parsed.Issues))
	for _, i := range parsed.Issues {
		file := i.File
		// Most rules name a repo-relative path; a coverage-derived rule like
		// untested-function names the module-qualified one instead
		// ("github.com/x/y/internal/a/b.go:12"). Strip it so this issue's
		// File matches the repo-relative paths every other pane and the
		// walkthrough use — otherwise it can never be attributed to a
		// changed file or anchored to a PR line.
		if mod != "" {
			if rest, ok := strings.CutPrefix(file, mod+"/"); ok {
				file = rest
			}
		}
		path, line := splitGorefactorLocation(file)
		out = append(out, Issue{
			Tool: "gorefactor", File: filepath.ToSlash(path), Line: line,
			Rule: i.Rule, Message: i.Message, Severity: i.Severity,
		})
	}
	return out, nil
}

// moduleImportPath reads the module directive from dir's go.mod, or "" if
// there is none to read. Failure degrades to leaving gorefactor's paths
// exactly as it wrote them, rather than blocking the whole tool run.
func moduleImportPath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// splitGorefactorLocation splits gorefactor's "file" field: a bare path for a
// whole-file or whole-function finding (file-size, complexity, dead-code),
// "path:line:col" or "path:line" for one anchored to a location. The column,
// when present, is discarded — Redline findings carry no column.
func splitGorefactorLocation(s string) (string, int) {
	parts := strings.Split(s, ":")
	if len(parts) >= 3 {
		if _, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			if n, err2 := strconv.Atoi(parts[len(parts)-2]); err2 == nil {
				return strings.Join(parts[:len(parts)-2], ":"), n
			}
		}
	}
	if len(parts) >= 2 {
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return strings.Join(parts[:len(parts)-1], ":"), n
		}
	}
	return s, 0
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
