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
	// APIChanges and SchemaChanges are the agent's reading of the contract
	// surface this change moves, highlighted at the top of the report.
	APIChanges    []Highlight `json:"apiChanges,omitempty"`
	SchemaChanges []Highlight `json:"schemaChanges,omitempty"`
	Findings      []Judgment  `json:"findings,omitempty"`
	// Unknowns are what the agent could not determine. Reported in the same
	// section as Redline's own gaps, for the same reason.
	Unknowns []string `json:"unknowns,omitempty"`
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
