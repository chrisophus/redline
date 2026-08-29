// Package findings is Redline's wire format. It is not a new schema: it is an
// independent reimplementation of doctor's JSON shape (gorefactor/doctor,
// SchemaVersion 1), extended additively. Shared contract, not shared internals.
package findings

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
	CategoryReview   Category = "review"   // agent-authored judgment with no more specific category
)

// DefaultSeverity derives a finding's severity from its category. Redline's
// categories all describe changes that alter a contract with something outside
// the diff, so they default to error; a pane may demote per finding.
func (c Category) DefaultSeverity() Severity {
	switch c {
	case CategorySchema, CategoryContract:
		return SeverityError
	default:
		return SeverityWarning
	}
}

// Source distinguishes reproducible findings from the LLM pass. Load-bearing:
// without it the determinism guarantee is invisible at the point of use.
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
	File        string   `json:"file,omitempty"`
	Line        int      `json:"line,omitempty"`
	Rule        string   `json:"rule"`
	Substrate   string   `json:"substrate"`
	Category    Category `json:"category"`
	Severity    Severity `json:"severity"`
	Message     string   `json:"message"`
	New         bool     `json:"new"`
	Fingerprint string   `json:"fingerprint"`
	FixCmd      string   `json:"fixCmd,omitempty"`
	Context     string   `json:"context,omitempty"`

	Anchor   *Anchor  `json:"anchor,omitempty"`   // pane-relative location; file:line often absent
	Evidence []string `json:"evidence,omitempty"` // observation IDs backing the claim
	Expected string   `json:"expected,omitempty"` // for surprise ranking
	Observed string   `json:"observed,omitempty"`
	Source   Source   `json:"source,omitempty"`
}

// SubstrateState records whether a pane produced findings this run.
type SubstrateState string

const (
	SubstrateRan     SubstrateState = "ran"
	SubstrateSkipped SubstrateState = "skipped" // did not apply, or could not run
	SubstrateFailed  SubstrateState = "failed"  // started and errored
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
}

// DarkSubstrates returns panes that applied but did not run. Redline is
// non-gating, so this drives rendering, not exit status.
func (r *Report) DarkSubstrates() []SubstrateStatus {
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
