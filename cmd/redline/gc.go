package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/run"
)

// cmdGC removes the detached worktrees Redline caches under
// ~/.redline/worktrees for the current repository. The cache is deliberate: a
// reviewed revision is immutable, so a second run against it is instant, which
// is why nothing prunes it automatically. gc is the manual reclaim, scoped to
// this repository. Removed worktrees are recreated on demand by the next run.
func cmdGC(o opts) error {
	if o.keepHeads < 0 {
		return fmt.Errorf("--keep-heads %d: give a count of at least 1", o.keepHeads)
	}
	if o.keepHeads > 0 || o.headsOlderThan != "" {
		return gcHeads(o)
	}
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

// gcHeads removes archived heads past the count or age limit. A head that an
// entry in the misses ledger points at stays: the ledger is the regression
// set, and a miss with no session to rerun is worth much less. Those heads do
// not count toward the --keep-heads limit either, so keeping them never pushes
// out a head that would otherwise have stayed.
func gcHeads(o opts) error {
	var cutoff time.Time
	if o.headsOlderThan != "" {
		d, err := time.ParseDuration(o.headsOlderThan)
		if err != nil {
			return fmt.Errorf("--heads-older-than: %w", err)
		}
		cutoff = time.Now().Add(-d)
	}
	root := o.out
	if root == "" {
		root = ".redline"
	}
	ledger := o.jsonl
	if ledger == "" {
		ledger = root
	}
	entries, err := miss.Read(ledger)
	if err != nil {
		return err
	}
	held := miss.Held(entries)
	sessions, err := os.ReadDir(filepath.Join(root, sessionsDir))
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("redline: no archived heads to remove")
			return nil
		}
		return err
	}
	removed := 0
	var freed int64
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		heads, err := run.Heads(filepath.Join(root, sessionsDir, s.Name()))
		if err != nil {
			return err
		}
		kept := 0
		for _, h := range heads {
			if held[s.Name()+"/"+h.SHA] {
				continue
			}
			tooMany := o.keepHeads > 0 && kept >= o.keepHeads
			tooOld := !cutoff.IsZero() && h.Archived.Before(cutoff)
			if !tooMany && !tooOld {
				kept++
				continue
			}
			freed += dirSize(h.Dir)
			if err := os.RemoveAll(h.Dir); err != nil {
				return err
			}
			removed++
		}
	}
	if removed == 0 {
		fmt.Println("redline: no archived heads to remove")
		return nil
	}
	fmt.Printf("redline: removed %d archived head(s), freed %s\n", removed, humanBytes(freed))
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
