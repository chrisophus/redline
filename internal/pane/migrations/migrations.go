// Package migrations implements the git-level half of the SQL pane: check 1
// (a migration that already exists at merge-base was modified) and check 2 (a
// new migration's version prefix already exists upstream).
//
// Both are pure git filename-and-blob comparisons. No database, no container,
// no stack bring-up — which is why they ship first.
package migrations

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/pane"
)

// Substrate is this pane's name in the findings schema. Checks 1 and 2 are the
// no-infrastructure part of the SQL pane, not a pane of their own.
const Substrate = "redline/sql"

// filePattern matches a golang-migrate versioned migration:
// {version}_{name}.{up|down}.sql. Rung 1 runs off this convention plus
// command-line flags; redline.toml is not needed until the two-worktree
// harness lands.
var filePattern = regexp.MustCompile(`(?:^|/)(\d+)_[^/]*\.(up|down)\.sql$`)

// parse returns the version prefix and direction of a migration path, and
// whether the path is a migration at all.
func parse(path string) (version, direction string, ok bool) {
	m := filePattern.FindStringSubmatch(path)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// Set is the migration files visible at one revision.
type Set struct {
	Rev   string
	Files map[string]string // path → blob SHA

	// UpstreamVersions is the set of migration versions already published on
	// the upstream branch, and is populated on the base-side observation only:
	// it is part of "what the world already has". UpstreamRef is empty and
	// UpstreamErr set when the upstream ref could not be read — which is an
	// unknown, never a pass.
	UpstreamVersions map[string]string // version → representative path upstream
	UpstreamRef      string
	UpstreamErr      string
}

// ID implements pane.Observation.
func (s *Set) ID() string {
	rev := s.Rev
	if rev == "" {
		rev = "worktree"
	}
	return "migrations@" + rev
}

func (s *Set) versions() map[string][]string {
	byVersion := map[string][]string{}
	for path := range s.Files {
		if v, _, ok := parse(path); ok {
			byVersion[v] = append(byVersion[v], path)
		}
	}
	for v := range byVersion {
		sort.Strings(byVersion[v])
	}
	return byVersion
}

// Pane observes migration files at two revisions.
type Pane struct {
	Repo *gitx.Repo
	// UpstreamRef is the branch new migrations must not collide with,
	// typically origin/main.
	UpstreamRef string
	// Dir optionally restricts the pane to one migrations directory. Empty
	// means every path matching the golang-migrate naming convention.
	Dir string
}

// Name implements pane.Pane.
func (p *Pane) Name() string { return Substrate }

// Scope returns the migration files this change touches.
func (p *Pane) Scope(changed []string) []string {
	var out []string
	for _, path := range changed {
		if p.match(path) {
			out = append(out, path)
		}
	}
	return out
}

func (p *Pane) match(path string) bool {
	if _, _, ok := parse(path); !ok {
		return false
	}
	if p.Dir != "" {
		prefix := strings.TrimSuffix(p.Dir, "/") + "/"
		return strings.HasPrefix(path, prefix)
	}
	return true
}

// Observe extracts the migration file set at a revision. Static extraction:
// this pane needs no running system.
func (p *Pane) Observe(rev pane.Revision) (pane.Observation, error) {
	var (
		blobs map[string]string
		err   error
	)
	if rev.Rev == "" {
		blobs, err = p.Repo.WorktreeBlobs()
	} else {
		blobs, err = p.Repo.Blobs(rev.Rev)
	}
	if err != nil {
		return nil, err
	}
	set := &Set{Rev: rev.Rev, Files: map[string]string{}}
	for path, sha := range blobs {
		if p.match(path) {
			set.Files[path] = sha
		}
	}
	if rev.Name == "base" {
		p.observeUpstream(set)
	}
	return set, nil
}

// observeUpstream records the migration versions already on the upstream
// branch. A missing upstream ref is recorded, not swallowed: check 2 then
// cannot be answered and must render as undetermined.
func (p *Pane) observeUpstream(set *Set) {
	if p.UpstreamRef == "" {
		set.UpstreamErr = "no upstream ref configured"
		return
	}
	set.UpstreamRef = p.UpstreamRef
	blobs, err := p.Repo.Blobs(p.UpstreamRef)
	if err != nil {
		set.UpstreamErr = fmt.Sprintf("cannot read %s: %v", p.UpstreamRef, err)
		return
	}
	set.UpstreamVersions = map[string]string{}
	for path := range blobs {
		if !p.match(path) {
			continue
		}
		v, _, _ := parse(path)
		if prev, ok := set.UpstreamVersions[v]; !ok || path < prev {
			set.UpstreamVersions[v] = path
		}
	}
}

// Diff compares two migration sets. Pure.
func (p *Pane) Diff(before, after pane.Observation) (pane.Result, error) {
	base, ok := before.(*Set)
	if !ok {
		return pane.Result{}, fmt.Errorf("migrations: base observation is %T", before)
	}
	head, ok := after.(*Set)
	if !ok {
		return pane.Result{}, fmt.Errorf("migrations: head observation is %T", after)
	}
	evidence := []string{base.ID(), head.ID()}

	res := pane.Result{Evidence: map[string]pane.Artifact{}}
	res.Findings = append(res.Findings, p.checkModified(base, head, evidence, res.Evidence)...)
	added := addedVersions(base, head)
	collisions, unknowns, confirmed := p.checkCollisions(base, head, added, evidence)
	res.Findings = append(res.Findings, collisions...)
	res.Unknowns = append(res.Unknowns, unknowns...)
	res.Confirmations = append(res.Confirmations, confirmed...)

	// State the denominator. "1 file unchanged" alongside a modified sibling
	// reads as reassurance; "1 of 2" reads as what it is.
	if total := len(base.Files); total > 0 {
		unchanged := len(p.unchangedExisting(base, head))
		if unchanged == total {
			res.Confirmations = append(res.Confirmations, findings.Confirmation{
				Substrate: Substrate,
				Rule:      "migration-immutable",
				Message:   fmt.Sprintf("all %d migration file(s) already merged at the base are byte-identical in this change", total),
			})
		} else {
			res.Unknowns = append(res.Unknowns, findings.Unknown{
				Substrate: Substrate,
				Message:   fmt.Sprintf("%d of %d migration file(s) merged at the base were changed by this branch", total-unchanged, total),
				Reason:    "immutability does not hold for this change; see findings",
			})
		}
	}
	res.Render = p.render(base, head, added)
	return res, nil
}

// checkModified is check 1: a migration file that exists at merge-base was
// edited or deleted. Applied migrations are immutable by construction —
// golang-migrate records the version, not the content, so an edited migration
// silently diverges between anyone who has already run it and anyone who has
// not.
func (p *Pane) checkModified(base, head *Set, evidence []string, artifacts map[string]pane.Artifact) []findings.Finding {
	var paths []string
	for path := range base.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var out []findings.Finding
	for _, path := range paths {
		baseSHA := base.Files[path]
		headSHA, present := head.Files[path]
		switch {
		case !present:
			version, _, _ := parse(path)
			out = append(out, findings.Finding{
				File:      path,
				Rule:      "migration-deleted-after-merge",
				Substrate: Substrate,
				Category:  findings.CategorySchema,
				Severity:  findings.SeverityError,
				Message:   fmt.Sprintf("migration %s was deleted, but it exists at the base revision and may already be applied", path),
				Anchor:    &findings.Anchor{Kind: "migration", ID: version},
				Evidence:  evidence,
				Expected:  "migrations present at the base revision are immutable",
				Observed:  "file removed from the working tree",
				Context:   "golang-migrate records applied versions, not contents. A database that already ran this version will never re-run it, and will never see the removal.",
			})
		case headSHA != baseSHA:
			version, _, _ := parse(path)
			// The reviewer's actual question is "what did the edit change?".
			// A pair of blob hashes does not answer it; the SQL does.
			ev := evidence
			if diff := p.Repo.DiffPath(base.Rev, path); diff != "" {
				id := "diff:" + path
				artifacts[id] = pane.Artifact{Kind: "diff", Content: diff}
				ev = append(append([]string{}, evidence...), id)
			}
			out = append(out, findings.Finding{
				File:      path,
				Rule:      "migration-modified-after-merge",
				Substrate: Substrate,
				Category:  findings.CategorySchema,
				Severity:  findings.SeverityError,
				Message:   fmt.Sprintf("migration %s was modified, but it exists at the base revision and may already be applied", path),
				Anchor:    &findings.Anchor{Kind: "migration", ID: version},
				Evidence:  ev,
				Expected:  "migrations present at the base revision are immutable",
				Observed:  fmt.Sprintf("SQL edited in place (blob %s → %s)", short(baseSHA), short(headSHA)),
				FixCmd:    "revert the edit and add a new migration expressing the change",
				Context:   "Every database that already ran this version keeps the old schema; every database that has not gets the new one. The two silently diverge.",
			})
		}
	}
	return out
}

// checkCollisions is check 2: a migration added by this change carries a
// version prefix that already exists upstream. Two branches picking the same
// version is the ordinary way this happens, and golang-migrate will refuse to
// apply the second one — after the branch is merged, not before.
func (p *Pane) checkCollisions(base, head *Set, added map[string][]string, evidence []string) (out []findings.Finding, unknowns []findings.Unknown, confirmed []findings.Confirmation) {
	if base.UpstreamVersions == nil {
		if len(added) > 0 {
			detail := base.UpstreamErr
			if detail == "" {
				detail = "upstream ref unavailable"
			}
			unknowns = append(unknowns, findings.Unknown{
				Substrate: Substrate,
				Message:   fmt.Sprintf("%d new migration version(s) could not be checked for collision with upstream", len(added)),
				Reason:    detail,
			})
		}
		return nil, unknowns, nil
	}

	var versions []string
	for v := range added {
		versions = append(versions, v)
	}
	sort.Strings(versions)

	for _, version := range versions {
		paths := added[version]
		upstreamPath, clash := base.UpstreamVersions[version]
		if !clash {
			confirmed = append(confirmed, findings.Confirmation{
				Substrate: Substrate,
				Rule:      "migration-version-unique",
				Message:   fmt.Sprintf("version %s is not present on %s", version, base.UpstreamRef),
				Anchor:    &findings.Anchor{Kind: "migration", ID: version},
			})
			continue
		}
		// The same path existing upstream is not a collision — that is the
		// same migration, and check 1 owns whether its contents drifted.
		if containsPath(paths, upstreamPath) {
			continue
		}
		out = append(out, findings.Finding{
			File:      paths[0],
			Rule:      "migration-version-collision",
			Substrate: Substrate,
			Category:  findings.CategorySchema,
			Severity:  findings.SeverityError,
			Message: fmt.Sprintf("new migration version %s already exists on %s as %s",
				version, base.UpstreamRef, upstreamPath),
			Anchor:   &findings.Anchor{Kind: "migration", ID: version},
			Evidence: evidence,
			Expected: fmt.Sprintf("a version prefix unused on %s", base.UpstreamRef),
			Observed: fmt.Sprintf("%s already taken by %s", version, upstreamPath),
			FixCmd:   "renumber this migration above the highest version upstream",
			Context:  "Once both branches are merged, golang-migrate sees two migrations claiming one version and refuses to run.",
		})
	}
	return out, unknowns, confirmed
}

// addedVersions returns version → added paths for migrations that this change
// introduces and the base does not have.
func addedVersions(base, head *Set) map[string][]string {
	baseVersions := base.versions()
	out := map[string][]string{}
	for version, paths := range head.versions() {
		if _, existed := baseVersions[version]; existed {
			continue
		}
		out[version] = paths
	}
	return out
}

func (p *Pane) unchangedExisting(base, head *Set) []string {
	var out []string
	for path, sha := range base.Files {
		if head.Files[path] == sha {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// render is section 1: the change in the domain where it lives — which
// migration versions this branch adds, and which pre-existing ones it touches.
func (p *Pane) render(base, head *Set, added map[string][]string) pane.Render {
	var versions []string
	for v := range added {
		versions = append(versions, v)
	}
	sort.Strings(versions)

	lines := make([]string, 0, len(versions)+2)
	for _, v := range versions {
		lines = append(lines, fmt.Sprintf("+ version %s — %s", v, strings.Join(added[v], ", ")))
	}
	var touched []string
	for path := range base.Files {
		if sha, ok := head.Files[path]; !ok || sha != base.Files[path] {
			_ = sha
			touched = append(touched, path)
		}
	}
	sort.Strings(touched)
	for _, path := range touched {
		lines = append(lines, fmt.Sprintf("~ %s (existed at base)", path))
	}

	summary := fmt.Sprintf("%d migration version(s) added, %d pre-existing file(s) touched", len(versions), len(touched))
	return pane.Render{Title: "Migrations", Summary: summary, Lines: lines}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
