package review

import (
	"fmt"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
)

// PromptPart is one part of a review request and its estimated size.
type PromptPart struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

// promptParts sizes each part of the request Assemble built, so a reader can
// see what a large request is large with. The diff and the findings cannot be
// dropped and the context competes for what they leave, and the one-line total
// says neither.
func promptParts(in Input, opts Options, system string, budget envelope.Budgeted) []PromptPart {
	context := 0
	if ctx := budget.Render(); ctx != "" {
		context = envelope.EstimateTokens(in.contextHeader(keptRoles(budget)) + ctx + "\n")
	}
	return []PromptPart{
		{"system", envelope.EstimateTokens(system)},
		{"tools", toolsTokens()},
		{"description", envelope.EstimateTokens(in.changeSection())},
		// Not the findings: those are not sent. This is which linters ran and
		// what no check determined.
		{"checks", envelope.EstimateTokens(in.priorsSection())},
		{"threads", envelope.EstimateTokens(in.heardSection())},
		{"coverage", envelope.EstimateTokens(in.coverageSection())},
		{"missing context", envelope.EstimateTokens(in.absentSection())},
		{"diff", envelope.EstimateTokens(in.diffSection())},
		{"context", context},
		// What this call is told to do, as against the material it reads. It
		// is sized apart because it is the part a reader can change: the
		// describing call and the judging call send the same packet and differ
		// only here.
		{"this pass", envelope.EstimateTokens(judgingTail + describingTail + callsBlock(StageReview))},
		{"note", envelope.EstimateTokens(in.noteTail())},
	}
}

// PartsLine renders Parts for the terminal, leaving out the empty parts and
// always naming the context beside the room it had.
func (r *Result) PartsLine() string {
	var out []string
	for _, p := range r.Parts {
		switch {
		case p.Name == "context":
			out = append(out, fmt.Sprintf("context %s of %s room", formatTokens(p.Tokens), formatTokens(r.ContextRoom)))
		case p.Tokens > 0:
			out = append(out, p.Name+" "+formatTokens(p.Tokens))
		}
	}
	return strings.Join(out, ", ")
}
