// Package harness runs produce steps declared in .redline.yml so artifacts
// Redline reads (coverage profiles, and similar) exist before observe.
package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/globmatch"
	"gopkg.in/yaml.v3"
)

const produceTimeout = 30 * time.Minute

var configNames = []string{".redline.yml", ".redline.yaml"}

// Config is the harness section of .redline.yml.
type Config struct {
	Env      EnvConfig `yaml:"env"`
	Profiles []Profile `yaml:"profiles"`
	// Worktree steps run in the tree under review (including detached PR
	// worktrees) before panes that execute tools there, for example make
	// stub-ui so go:embed dist exists for golangci-lint typecheck.
	Worktree []Profile `yaml:"worktree"`
}

// EnvConfig is optional environment setup before each produce step.
type EnvConfig struct {
	// From is a shell script sourced before each produce command (set -a for
	// export). Relative paths resolve in the checkout that owns .redline.yml,
	// then in the tree under review.
	From string `yaml:"from"`
}

// Profile is one artifact the harness can produce before observe.
type Profile struct {
	ID      string        `yaml:"id"`
	Path    string        `yaml:"path"`
	Produce ProduceConfig `yaml:"produce"`
	// When is missing, stale, or always. Default stale: run when the artifact
	// is absent or older than a file this change touches.
	When  string   `yaml:"when"`
	Scope []string `yaml:"scope"`
}

// ProduceConfig is the command that writes Path.
type ProduceConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

type fileConfig struct {
	Harness Config `yaml:"harness"`
}

// Load reads harness.profiles from .redline.yml. A missing file or empty
// harness section returns (nil, nil).
func Load(root string) (*Config, error) {
	for _, name := range configNames {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var cfg fileConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if len(cfg.Harness.Profiles) == 0 && len(cfg.Harness.Worktree) == 0 {
			return nil, nil
		}
		if err := validate(&cfg.Harness, name); err != nil {
			return nil, err
		}
		return &cfg.Harness, nil
	}
	return nil, nil
}

func validate(cfg *Config, file string) error {
	seen := map[string]bool{}
	if err := validateProfiles(&cfg.Profiles, file, "profiles", "stale", seen); err != nil {
		return err
	}
	return validateProfiles(&cfg.Worktree, file, "worktree", "missing", seen)
}

func validateProfiles(profiles *[]Profile, file, section, defaultWhen string, seen map[string]bool) error {
	for i, p := range *profiles {
		if p.Path == "" {
			return fmt.Errorf("%s: harness.%s[%d] has no path", file, section, i)
		}
		if p.Produce.Command == "" {
			id := p.ID
			if id == "" {
				id = p.Path
			}
			return fmt.Errorf("%s: harness %s profile %q has no produce.command", file, section, id)
		}
		when := strings.TrimSpace(p.When)
		if when == "" {
			(*profiles)[i].When = defaultWhen
		} else if when != "missing" && when != "stale" && when != "always" {
			id := p.ID
			if id == "" {
				id = p.Path
			}
			return fmt.Errorf("%s: harness %s profile %q: when must be missing, stale, or always", file, section, id)
		}
		if (*profiles)[i].ID == "" {
			(*profiles)[i].ID = p.Path
		}
		if seen[(*profiles)[i].ID] {
			return fmt.Errorf("%s: duplicate harness profile id %q", file, (*profiles)[i].ID)
		}
		seen[(*profiles)[i].ID] = true
	}
	return nil
}

// Prepare runs produce for profiles that need fresh artifacts. changed is the
// paths in the diff under review. configRoot is where .redline.yml was read;
// env.from is resolved there when missing from produceRoot.
func Prepare(produceRoot, configRoot string, changed []string, cfg *Config) ([]string, error) {
	if cfg == nil {
		return nil, nil
	}
	return runProfiles(produceRoot, configRoot, changed, cfg.Env.From, cfg.Profiles, "preparing")
}

// PrepareWorktree runs harness.worktree steps in the tree under review before
// tool panes execute there (detached PR worktrees included). Each produce
// root is prepared at most once per run.
func PrepareWorktree(produceRoot, configRoot string, changed []string, cfg *Config) ([]string, error) {
	if cfg == nil {
		return nil, nil
	}
	if worktreePrepared[produceRoot] {
		return nil, nil
	}
	produced, err := runProfiles(produceRoot, configRoot, changed, cfg.Env.From, cfg.Worktree, "worktree")
	if err != nil {
		return produced, err
	}
	worktreePrepared[produceRoot] = true
	return produced, nil
}

func runProfiles(produceRoot, configRoot string, changed []string, envFrom string, profiles []Profile, label string) ([]string, error) {
	if len(profiles) == 0 {
		return nil, nil
	}
	var produced []string
	for _, p := range profiles {
		if !needsProduce(produceRoot, p, changed) {
			continue
		}
		fmt.Fprintf(os.Stderr, "redline: %s %s: %s %s\n", label, p.ID, p.Produce.Command, strings.Join(p.Produce.Args, " "))
		if err := runProduce(produceRoot, configRoot, envFrom, p.Produce); err != nil {
			return produced, fmt.Errorf("harness %s %s: %w", label, p.ID, err)
		}
		produced = append(produced, p.ID)
	}
	return produced, nil
}

func needsProduce(root string, p Profile, changed []string) bool {
	if len(p.Scope) > 0 {
		touched := false
		for _, path := range changed {
			if globmatch.MatchesAny(p.Scope, filepath.ToSlash(path)) {
				touched = true
				break
			}
		}
		if !touched {
			return false
		}
	}
	exists := artifactExists(root, p.Path)
	switch p.When {
	case "always":
		return true
	case "missing":
		return !exists
	default: // stale
		if !exists {
			return true
		}
		return cover.ArtifactStale(root, p.Path, changed)
	}
}

func artifactExists(root, rel string) bool {
	full := filepath.Join(root, rel)
	st, err := os.Stat(full)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func runProduce(produceRoot, configRoot, envFrom string, prod ProduceConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), produceTimeout)
	defer cancel()

	var script strings.Builder
	script.WriteString("set -euo pipefail")
	if envFrom != "" {
		script.WriteString("; set -a; source ")
		script.WriteString(shellQuote(resolveEnvFrom(produceRoot, configRoot, envFrom)))
		script.WriteString("; set +a")
	}
	script.WriteString("; ")
	script.WriteString(shellQuote(prod.Command))
	for _, a := range prod.Args {
		script.WriteString(" ")
		script.WriteString(shellQuote(a))
	}

	cmd := exec.CommandContext(ctx, "bash", "-lc", script.String())
	cmd.Dir = produceRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", prod.Command, strings.Join(prod.Args, " "), err)
	}
	return nil
}

// resolveEnvFrom finds env.from when the produce tree is a detached worktree
// that does not yet carry harness helper scripts from the caller's checkout.
func resolveEnvFrom(produceRoot, configRoot, envFrom string) string {
	if envFrom == "" {
		return ""
	}
	if filepath.IsAbs(envFrom) {
		return envFrom
	}
	if configRoot != "" {
		p := filepath.Join(configRoot, envFrom)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if produceRoot != "" {
		p := filepath.Join(produceRoot, envFrom)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return filepath.Join(produceRoot, envFrom)
	}
	return envFrom
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
