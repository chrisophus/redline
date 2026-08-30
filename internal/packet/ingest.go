package packet

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ccason/redline/internal/findings"
)

// Review is what the agent returns: judgments about the change, plus the
// plain-language framing Redline deliberately does not own.
type Review struct {
	// Summary is one or two sentences: what this change does, in the domain
	// it lives in. Redline emits evidence; the agent supplies the sentence a
	// reviewer reads first. It stays the briefing headline; Intent and
	// Surfaces frame it, they do not replace it.
	Summary string `json:"summary"`
	// Intent is why the change exists: the pull request and ticket the
	// briefing can open. Both links are optional and agent-supplied; Redline
	// never fetches them.
	Intent *Intent `json:"intent,omitempty"`
	// Surfaces is one line per product surface — Interface, API, Schema — the
	// tiles the opening briefing reads across. A surface may carry a quiet
	// line and report Moved false; that is "did not move," not absent.
	Surfaces *Surfaces `json:"surfaces,omitempty"`
	// Files is the walkthrough: one sentence per changed path, what that file
	// does in this change. The report leads with this even when no pane ran.
	Files []FileNote `json:"files,omitempty"`
	// APIChanges and SchemaChanges are the agent's reading of the contract
	// surface this change moves, highlighted at the top of the report.
	APIChanges    []Highlight `json:"apiChanges,omitempty"`
	SchemaChanges []Highlight `json:"schemaChanges,omitempty"`
	Findings      []Judgment  `json:"findings,omitempty"`
	// Unknowns are what the agent could not determine. Reported in the same
	// section as Redline's own gaps, for the same reason.
	Unknowns []string `json:"unknowns,omitempty"`
	// Screenshots are routes the agent walked. They are not Redline's UI pane
	// (checks 15–16); they are labelled as agent work when the report renders.
	Screenshots []Shot `json:"screenshots,omitempty"`
}

// Shot is one captured page from an agent UI walk.
type Shot struct {
	Route   string `json:"route"`
	Path    string `json:"path"`             // current (or after) image on disk
	Before  string `json:"before,omitempty"` // optional before image
	Caption string `json:"caption,omitempty"`
}

// FileNote is the agent's one-sentence account of a changed file.
type FileNote struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

// Highlight is a called-out contract change.
type Highlight struct {
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	File     string `json:"file,omitempty"`
	Breaking bool   `json:"breaking,omitempty"`
}

// Intent is the agent's framing for why the change exists: the pull request
// and the ticket a reviewer can open. Both are optional. The PR here is an
// overlay on any --pr target Redline already resolved; Redline stores these
// links and never fetches them.
type Intent struct {
	PR     *IntentPR     `json:"pr,omitempty"`
	Ticket *IntentTicket `json:"ticket,omitempty"`
}

// IntentPR is the pull request the change lives on. Number matches
// target.PullRequest.Number; all fields are optional so a bare link parses.
type IntentPR struct {
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
}

// IntentTicket is the tracker item the change answers to. ID is a string
// (e.g. REL-24), not a number; Redline does not fetch the ticket host.
type IntentTicket struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Surfaces carries one line per product surface the briefing reads across.
// The keys are fixed — Interface, API, Schema — so a surface cannot bind to
// an unexpected name.
type Surfaces struct {
	Interface *Surface `json:"interface,omitempty"`
	API       *Surface `json:"api,omitempty"`
	Schema    *Surface `json:"schema,omitempty"`
}

// Surface is one tile's one-liner. Moved carries no omitempty: a surface that
// did not move reports Moved false explicitly, and a quiet Line beside it is
// still real content, not an absence.
type Surface struct {
	Line  string `json:"line,omitempty"`
	Moved bool   `json:"moved"`
}

// Judgment is one agent-authored finding, before it becomes a Finding.
type Judgment struct {
	File     string            `json:"file"`
	Line     int               `json:"line,omitempty"`
	Rule     string            `json:"rule"`
	Category findings.Category `json:"category,omitempty"`
	Severity findings.Severity `json:"severity,omitempty"`
	Message  string            `json:"message"`
	Context  string            `json:"context,omitempty"`
	FixCmd   string            `json:"fix,omitempty"`
	// Confidence lets the agent say it is unsure rather than either
	// suppressing a real concern or asserting a shaky one.
	Confidence string `json:"confidence,omitempty"` // high | medium | low
	// Instruction cites the repository instruction file a finding rests on,
	// so house-rule findings are traceable to the rule.
	Instruction string `json:"instruction,omitempty"`
}

// ParseReview reads a review from JSON, tolerating a fenced code block since
// that is how a model most often emits one.
func ParseReview(r io.Reader) (*Review, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil, fmt.Errorf("empty review; expected findings JSON on stdin")
	}
	if i := strings.Index(text, "```"); i >= 0 {
		rest := text[i+3:]
		if j := strings.Index(rest, "\n"); j >= 0 {
			rest = rest[j+1:]
		}
		if k := strings.LastIndex(rest, "```"); k >= 0 {
			text = strings.TrimSpace(rest[:k])
		}
	}
	var rev Review
	if err := json.Unmarshal([]byte(text), &rev); err != nil {
		return nil, fmt.Errorf("review is not valid JSON: %w", err)
	}
	return &rev, nil
}

// Merge layers a new review onto the one already recorded for this session.
// The reviewer-comments loop invites a second ingest carrying only the
// findings that changed; the summary, the file walkthrough, and the contract
// highlights live nowhere but here, so an omitted field must keep its
// previous value rather than blank the section the report leads with. An
// explicitly supplied field always wins.
func Merge(prev, next *Review) *Review {
	if next == nil {
		return prev
	}
	if prev == nil {
		return next
	}
	out := *next
	if out.Summary == "" {
		out.Summary = prev.Summary
	}
	if len(out.Files) == 0 {
		out.Files = prev.Files
	}
	if len(out.APIChanges) == 0 {
		out.APIChanges = prev.APIChanges
	}
	if len(out.SchemaChanges) == 0 {
		out.SchemaChanges = prev.SchemaChanges
	}
	if len(out.Screenshots) == 0 {
		out.Screenshots = prev.Screenshots
	}
	if len(out.Unknowns) == 0 {
		out.Unknowns = prev.Unknowns
	}
	if intentEmpty(out.Intent) {
		out.Intent = prev.Intent
	}
	if surfacesEmpty(out.Surfaces) {
		out.Surfaces = prev.Surfaces
	}
	return &out
}

// intentEmpty reports whether an Intent carries no briefing link, so an
// omitted or empty intent keeps the previous one rather than blanking it.
func intentEmpty(i *Intent) bool {
	return i == nil || (intentPREmpty(i.PR) && intentTicketEmpty(i.Ticket))
}

func intentPREmpty(pr *IntentPR) bool {
	return pr == nil || (pr.Number == 0 && pr.Title == "" && pr.URL == "")
}

func intentTicketEmpty(t *IntentTicket) bool {
	return t == nil || (t.ID == "" && t.Title == "" && t.URL == "")
}

// surfacesEmpty reports whether a Surfaces carries no tile content. A surface
// with a quiet Line, or one reporting Moved true, is not empty; only an
// absent or Moved-false-with-no-line surface counts as empty.
func surfacesEmpty(s *Surfaces) bool {
	return s == nil || (surfaceEmpty(s.Interface) && surfaceEmpty(s.API) && surfaceEmpty(s.Schema))
}

func surfaceEmpty(s *Surface) bool {
	return s == nil || (s.Line == "" && !s.Moved)
}

// Apply merges an agent's review into a report: LLM findings and unknowns
// are appended, fingerprints stamped, duplicates dropped, then sorted so
// a high-severity judgment is not buried under an earlier info finding.
func Apply(rep *findings.Report, rev *Review) {
	if rev == nil {
		return
	}
	rep.Findings = append(rep.Findings, rev.ToFindings()...)
	seen := map[string]bool{}
	for _, u := range rep.Unknowns {
		seen[u.Substrate+"\x00"+u.Message] = true
	}
	for _, msg := range rev.Unknowns {
		key := Substrate + "\x00" + msg
		if seen[key] {
			continue
		}
		seen[key] = true
		rep.Unknowns = append(rep.Unknowns, findings.Unknown{
			Substrate: Substrate, Message: msg, Reason: "reported by the reviewing agent",
		})
	}
	rep.Finalize()
	rep.Dedupe()
	findings.Sort(rep.Findings)
}

// Substrate is the pane name agent findings are recorded under. They are a
// substrate like any other, so a reviewer sees them ranked alongside observed
// findings — but never mistakes one for the other.
const Substrate = "redline/review"

// ToFindings converts judgments into schema findings, stamping every one as
// LLM-sourced. Nothing else in Redline may set this field to "llm".
func (r *Review) ToFindings() []findings.Finding {
	out := make([]findings.Finding, 0, len(r.Findings))
	for _, j := range r.Findings {
		category := j.Category
		if category == "" {
			category = findings.CategoryReview
		}
		severity := j.Severity
		if severity == "" {
			severity = findings.SeverityWarning
		}
		context := j.Context
		if j.Instruction != "" {
			context = strings.TrimSpace(context + "\n\nHouse rule: " + j.Instruction)
		}
		if j.Confidence != "" && j.Confidence != "high" {
			context = strings.TrimSpace(context + fmt.Sprintf("\n\nThe agent reported %s confidence in this finding.", j.Confidence))
		}
		out = append(out, findings.Finding{
			File:      j.File,
			Line:      j.Line,
			Rule:      j.Rule,
			Substrate: Substrate,
			Category:  category,
			Severity:  severity,
			Message:   j.Message,
			Context:   context,
			FixCmd:    j.FixCmd,
			Source:    findings.SourceLLM,
		})
	}
	return out
}
