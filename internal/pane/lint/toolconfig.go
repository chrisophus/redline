package lint

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// redlineConfig is the parsed shape of .redline.yml: the external tools a
// repository wants Redline to run, described declaratively so a new tool
// needs no Go code — only a repository owner who can point Redline at a
// command and say what its output looks like.
type redlineConfig struct {
	Tools []ToolConfig `yaml:"tools"`
}

// ToolConfig is one entry under `tools:`.
type ToolConfig struct {
	// Name identifies this tool in Rule ("vacuum/oas3-schema"), in the lint
	// delta's render lines, and in error messages.
	Name string `yaml:"name"`

	// Kind is "linter" (default): run the command once at the base revision
	// and once at head, in the same detached-worktree model golangci-lint
	// and eslint already use, and diff the two issue lists by fingerprint.
	// "differ": run the command once, comparing the file's content at the
	// base revision against its content at head directly — for a tool like
	// oasdiff that computes its own delta rather than linting one snapshot.
	// Every issue a differ tool reports is treated as introduced by this
	// change; there is no separate resolved count, because the tool already
	// decided what changed.
	Kind string `yaml:"kind"`

	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	// For Kind "differ", any argument equal to exactly "{{base}}" or
	// "{{head}}" is replaced with the path to the file's content at the base
	// revision (materialized to a temp file) or at head (its real path in
	// the tree under review), substituted once per scoped file.
	//
	// OKExitCodes are exit codes that mean "the tool ran" — whether or not it
	// found issues. Anything else means the tool failed. Empty defaults to
	// {0, 1}, the convention every linter Redline already runs follows.
	OKExitCodes []int `yaml:"okExitCodes"`

	// Scope is the glob patterns (relative to the repository root, "/" as
	// the separator, "**" matches across directories) this tool examines. A
	// changed file matching none of them means this tool has nothing to say
	// about the change, the same "no configured tool has scope" honesty the
	// built-in tools already give a repository that has not opted in.
	Scope []string `yaml:"scope"`

	// Format is how to parse the tool's stdout: "sarif" (the OASIS format
	// many static-analysis tools emit directly, needing no field mapping),
	// "spectral" (the JSON shape Spectral and Spectral-compatible OpenAPI
	// linters — vacuum's spectral-report among them — use), or "json" (an
	// arbitrary JSON shape, located with ResultsPath and Fields below).
	// Empty defaults to "json".
	Format string `yaml:"format"`

	// ResultsPath is the dotted path, only meaningful for Format "json", to
	// the array of results within the decoded JSON body; empty means the
	// root value is that array.
	ResultsPath string     `yaml:"resultsPath"`
	Fields      FieldPaths `yaml:"fields"`
	// SeverityMap translates the tool's own severity spelling (a string,
	// even for a tool that writes a bare number: "0", "1") to Redline's
	// error/warning/info. A value with no entry here falls back to a
	// best-effort guess (see mapSeverity), so this is only needed when a
	// tool's convention is not the obvious one.
	SeverityMap map[string]string `yaml:"severityMap"`

	// LineOffset is added to every line number this tool reports. Several
	// widely used formats (Spectral, and by extension vacuum) number lines
	// from 0, the LSP convention, while Redline numbers lines from 1 like
	// every other pane; set this to 1 for such a tool. Left at the default
	// (0) for a tool that already reports 1-indexed lines.
	LineOffset int `yaml:"lineOffset"`

	Baseline BaselineConfig `yaml:"baseline"`
}

// FieldPaths locates the fields of one result item within Format "json"
// output. Each is a dotted path ("range.start.line"), optionally indexing
// an array ("locations[0].uri"); see jsonPathGet.
type FieldPaths struct {
	File     string `yaml:"file"`
	Line     string `yaml:"line"`
	Rule     string `yaml:"rule"`
	Message  string `yaml:"message"`
	Severity string `yaml:"severity"`
}

// BaselineConfig picks how a linter-kind tool's "already existed" issues are
// known.
type BaselineConfig struct {
	// Mode is "revision" (default): run the tool at the base commit too, in
	// a cached detached worktree, and diff. "file": instead of a second
	// run, read File (an artifact this tool itself maintains and the
	// repository commits, e.g. gorefactor's own baseline-ratchet file) and
	// treat its entries as already known, reporting only what exceeds it.
	// Useful when running the tool twice is expensive, or when a repository
	// already maintains such a file for its own local `make lint`.
	Mode string `yaml:"mode"`
	// File is the baseline artifact's path, relative to the repository
	// root, read at head. Required when Mode is "file". It is parsed with
	// this same tool's Format/ResultsPath/Fields — a baseline is a
	// snapshot of the same shape the tool always produces.
	File string `yaml:"file"`
}

// configNames are the filenames .redline.yml is read from, checked in order.
var configNames = []string{".redline.yml", ".redline.yaml"}

// isRedlineConfig reports whether path is the repository's own tool config,
// so a change to it is treated the same way a changed .golangci.yml is.
func isRedlineConfig(path string) bool {
	base := filepath.Base(path)
	for _, n := range configNames {
		if base == n {
			return true
		}
	}
	return false
}

// loadConfig reads .redline.yml from root. A missing file is not an error —
// no repository is required to have one — and returns (nil, nil). A file
// that exists but fails to parse is: a misconfigured tool must be visible as
// broken, not silently absent from the review.
func loadConfig(root string) (*redlineConfig, error) {
	for _, name := range configNames {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var cfg redlineConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for i := range cfg.Tools {
			if cfg.Tools[i].Name == "" {
				return nil, fmt.Errorf("%s: tool %d has no name", name, i)
			}
			if cfg.Tools[i].Command == "" {
				return nil, fmt.Errorf("%s: tool %q has no command", name, cfg.Tools[i].Name)
			}
		}
		return &cfg, nil
	}
	return nil, nil
}
