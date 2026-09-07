package run

import (
	"fmt"
	"strings"

	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/harness"
	"github.com/chrisophus/redline/internal/target"
)

// Prepare runs harness produce steps declared in .redline.yml before observe.
// Profiles run in the tree under review when that is a detached worktree (PR
// or branch), so coverage.out describes the revision being reviewed. Otherwise
// they run in the caller's checkout.
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
	cfg, err := harness.Load(harnessConfigRoot(opts.Dir, tgt))
	if err != nil {
		return nil, err
	}
	return harness.Prepare(harnessProduceRoot(origin.Root, tgt.Dir), harnessConfigRoot(opts.Dir, tgt), changed, cfg)
}

// harnessProduceRoot is where harness profiles write artifacts. A detached PR
// or branch worktree must run test-coverage itself; the origin checkout's
// profile does not describe that revision unless it is checked out there.
func harnessProduceRoot(originRoot, observeRoot string) string {
	if observeRoot != "" && observeRoot != originRoot {
		return observeRoot
	}
	return originRoot
}

// harnessConfigRoot is where .redline.yml is read from: the checkout the user
// ran redline in, not necessarily the detached worktree under review.
func harnessConfigRoot(dir string, tgt *target.Target) string {
	if dir == "" {
		return tgt.Dir
	}
	orig, err := gitx.Open(dir)
	if err != nil {
		return tgt.Dir
	}
	return orig.Root
}
