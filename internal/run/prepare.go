package run

import (
	"fmt"
	"strings"

	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/harness"
	"github.com/ccason/redline/internal/target"
)

// Prepare runs harness produce steps declared in .redline.yml before observe.
// It uses the caller's checkout (opts.Dir) for artifacts, not a detached PR
// worktree, because coverage profiles and make targets live where the developer
// runs tests.
func Prepare(opts Options) ([]string, error) {
	tgt, err := target.Resolve(target.Options{
		Dir: opts.Dir, PR: opts.PR, Branch: opts.Branch,
		Commit: opts.Commit, Range: opts.Range, Base: opts.Base,
	})
	if err != nil {
		return nil, err
	}
	repo, err := gitx.Open(tgt.Dir)
	if err != nil {
		return nil, err
	}
	if tgt.Base != "" {
		opts.Base = tgt.Base
	}
	baseRef := resolveRef(repo, opts.Base)
	if baseRef == "" {
		return nil, fmt.Errorf("cannot determine a base revision: tried %s. "+
			"Pass --base REF, or make a first commit if this repository has none",
			strings.Join(defaultRefs, ", "))
	}
	baseSHA, err := repo.MergeBase(baseRef)
	if err != nil {
		return nil, fmt.Errorf("base %q: %w", baseRef, err)
	}
	changed, err := repo.ChangedPaths(baseSHA)
	if err != nil {
		return nil, err
	}
	origin, err := gitx.Open(opts.Dir)
	if err != nil {
		return nil, err
	}
	cfg, err := harness.Load(origin.Root)
	if err != nil {
		return nil, err
	}
	return harness.Prepare(origin.Root, changed, cfg)
}
