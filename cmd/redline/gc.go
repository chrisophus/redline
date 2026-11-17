package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chrisophus/redline/internal/gitx"
)

// cmdGC removes the detached worktrees Redline caches under
// ~/.redline/worktrees for the current repository. The cache is deliberate: a
// reviewed revision is immutable, so a second run against it is instant, which
// is why nothing prunes it automatically. gc is the manual reclaim, scoped to
// this repository. Removed worktrees are recreated on demand by the next run.
//
// With --keep-heads or --heads-older-than it then trims the sessions kept for
// each reviewed head, which the worktree cache does not hold.
func cmdGC(o opts) error {
	cutoff, err := cutoffFor("--older-than", o.olderThan)
	if err != nil {
		return err
	}
	headsCutoff, err := cutoffFor("--heads-older-than", o.headsOlderThan)
	if err != nil {
		return err
	}
	if o.keepHeads < 0 {
		return errors.New("--keep-heads: give a count of zero or more")
	}
	trimHeads := o.keepHeads > 0 || !headsCutoff.IsZero()
	if err := gcWorktrees(cutoff); err != nil {
		// Trimming kept heads reads only --out, so it still works outside a
		// checkout. Say why the worktrees were skipped and carry on.
		if !trimHeads {
			return err
		}
		fmt.Fprintf(os.Stderr, "redline: cached worktrees not cleaned: %v\n", err)
	}
	if trimHeads {
		ledger := o.jsonl
		if ledger == "" {
			ledger = o.out
		}
		n, freed := pruneHeads(o.out, ledger, o.keepHeads, headsCutoff)
		fmt.Printf("redline: removed %d kept head session(s), freed %s\n", n, humanBytes(freed))
	}
	return nil
}

func gcWorktrees(cutoff time.Time) error {
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

// cutoffFor turns a duration flag into the time before which things are old,
// or the zero time when the flag is empty.
func cutoffFor(flagName, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", flagName, err)
	}
	return time.Now().Add(-d), nil
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
