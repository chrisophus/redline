// Package pane defines the shape every Redline check family shares: observe
// the system at two revisions, diff the observations, emit findings.
package pane

import "github.com/ccason/redline/internal/findings"

// Revision names a side of the comparison. The working tree is a revision.
type Revision struct {
	Name string // "base" | "worktree"
	Rev  string // commit SHA, empty for the working tree
}

// Worktree is the revision constant for the uncommitted working tree.
var Worktree = Revision{Name: "worktree"}

// Observation is one pane's view of the system at one revision. Panes that
// need a running system produce theirs by standing it up; panes that do not
// produce theirs by static extraction. The interface does not change.
type Observation interface {
	// ID identifies this observation for the Evidence field of a finding.
	ID() string
}

// Render is a pane's contribution to section 1 of the report: the change,
// shown in the domain where it lives.
type Render struct {
	Title   string
	Summary string
	Lines   []string
}

// Result is everything a pane produces in one run.
type Result struct {
	Render        Render
	Findings      []findings.Finding
	Confirmations []findings.Confirmation
	Unknowns      []findings.Unknown

	// Evidence maps an observation ID to the artifact backing it — the SQL a
	// migration edit actually changed, a schema dump, a captured response.
	// Findings reference these by ID. A finding a reviewer cannot check for
	// themselves is inference wearing evidence's clothes.
	Evidence map[string]Artifact
}

// Artifact is one piece of captured evidence, persisted under .redline/ and
// inlined into the report when it is small enough to read in place.
type Artifact struct {
	Kind    string // "diff" | "sql" | "text"
	Content string
}

// Pane is the check-family interface. Observe is the expensive,
// side-effectful half; Diff is pure.
//
// Scope returns the subset of the changed paths this pane examines, rather
// than a bare "does it apply". The difference matters: the union of every
// pane's scope is how much of the change Redline actually looked at, and a
// report that cannot state that number reads identically whether it covered
// the whole change or none of it.
type Pane interface {
	Name() string
	Scope(changed []string) []string
	Observe(rev Revision) (Observation, error)
	Diff(before, after Observation) (Result, error)
}
