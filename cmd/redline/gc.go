package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ccason/redline/internal/gitx"
)

// cmdGC removes the detached worktrees Redline caches under
// ~/.redline/worktrees for the current repository. The cache is deliberate: a
// reviewed revision is immutable, so a second run against it is instant, which
// is why nothing prunes it automatically. gc is the manual reclaim, scoped to
// this repository. Removed worktrees are recreated on demand by the next run.
func cmdGC(o opts) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repo, err := gitx.Open(cwd)
	if err != nil {
		return err
	}
	dirs, err := repo.CachedWorktrees()
	if err != nil {
		return err
	}
	var cutoff time.Time
	if o.olderThan != "" {
		d, err := time.ParseDuration(o.olderThan)
		if err != nil {
			return fmt.Errorf("--older-than: %w", err)
		}
		cutoff = time.Now().Add(-d)
	}
	removed := 0
	var freed int64
	for _, dir := range dirs {
		if !cutoff.IsZero() {
			if fi, err := os.Stat(dir); err == nil && fi.ModTime().After(cutoff) {
				continue
			}
		}
		freed += dirSize(dir)
		repo.RemoveWorktree(dir)
		removed++
	}
	if removed == 0 {
		fmt.Println("redline: no cached worktrees to remove")
		return nil
	}
	fmt.Printf("redline: removed %d cached worktree(s), freed %s\n", removed, humanBytes(freed))
	return nil
}

// dirSize sums the bytes under dir, best effort: an unreadable entry contributes
// nothing rather than failing the reclaim.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
