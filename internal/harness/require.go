package harness

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/globmatch"
	"github.com/chrisophus/redline/internal/mutation"
)

// ErrMissingArtifact is returned when a configured harness profile has no
// usable artifact for this change.
var ErrMissingArtifact = errors.New("harness artifact missing or stale")

// Roots lists directories to search for harness profile artifacts, in order.
type Roots struct {
	Observe string // tree under review (PR worktree)
	Origin  string // caller checkout when it describes the reviewed revision
	Caller  string // checkout where redline was invoked
}

// RequireOpts tunes which configured profiles are enforced.
type RequireOpts struct {
	// SkipCoverage does not fail when a configured coverage profile is missing
	// or stale. The report may still record it as unknown.
	SkipCoverage bool
}

// Require fails fast when a configured profile applies to this change but its
// artifact is missing or stale. Unconfigured profiles are not checked.
func Require(roots Roots, changed []string, cfg *Config, opts RequireOpts) error {
	if cfg == nil {
		return nil
	}
	for _, p := range cfg.Profiles {
		if !appliesToRequire(p, changed) {
			continue
		}
		if opts.SkipCoverage && cover.IsProfilePath(p.Path) {
			continue
		}
		root, stale := artifactStatus(roots, p.Path, changed, p.When)
		if root == "" {
			return fmt.Errorf("harness profile %q: %s is missing for this change; run with --prepare when produce is configured: %w",
				p.ID, p.Path, ErrMissingArtifact)
		}
		if stale {
			return fmt.Errorf("harness profile %q: %s is stale for this change; run with --prepare when produce is configured: %w",
				p.ID, p.Path, ErrMissingArtifact)
		}
	}
	return nil
}

// ArtifactRoot returns the first search root that holds a non-empty artifact at
// rel, or "" when none do.
func ArtifactRoot(roots Roots, rel string) string {
	for _, root := range artifactSearchRoots(roots, rel) {
		if artifactExists(root, rel) {
			return root
		}
	}
	return ""
}

// ProfileApplies reports whether a harness profile's scope matches this change.
func ProfileApplies(p Profile, changed []string) bool {
	return profileApplies(p, changed)
}

// RequiresPath reports whether any configured profile with this path applies.
func (cfg *Config) RequiresPath(path string, changed []string) bool {
	if cfg == nil {
		return false
	}
	for _, p := range cfg.Profiles {
		if p.Path != path {
			continue
		}
		if profileApplies(p, changed) {
			return true
		}
	}
	return false
}

// RequiresCoverage reports whether a configured coverage profile applies.
func (cfg *Config) RequiresCoverage(changed []string) bool {
	if cfg == nil {
		return false
	}
	for _, p := range cfg.Profiles {
		if cover.IsProfilePath(p.Path) && appliesToRequire(p, changed) {
			return true
		}
	}
	return false
}

// MutationPaths returns configured gomutants report paths, if any.
func (cfg *Config) MutationPaths() []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for _, p := range cfg.Profiles {
		if mutation.IsReportPath(p.Path) {
			out = append(out, p.Path)
		}
	}
	return out
}

func profileApplies(p Profile, changed []string) bool {
	if len(p.Scope) == 0 {
		return len(changed) > 0
	}
	for _, path := range changed {
		if globmatch.MatchesAny(p.Scope, filepath.ToSlash(path)) {
			return true
		}
	}
	return false
}

func appliesToRequire(p Profile, changed []string) bool {
	return profileApplies(p, changed)
}

func appliesToProduce(p Profile, changed []string) bool {
	if len(p.Scope) == 0 {
		return true
	}
	return profileApplies(p, changed)
}

func artifactStatus(roots Roots, rel string, changed []string, when string) (root string, stale bool) {
	for _, r := range artifactSearchRoots(roots, rel) {
		if !artifactExists(r, rel) {
			continue
		}
		switch when {
		case "missing":
			return r, false
		default:
			if cover.ArtifactStale(r, rel, changed) {
				return r, true
			}
			return r, false
		}
	}
	return "", false
}

// artifactSearchRoots is where a profile path may be read. Coverage for a
// detached PR worktree must come from that worktree (or origin when it
// describes the same revision), not the invocation checkout's unrelated
// profile. Mutation and other hand-run reports still read from the caller.
func artifactSearchRoots(roots Roots, rel string) []string {
	out := make([]string, 0, 3)
	if roots.Observe != "" {
		out = append(out, roots.Observe)
	}
	if roots.Origin != "" && roots.Origin != roots.Observe {
		out = append(out, roots.Origin)
	}
	if cover.IsProfilePath(rel) {
		return out
	}
	if roots.Caller != "" && roots.Caller != roots.Observe && roots.Caller != roots.Origin {
		out = append(out, roots.Caller)
	}
	return out
}
