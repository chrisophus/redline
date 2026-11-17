package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/run"
)

// A session directory such as .redline/sessions/pr-7 holds the latest review of
// that target. Each head that was reviewed also keeps its own copy under
// pr-7/<head-sha>, so the review of a commit is still there after the next
// push overwrites pr-7 itself. A reader opens an older one with --head.
var (
	headDirRE = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	headArgRE = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

// archiveHead copies the session just written into <session>/<head-sha>. It is
// a no-op for a session with no commit to name it by (the working tree) and
// for an explicit --out, which is a directory the caller manages.
//
// A failure is reported and not returned: the session itself is already on
// disk, and a review that cost money is not worth failing over a copy.
func archiveHead(o opts, res *run.Result) {
	if o.sessionKey == "" || res == nil || res.Change == nil || res.Change.Target == nil {
		return
	}
	head := res.Change.Target.Head
	if !headDirRE.MatchString(head) {
		return
	}
	if err := copySession(o.out, filepath.Join(o.out, head), change.ReviewIdentity(res.Report.BaseSHA, res.Change)); err != nil {
		fmt.Fprintf(os.Stderr, "redline: could not keep the session for %s: %v\n", head, err)
	}
}

// copySession replaces dst with the files of src. The debug captures and the
// per-head archives that live inside src are left out: the first are large and
// belong to one run, and the second would copy the archive into itself.
//
// The copy is made in a temporary directory beside dst and renamed into place,
// so a copy that fails partway leaves the previous archive, or none, and never
// a half-written head that keptHeads would list.
//
// review.json and the postmortem trace outlive a run, so the session can hold
// those of an earlier head. A copy of either that is stamped with a different
// change than id is left out, so the archive never shows another commit's
// review as this head's. A file that carries no stamp, or cannot be read, is
// kept: it says nothing against this head.
func copySession(src, dst, id string) error {
	tmp, err := os.MkdirTemp(src, tmpArchivePrefix)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// MkdirTemp makes the directory owner-only, and the archive should be as
	// readable as the session it copies.
	if err := os.Chmod(tmp, 0o755); err != nil { //nolint:gosec // matches the session directory's mode
		return err
	}
	if err := copyDir(src, tmp, true); err != nil {
		return err
	}
	for _, name := range []string{"review.json", postmortem.File} {
		if staleFor(filepath.Join(tmp, name), id) {
			if err := os.Remove(filepath.Join(tmp, name)); err != nil {
				return err
			}
		}
	}
	// The earlier archive is moved aside and removed only once the new one is
	// in place, so a failed rename leaves it as it was.
	aside := dst + asideSuffix
	if err := os.RemoveAll(aside); err != nil {
		return err
	}
	moved := true
	if err := os.Rename(dst, aside); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		moved = false
	}
	if err := os.Rename(tmp, dst); err != nil {
		if moved {
			_ = os.Rename(aside, dst)
		}
		return err
	}
	return os.RemoveAll(aside)
}

// staleFor reports whether the JSON file at path names a revision other than
// id.
func staleFor(path, id string) bool {
	buf, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var stamp struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(buf, &stamp) != nil {
		return false
	}
	return stamp.Revision != "" && stamp.Revision != id
}

// tmpArchivePrefix names the directory a copy is built in. It is inside the
// session and so is skipped when copying, like the archives.
// asideSuffix marks the earlier archive while its replacement is renamed in.
// A copy skips one a crash left behind.
const asideSuffix = ".old"

const tmpArchivePrefix = ".keep-"

func copyDir(src, dst string, top bool) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		switch {
		case e.IsDir():
			if top && (e.Name() == "debug" || headDirRE.MatchString(e.Name()) || strings.HasPrefix(e.Name(), tmpArchivePrefix) || strings.HasSuffix(e.Name(), asideSuffix)) {
				continue
			}
			if err := copyDir(from, to, false); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := copyFile(from, to); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	// The copy keeps the original's permissions, so it is as readable as the
	// session it was taken from.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// applyHead points o.out at the archived session for --head, inside the
// session directory o.out already names. SHA may be abbreviated to seven
// characters or more, as long as only one kept head matches.
func applyHead(o *opts) error {
	if o.head == "" {
		return nil
	}
	if !headArgRE.MatchString(o.head) {
		return fmt.Errorf("--head %q: give a commit SHA, at least 7 hex characters", o.head)
	}
	dir, err := findHead(o.out, o.head)
	if err != nil {
		return err
	}
	o.out = dir
	return nil
}

func findHead(session, sha string) (string, error) {
	sha = strings.ToLower(sha)
	var match []string
	for _, h := range keptHeads(session) {
		if strings.HasPrefix(h.sha, sha) {
			match = append(match, h.sha)
		}
	}
	switch len(match) {
	case 1:
		return filepath.Join(session, match[0]), nil
	case 0:
		return "", fmt.Errorf("no kept session for head %s in %s; `redline run` or `redline review` of that commit keeps one", sha, session)
	}
	return "", fmt.Errorf("--head %s matches %d kept heads in %s; give more of it", sha, len(match), session)
}

// keptHead is one archived head: its commit and when its session was written.
type keptHead struct {
	sha  string
	when time.Time
}

// keptHeads lists the archived heads under one session directory, newest
// first. A directory with the right name and no session.json is not one.
func keptHeads(session string) []keptHead {
	entries, err := os.ReadDir(session)
	if err != nil {
		return nil
	}
	var out []keptHead
	for _, e := range entries {
		if !e.IsDir() || !headDirRE.MatchString(e.Name()) {
			continue
		}
		fi, err := os.Stat(filepath.Join(session, e.Name(), "session.json"))
		if err != nil {
			continue
		}
		out = append(out, keptHead{sha: e.Name(), when: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].when.After(out[j].when) })
	return out
}

// pruneHeads removes archived heads under every session in root: all but the
// newest keep of each session when keep is above zero, and any written before
// cutoff when it is not zero. A head that a miss in ledger's misses.jsonl names
// is kept whatever its age, since the miss is the record of why it matters and
// its answers can only be checked against that session. A miss names its head
// by pull request number and commit, which holds wherever the ledger was
// recorded from. If the ledger cannot be read nothing is removed, because the
// heads it protects are unknown. It returns how many it removed and their size.
func pruneHeads(root, ledger string, keep int, cutoff time.Time) (int, int64, error) {
	misses, err := miss.Read(ledger)
	if err != nil {
		return 0, 0, fmt.Errorf("kept heads not trimmed: %w", err)
	}
	sessions, _ := filepath.Glob(filepath.Join(root, sessionsDir, "*"))
	removed := 0
	var freed int64
	for _, s := range sessions {
		for i, h := range keptHeads(s) {
			if (keep <= 0 || i < keep) && (cutoff.IsZero() || !h.when.Before(cutoff)) {
				continue
			}
			if pinnedByMiss(misses, filepath.Base(s), h.sha) {
				continue
			}
			dir := filepath.Join(s, h.sha)
			size := dirSize(dir)
			if err := os.RemoveAll(dir); err != nil {
				fmt.Fprintf(os.Stderr, "redline: could not remove %s: %v\n", dir, err)
				continue
			}
			freed += size
			removed++
		}
	}
	return removed, freed, nil
}

// pinnedByMiss reports whether a miss on the pull request behind session names
// the head sha. A head typed short and never matched to a session is a prefix.
func pinnedByMiss(misses []miss.Entry, session, sha string) bool {
	for _, m := range misses {
		if session == "pr-"+strconv.Itoa(m.PR) && len(m.Head) >= 7 && strings.HasPrefix(sha, m.Head) {
			return true
		}
	}
	return false
}
