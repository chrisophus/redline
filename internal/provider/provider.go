// Package provider runs the context providers that resolve a change in a
// language Redline does not itself understand.
//
// Discovery mirrors the lint pane: a provider is found by the config file
// that says the repository opted into its tool, plus anything declared in
// .redline.yml. Redline holds no list of provider names in a code path, so a
// second language needs a config entry rather than a code change.
//
// A provider is a separate program. Redline runs it, reads one JSON envelope
// from its stdout, and links none of its code. See docs/context-envelope.md
// for the format.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/globmatch"
	"gopkg.in/yaml.v3"
)

// runTimeout bounds one provider. A provider that hangs must fail the
// context layer with a message rather than hang the review.
const runTimeout = 5 * time.Minute

// baseToken is replaced in a provider's arguments with the merge-base SHA.
const baseToken = "{{base}}"

// Config is one entry under `context:` in .redline.yml.
type Config struct {
	// Name identifies the provider on the report and in error messages.
	Name string `yaml:"name"`
	// Command and Args are the executable and its arguments. Any argument
	// equal to exactly "{{base}}" is replaced with the merge-base SHA.
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	// Scope is the globs this provider speaks for. A changed file outside
	// every provider's scope is reported as uncovered by the context layer,
	// the same honesty the lint pane gives a file no tool reads.
	Scope []string `yaml:"scope"`
	// OKExitCodes are exit codes that mean the provider ran. Empty defaults
	// to {0}: unlike a linter, a provider has no "found issues" exit code.
	OKExitCodes []int `yaml:"okExitCodes"`
}

type fileConfig struct {
	Context []Config `yaml:"context"`
}

var configNames = []string{".redline.yml", ".redline.yaml"}

// builtin are the providers Redline detects with no configuration, keyed by
// the config file whose presence at the repository root means the repository
// already runs that tool. Same shape as the lint pane's built-in three: a
// repository that carries the config opted in, and a missing binary degrades
// the context layer rather than erasing it.
//
// This table is data. Nothing downstream branches on a provider's name.
var builtin = []struct {
	config string
	cfg    Config
}{
	{
		config: ".gorefactor.yaml",
		cfg: Config{
			Name:    "gorefactor",
			Command: "gorefactor",
			Args:    []string{"context", "--changed", baseToken, "--json"},
			Scope:   []string{"**/*.go"},
		},
	},
	{
		config: ".gorefactor.yml",
		cfg: Config{
			Name:    "gorefactor",
			Command: "gorefactor",
			Args:    []string{"context", "--changed", baseToken, "--json"},
			Scope:   []string{"**/*.go"},
		},
	},
}

// Detect returns the providers this repository is configured for: the
// built-ins by config-file presence, then every entry in .redline.yml. A
// declared entry with the same name as a built-in replaces it, so a
// repository can override the default invocation without losing detection.
func Detect(root string) ([]Config, error) {
	var out []Config
	seen := map[string]bool{}
	for _, b := range builtin {
		if seen[b.cfg.Name] {
			continue
		}
		if fileExists(filepath.Join(root, b.config)) {
			out = append(out, b.cfg)
			seen[b.cfg.Name] = true
		}
	}
	declared, err := loadConfig(root)
	if err != nil {
		// The built-in detections still stand: a broken .redline.yml must
		// not silently disable a provider the repository plainly has. The
		// caller gets both the partial list and the error.
		return out, err
	}
	for _, c := range declared {
		if c.Name == "" || c.Command == "" {
			return out, fmt.Errorf("context provider entry needs both name and command")
		}
		replaced := false
		for i := range out {
			if out[i].Name == c.Name {
				out[i] = c
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, c)
		}
	}
	return out, nil
}

func loadConfig(root string) ([]Config, error) {
	for _, name := range configNames {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var fc fileConfig
		if err := yaml.Unmarshal(data, &fc); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return fc.Context, nil
	}
	return nil, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// Claimed returns the changed paths this provider speaks for.
func (c Config) Claimed(changed []string) []string {
	var out []string
	for _, p := range changed {
		if len(c.Scope) == 0 || globmatch.MatchesAny(c.Scope, p) {
			out = append(out, p)
		}
	}
	return out
}

// Run invokes the provider in dir and parses its envelope.
//
// Failure is returned, never swallowed: a provider that could not run is a
// missing input the review must be told about, because an empty context and
// an unexamined one read identically to a model.
func (c Config) Run(dir, baseSHA string) (*envelope.Envelope, error) {
	if _, err := exec.LookPath(c.Command); err != nil {
		return nil, fmt.Errorf("%s is configured for this repository but not on PATH", c.Command)
	}
	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		if a == baseToken {
			args[i] = baseSHA
			continue
		}
		args[i] = a
	}
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Command, args...)
	cmd.Dir = dir
	// Stdout and Stderr here are not *os.File, so os/exec copies them
	// through a pipe and cmd.Run blocks until every write end is closed.
	// Killing the provider on the deadline does not close a pipe a
	// grandchild still holds — and a context provider spawns exactly those
	// (go list, git log -L) — so without a bound the DeadlineExceeded check
	// below is never reached and the review hangs. WaitDelay is what makes
	// the deadline enforceable: after it, the copy is abandoned.
	cmd.WaitDelay = 5 * time.Second
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s did not finish within %s", c.Name, runTimeout)
	}
	exit := 0
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		exit = ee.ExitCode()
		runErr = nil
	}
	if runErr != nil {
		return nil, fmt.Errorf("%s: %w", c.Name, runErr)
	}
	if !c.okExit(exit) {
		return nil, fmt.Errorf("%s exited %d: %s", c.Name, exit, firstLine(errBuf.String()))
	}
	env, err := Parse([]byte(out.String()))
	if err != nil {
		// Anything on stderr is the provider's own account of why it wrote
		// no usable envelope. This message reaches the model verbatim as the
		// Absent line, so dropping it tells the reviewer context is missing
		// without saying why.
		if diag := strings.TrimSpace(errBuf.String()); diag != "" {
			return nil, fmt.Errorf("%s: %w: %s", c.Name, err, firstLine(diag))
		}
		return nil, fmt.Errorf("%s: %w", c.Name, err)
	}
	return env, nil
}

func (c Config) okExit(code int) bool {
	if len(c.OKExitCodes) == 0 {
		return code == 0
	}
	for _, ok := range c.OKExitCodes {
		if ok == code {
			return true
		}
	}
	return false
}

// resultWrapper is the shape of a CLI that wraps every command's payload in
// a uniform result object. Unwrapping it is a generic allowance, not
// knowledge of any one tool: a provider should not have to break its own CLI
// contract to serve this one.
type resultWrapper struct {
	OK    *bool           `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// Parse reads an envelope from a provider's stdout, accepting either the
// bare envelope or one wrapped in an ok/error/data result.
func Parse(stdout []byte) (*envelope.Envelope, error) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return nil, fmt.Errorf("wrote no envelope")
	}
	body := []byte(trimmed)
	var wrap resultWrapper
	if err := json.Unmarshal(body, &wrap); err == nil && wrap.OK != nil {
		// A failure frame carries no payload when the CLI's result type
		// declares `Data any` with omitempty, so requiring data before
		// believing ok:false would unmarshal {"ok":false,"error":...} into a
		// zero envelope and blame a schema version for the tool's own
		// diagnosis. Trust the flag first.
		if !*wrap.OK {
			return nil, fmt.Errorf("reported failure: %s", wrap.Error)
		}
		if len(wrap.Data) > 0 {
			body = wrap.Data
		}
	}
	var env envelope.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("output was not an envelope: %w", err)
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	return &env, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "no stderr output"
	}
	return s
}
