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
	// reviewer reads first.
	Summary string `json:"summary"`
	// Actual is what the change does, as read from the code. Stated intent
	// comes from the pull request body Redline already fetched; Actual is the
	// half the agent must supply so the posted review can name the gap.
	Actual string `json:"actual,omitempty"`
	// Discrepancies are places the change does not do what it claims. Each
	// one should also appear in findings (category intent) so it can ride as
	// a line comment; Rule joins the body entry to that finding for threading.
	Discrepancies []Discrepancy `json:"discrepancies,omitempty"`
	// Intent is why the change exists: the ticket, the pull request, and
	// whether this is the right thing built the right way. It is the first
	// thing a reviewer reads and the last thing a diff can tell them.
	Intent *Intent `json:"intent,omitempty"`
	// Surfaces is one line per contract surface — what moved on the
	// interface, the API, and the schema. The tile copy, not the evidence:
	// APIChanges, SchemaChanges and Screenshots remain the drill-in.
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

// Discrepancy is one place the change diverges from what it said it would do.
// Claim is the words from the PR (or ticket); Actual is what the code does.
type Discrepancy struct {
	Claim  string `json:"claim"`
	Actual string `json:"actual"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	// StartLine begins a ranged anchor when the claim spans more than one line.
	StartLine int `json:"startLine,omitempty"`
	// Rule joins this entry to the finding that carries it inline. When empty,
	// ToFindings invents one from the claim so the body can still thread it.
	Rule string `json:"rule,omitempty"`
}

// Intent is the orientation block. Every field is optional because the screen
// opens at two moments: before a push, when there is no pull request and
// possibly no ticket, and on an open pull request, when there is both.
type Intent struct {
	Ticket *IntentTicket `json:"ticket,omitempty"`
	// PR is what the agent knows about the pull request. It is only shown when
	// Redline was not pointed at one itself: a `--pr` run fetched the real
	// thing, and preferring hearsay over it would contradict the target named
	// at the top of the screen. A disagreement is not an error — failing an
	// ingest over a mismatched title would be absurd.
	PR  *IntentPR  `json:"pr,omitempty"`
	Fit *IntentFit `json:"fit,omitempty"`
}

// IntentTicket is the ticket, as the agent read it. Redline fetches nothing:
// it has no ticket-host client and no credentials, and a URL here is stored,
// never requested.
type IntentTicket struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// IntentPR identifies a pull request.
type IntentPR struct {
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
}

// IntentFit is the judgment a diff cannot carry: is this the thing the ticket
// asked for, and is this the way to build it. Both are the agent's words and
// are labelled as such.
type IntentFit struct {
	Thing string `json:"thing,omitempty"`
	Way   string `json:"way,omitempty"`
}

// Surfaces is the three contract surfaces a reviewer checks, keyed rather than
// mapped so a stray key cannot silently bind to nothing.
type Surfaces struct {
	Interface *Surface `json:"interface,omitempty"`
	API       *Surface `json:"api,omitempty"`
	Schema    *Surface `json:"schema,omitempty"`
}

// Surface is one line about one surface. Moved is explicit rather than
// inferred from a non-empty line, so "did not move" can still carry a sentence
// explaining what was looked at.
type Surface struct {
	Line  string `json:"line,omitempty"`
	Moved bool   `json:"moved"`
}

func (i *Intent) empty() bool {
	return i == nil || (i.Ticket.empty() && i.PR.empty() && i.Fit.empty())
}

func (t *IntentTicket) empty() bool {
	return t == nil || (t.ID == "" && t.Title == "" && t.URL == "")
}

func (p *IntentPR) empty() bool {
	return p == nil || (p.Number == 0 && p.Title == "" && p.URL == "")
}

func (f *IntentFit) empty() bool {
	return f == nil || (f.Thing == "" && f.Way == "")
}

func (s *Surfaces) empty() bool {
	return s == nil || (s.Interface.empty() && s.API.empty() && s.Schema.empty())
}

// empty is false for idle copy: a surface that did not move but says why is
// worth keeping, while a bare `{"moved": false}` carries nothing and must not
// overwrite a previous line.
func (s *Surface) empty() bool {
	return s == nil || (s.Line == "" && !s.Moved)
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

// Judgment is one agent-authored finding, before it becomes a Finding.
type Judgment struct {
	File      string            `json:"file"`
	Line      int               `json:"line,omitempty"`
	StartLine int               `json:"startLine,omitempty"`
	Rule      string            `json:"rule"`
	Category  findings.Category `json:"category,omitempty"`
	Severity  findings.Severity `json:"severity,omitempty"`
	Message   string            `json:"message"`
	Context   string            `json:"context,omitempty"`
	FixCmd    string            `json:"fix,omitempty"`
	// Suggestion is a literal replacement for the anchored lines, single file,
	// at most twenty lines. Longer or multi-file fixes are omitted rather than
	// posted wrong; say what to do in context instead.
	Suggestion string `json:"suggestion,omitempty"`
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
	if out.Actual == "" {
		out.Actual = prev.Actual
	}
	if len(out.Discrepancies) == 0 {
		out.Discrepancies = prev.Discrepancies
	}
	// Whole-object replace, the same rule Files follows. A supplied intent is
	// the agent's current account and wins entirely; merging it key by key
	// would leave a stale ticket attached to a new pull request.
	if out.Intent.empty() {
		out.Intent = prev.Intent
	}
	if out.Surfaces.empty() {
		out.Surfaces = prev.Surfaces
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
	return &out
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
//
// Discrepancies become category-intent findings too, so they are countable and
// can ride as line comments. A discrepancy whose Rule already appears in
// findings is left as narrative only — the finding carries the inline comment.
func (r *Review) ToFindings() []findings.Finding {
	out := make([]findings.Finding, 0, len(r.Findings)+len(r.Discrepancies))
	seenRule := map[string]bool{}
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
			File:       j.File,
			Line:       j.Line,
			StartLine:  j.StartLine,
			Rule:       j.Rule,
			Substrate:  Substrate,
			Category:   category,
			Severity:   severity,
			Message:    j.Message,
			Context:    context,
			FixCmd:     j.FixCmd,
			Suggestion: clipSuggestion(j.Suggestion),
			Source:     findings.SourceLLM,
		})
		if j.Rule != "" {
			seenRule[j.Rule] = true
		}
	}
	for _, d := range r.Discrepancies {
		rule := strings.TrimSpace(d.Rule)
		if rule == "" {
			rule = "intent-drift"
		}
		if seenRule[rule] {
			continue
		}
		msg := strings.TrimSpace(d.Claim)
		if msg == "" {
			msg = "the change does not match its stated intent"
		}
		out = append(out, findings.Finding{
			File:      d.File,
			Line:      d.Line,
			StartLine: d.StartLine,
			Rule:      rule,
			Substrate: Substrate,
			Category:  findings.CategoryIntent,
			Severity:  findings.SeverityWarning,
			Message:   msg,
			Context:   strings.TrimSpace(d.Actual),
			Source:    findings.SourceLLM,
		})
	}
	return out
}

// maxSuggestionLines is Copilot's FixGeneratorMaxExpansionLines. Longer
// replacements are dropped: a wrong fix is worse than no fix.
const maxSuggestionLines = 20

// clipSuggestion keeps a single-file suggestion that fits the GitHub block
// budget and drops anything longer.
func clipSuggestion(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	if strings.Count(s, "\n")+1 > maxSuggestionLines {
		return ""
	}
	return s
}
