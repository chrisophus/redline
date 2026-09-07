// Package findings is Redline's wire format. It is not a new schema: it is an
// independent reimplementation of doctor's JSON shape (gorefactor/doctor,
// SchemaVersion 1), extended additively. Shared contract, not shared internals.
package findings

import (
	"sort"

	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/mutation"
)

// SchemaVersion guards the JSON encoding of Report. Kept in lockstep with
// doctor's constant of the same name.
const SchemaVersion = 1

// Severity of a finding.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Category classifies a finding. Redline adds its own categories rather than
// overloading doctor's — doctor's "api" means undeclared Go exported-API
// changes, which is a different thing from an HTTP contract change.
type Category string

const (
	CategorySchema   Category = "schema"   // migrations, database structure
	CategoryContract Category = "contract" // HTTP/OpenAPI contract
	CategoryCover    Category = "cover"    // diff coverage
	CategoryUI       Category = "ui"       // rendered interface
	CategoryLint     Category = "lint"     // lint delta, suppressions, lint config
	CategoryReview   Category = "review"   // agent review comment
)

// DefaultSeverity derives a finding's severity from its category. Redline's
// categories all describe changes that alter a contract with something outside
// the diff, so they default to error; a pane may demote per finding.
func (c Category) DefaultSeverity() Severity {
	switch c {
	case CategorySchema, CategoryContract:
		return SeverityError
	case CategoryReview:
		return SeverityInfo
	default:
		return SeverityWarning
	}
}

// Source names where a finding came from. Every pane Redline ships is
// deterministic; the field stays in the wire format so a reader of
// findings.json can see that stated rather than assumed.
type Source string

const (
	SourceDeterministic Source = "deterministic"
	SourceLLM           Source = "llm"
)

// Anchor is a pane-relative location, for findings that have no file:line.
// Kind names the pane's identity scheme ("migration", "table.column",
// "method path"); ID is the value. Anchor identity must be stable across runs
// because the fingerprint is the join key for human comment state.
type Anchor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Key returns the anchor's contribution to a fingerprint.
func (a *Anchor) Key() string {
	if a == nil {
		return ""
	}
	return a.Kind + ":" + a.ID
}

// Finding is one diagnostic from one pane. Fields through Context mirror
// doctor's Finding; the rest are Redline's additive extension.
type Finding struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// StartLine is the first line of a ranged comment. Zero means the comment
	// anchors on Line alone. When set it must be less than or equal to Line.
	StartLine   int      `json:"startLine,omitempty"`
	Rule        string   `json:"rule"`
	Substrate   string   `json:"substrate"`
	Category    Category `json:"category"`
	Severity    Severity `json:"severity"`
	Message     string   `json:"message"`
	New         bool     `json:"new"`
	Fingerprint string   `json:"fingerprint"`
	FixCmd      string   `json:"fixCmd,omitempty"`
	// Suggestion is a literal single-file replacement for the anchored lines,
	// rendered as a GitHub suggestion block when short enough. Empty means no
	// proposed fix; a suggestion that would need more than one file is refused
	// rather than emitted wrong.
	Suggestion string `json:"suggestion,omitempty"`
	Context    string `json:"context,omitempty"`

	Anchor   *Anchor  `json:"anchor,omitempty"`   // pane-relative location; file:line often absent
	Evidence []string `json:"evidence,omitempty"` // observation IDs backing the claim
	Expected string   `json:"expected,omitempty"` // for surprise ranking
	Observed string   `json:"observed,omitempty"`
	Source   Source   `json:"source,omitempty"`
	// Verdict is the agent's judgment of this finding, merged from review.json
	// after fingerprints are stamped. Nil until an agent has ruled on it.
	Verdict *Verdict `json:"verdict,omitempty"`
}

// Verdict is an agent's judgment of a finding, ingested from review.json and
// joined by fingerprint. Redline never fills it: the instrument states facts,
// the agent supplies the reading, so Source is always "llm". Ruling is a short
// controlled word per finding class (justified / should-fix / rule-noisy for a
// suppression); Rationale is one line of why.
type Verdict struct {
	Ruling    string `json:"ruling"`
	Rationale string `json:"rationale,omitempty"`
	// Fix is the agent's recommendation for how to resolve the finding, one or
	// two lines. Empty when the ruling stands on its own.
	Fix    string `json:"fix,omitempty"`
	Source Source `json:"source"`
}

// SubstrateState records whether a pane produced findings this run.
type SubstrateState string

const (
	SubstrateRan     SubstrateState = "ran"
	SubstrateSkipped SubstrateState = "skipped" // the repository has such files; this change touched none
	SubstrateFailed  SubstrateState = "failed"  // started and errored
	// SubstrateNotApplicable is a pane the repository has no files for at all:
	// no migrations, no API spec, no linter configured. It is recorded here so
	// findings.json says the pane exists, and rendered nowhere. A report on a
	// repository with no API should not mention the API; naming an absence
	// the reader already knows about is noise, and worse, it makes the panes
	// that did apply and failed harder to see.
	SubstrateNotApplicable SubstrateState = "not-applicable"
)

// SubstrateStatus is the per-pane availability record. Redline is non-gating,
// so Gating is always false and GateOK does not apply — but the rendering
// obligation is the same: a pane that did not run renders as "did not run".
type SubstrateStatus struct {
	Name   string         `json:"name"`
	State  SubstrateState `json:"state"`
	Detail string         `json:"detail,omitempty"`
	Gating bool           `json:"gating"`
}

// Confirmation is a check that ran and came back clean. Confirmations are the
// deliverable, not suppressed noise: each is a question the reviewer no longer
// has to ask. They never emit findings and are rendered collapsed.
type Confirmation struct {
	Substrate string  `json:"substrate"`
	Rule      string  `json:"rule"`
	Message   string  `json:"message"`
	Anchor    *Anchor `json:"anchor,omitempty"`
}

// Unknown records something a pane could not determine, and why. Section 3 of
// the report: a report that silently covers 60% of a change reads exactly like
// one that covers all of it.
type Unknown struct {
	Substrate string `json:"substrate"`
	Message   string `json:"message"`
	Reason    string `json:"reason,omitempty"`
}

// Coverage records how much of the change Redline actually examined. It is
// the answer to the question a reader of an empty report cannot otherwise ask:
// did every check come back clean, or did nothing look? Without it a report
// covering none of a change is indistinguishable from one covering all of it.
type Coverage struct {
	ChangedFiles  int      `json:"changedFiles"`
	ExaminedFiles int      `json:"examinedFiles"`
	Unexamined    []string `json:"unexamined,omitempty"`

	// Generated are paths dropped from the change as machine output before any
	// pane ran. They are listed rather than merely counted: excluding a file a
	// human actually wrote is the one way this feature can hide a real change,
	// and naming every exclusion is what makes that recoverable.
	Generated []string `json:"generated,omitempty"`

	// CoverableFiles counts the changed files a coverage profile could
	// describe (Go files, until the pane reads other formats). Zero means the
	// coverage number does not apply to this change, and the report leaves
	// coverage out entirely rather than reporting a profile as missing for a
	// change no profile could ever cover.
	CoverableFiles int `json:"coverableFiles"`

	// Diff is the share of added lines a test profile shows executed. Nil when
	// no profile was found, which must render as "nobody knows" rather than as
	// zero per cent — those are very different claims. Only meaningful when
	// CoverableFiles is non-zero.
	Diff *cover.Result `json:"diffCoverage,omitempty"`
}

// CoverageApplies reports whether the coverage number has anything to say
// about this change: a profile was read, or a changed file could have been
// covered by one. When false, coverage is left off the report.
func (c Coverage) CoverageApplies() bool {
	return c.Diff != nil || c.CoverableFiles > 0
}

// Report is the merged result of one Redline run.
type Report struct {
	SchemaVersion int               `json:"schemaVersion"`
	BaseRef       string            `json:"baseRef"`
	BaseSHA       string            `json:"baseSHA,omitempty"`
	Scope         []string          `json:"scope,omitempty"`
	Findings      []Finding         `json:"findings"`
	Substrates    []SubstrateStatus `json:"substrates"`
	NewCount      map[Severity]int  `json:"newCount"`
	Coverage      Coverage          `json:"coverage"`

	Confirmations []Confirmation `json:"confirmations,omitempty"`
	Unknowns      []Unknown      `json:"unknowns,omitempty"`

	// Agent is the review narrative the agent wrote, read from review.json: an
	// overview of the change and a per-file summary, rendered as the agent's.
	Agent *AgentReview `json:"agent,omitempty"`

	// Mutation is the diff-scoped result of a gomutants report, when one is on
	// disk: of the mutants on the lines this change adds, which a test killed and
	// which lived. A lived mutant is a line a test runs but nothing asserts. Nil
	// when no report was found.
	Mutation *mutation.Result `json:"mutation,omitempty"`
}

// AgentReview is the agent's prose layer over a change, ingested from
// review.json. The comments in that file become findings; this holds the parts
// that have no single line to sit on.
type AgentReview struct {
	Overview string            `json:"overview,omitempty"`
	Files    map[string]string `json:"files,omitempty"`
}

// FailedSubstrates returns panes that applied but did not run. Redline is
// non-gating, so this drives rendering, not exit status.
func (r *Report) FailedSubstrates() []SubstrateStatus {
	var out []SubstrateStatus
	for _, s := range r.Substrates {
		if s.State == SubstrateFailed {
			out = append(out, s)
		}
	}
	return out
}

// Finalize stamps fingerprints, fills defaults and recomputes NewCount.
// Every Redline pane is diff-based by construction — its findings are relative
// to the base — so New is always true and no baseline build is needed.
func (r *Report) Finalize() {
	r.SchemaVersion = SchemaVersion
	r.NewCount = map[Severity]int{}
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.Severity == "" {
			f.Severity = f.Category.DefaultSeverity()
		}
		if f.Source == "" {
			f.Source = SourceDeterministic
		}
		f.New = true
		f.Fingerprint = Fingerprint(*f)
		r.NewCount[f.Severity]++
	}
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	if r.Substrates == nil {
		r.Substrates = []SubstrateStatus{}
	}
}

// Sort orders by severity, then substrate, then rule, then location, so
// output is stable across runs. Surprise ranking decides emphasis within
// the report; it never decides inclusion.
func Sort(fs []Finding) {
	rank := map[Severity]int{SeverityError: 0, SeverityWarning: 1, SeverityInfo: 2}
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if rank[a.Severity] != rank[b.Severity] {
			return rank[a.Severity] < rank[b.Severity]
		}
		if a.Substrate != b.Substrate {
			return a.Substrate < b.Substrate
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.File < b.File
	})
}
