package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chrisophus/redline/internal/gitx"
)

// A run writes its session, its report and everything a later command reads
// into one directory. Under the default --out that directory is
// .redline/sessions/<name>, one per target, so observing a pull request and
// then the working tree keeps both instead of the second overwriting the
// first. An explicit --out is still the session directory itself, as it always
// was: the eval's fixtures, the report server re-running itself with --out, and
// any script reading <out>/findings.json all depend on that.
const (
	sessionsDir = "sessions"
	// latestFile names the session the last run wrote, which is the one a
	// command given no target and no --session works on.
	latestFile = "latest"
)

var (
	sessionNameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	prURLNumber     = regexp.MustCompile(`/pull/(\d+)`)
)

// observeOnlyFlags mean something only while a change is being observed, so
// review accepts them only with --run.
var observeOnlyFlags = []string{"base", "upstream", "migrations", "prepare", "allow-missing-coverage", "no-lint", "no-context"}

// targetSession names the session a command's target flags point at. It reads
// only the flags, so `run --pr 7` and a later `review --pr 7` arrive at the
// same directory without asking gh or git anything. Post is only ever about a
// pull request, and refuses the other target flags itself with a better
// message than a missing session would give.
func (o opts) targetSession(cmd string) string {
	switch {
	case o.pr != "":
		if m := prURLNumber.FindStringSubmatch(o.pr); m != nil {
			return "pr-" + m[1]
		}
		return "pr-" + safeName(strings.TrimPrefix(o.pr, "#"))
	case cmd == "post":
		return ""
	case o.branch != "":
		return "branch-" + safeName(o.branch)
	case o.commit != "":
		return "commit-" + safeName(o.commit)
	case o.revRange != "":
		return "range-" + safeName(o.revRange)
	}
	return ""
}

// validSessionName is a single directory name that stays under
// .redline/sessions: no separator, and a first character that cannot begin
// "." or "..". A range's "A..B" inside one name is still one directory.
func validSessionName(name string) bool {
	return sessionNameRE.MatchString(name) && filepath.IsLocal(name)
}

func safeName(s string) string {
	s = strings.Trim(unsafeNameChars.ReplaceAllString(s, "-"), "-.")
	if s == "" {
		return "unnamed"
	}
	return s
}

// workingTreeName names the session for the working tree by the branch it is
// on, so work on two branches in one sandbox keeps two sessions. A detached
// HEAD is named by its commit.
func workingTreeName() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "tree"
	}
	repo, err := gitx.Open(cwd)
	if err != nil {
		return "tree"
	}
	if branch, err := repo.Branch(); err == nil && branch != "" {
		return "tree-" + safeName(branch)
	}
	if head, err := repo.Head(); err == nil && len(head) >= 7 {
		return "tree-" + head[:7]
	}
	return "tree"
}

// resolveSession points o.out at the session directory the command works on,
// before it runs. set holds the flags the caller passed.
func resolveSession(c command, o *opts, set map[string]bool) error {
	if c.name == "gc" {
		return nil
	}
	observes := c.name == "run" || (c.name == "review" && o.observe)
	if c.name == "review" && !o.observe {
		for _, name := range observeOnlyFlags {
			if set[name] {
				return fmt.Errorf("--%s applies when review observes the change first; add --run", name)
			}
		}
	}
	o.root = o.out
	if set["out"] {
		if o.session != "" {
			return errors.New("--out names the session directory itself and --session names one under " +
				".redline/sessions; pass one of them")
		}
		return nil
	}
	// --stats reads the cost ledger, which every session shares.
	if c.name == "review" && o.stats && !o.observe {
		return nil
	}
	name := o.session
	if name != "" && !validSessionName(name) {
		return fmt.Errorf("--session %q: use letters, digits, '.', '_' and '-', starting with a letter or digit", name)
	}
	if name == "" {
		name = o.targetSession(c.name)
	}
	if name == "" && observes {
		name = workingTreeName()
	}
	if name == "" {
		latest, err := os.ReadFile(filepath.Join(o.root, latestFile))
		if err != nil {
			// No run has written a session under this layout. A session an
			// older Redline wrote straight into --out is still read from
			// there, and with neither the command says there was no prior run.
			return nil
		}
		name = strings.TrimSpace(string(latest))
	}
	// Every name becomes a path under .redline/sessions, whether the flag, the
	// target or the latest file supplied it, and a hand-edited latest file is
	// no more trusted than a flag.
	if !validSessionName(name) {
		return fmt.Errorf("session name %q is not a plain directory name", name)
	}
	dir := filepath.Join(o.root, sessionsDir, name)
	if !observes {
		if _, err := os.Stat(filepath.Join(dir, "session.json")); err != nil { //nolint:gosec // G703: name validated above
			where := filepath.Join(o.root, sessionsDir)
			if c.name == "review" {
				return fmt.Errorf("no saved session %q in %s; add --run to observe it first", name, where)
			}
			return fmt.Errorf("no saved session %q in %s; run `redline run` with the same target first", name, where)
		}
	}
	o.out = dir
	o.sessionKey = name
	return nil
}

// recordSession marks the session just written as the latest and says where
// it is. Nothing is recorded when --out named the directory.
func recordSession(o opts) {
	if o.sessionKey == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(o.root, latestFile), []byte(o.sessionKey+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "redline: could not record %s as the latest session: %v\n", o.sessionKey, err)
	}
	fmt.Fprintf(os.Stderr, "redline: session %s in %s\n", o.sessionKey, o.out)
}

// ledgerDir is where review costs are recorded: the root every session
// shares, so --stats describes every review in this sandbox.
func (o opts) ledgerDir() string {
	if o.root != "" {
		return o.root
	}
	return o.out
}
