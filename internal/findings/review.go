package findings

import (
	"encoding/json"
	"os"
)

// Review is the agent's layer over a run, read from review.json. The agent reads
// findings.json, writes this file, and Redline merges it back on the next run.
// Redline never writes it, the way it never writes comments.json: it is the
// reviewer's own state, and a re-run must not clobber it.
//
// Overview and Files are prose about the change. Comments are line remarks that
// become findings marked source llm. Verdicts are judgments keyed by a finding's
// fingerprint. Redline composes none of it.
type Review struct {
	Overview string             `json:"overview,omitempty"`
	Files    map[string]string  `json:"files,omitempty"`
	Comments []ReviewComment    `json:"comments,omitempty"`
	Verdicts map[string]Verdict `json:"verdicts,omitempty"`
}

// ReviewComment is one remark the agent left on a line, shaped like a GitHub
// review comment. It becomes a finding so it renders on the diff line in the
// drawer beside Redline's own findings.
type ReviewComment struct {
	File      string   `json:"file"`
	Line      int      `json:"line,omitempty"`
	StartLine int      `json:"startLine,omitempty"`
	Severity  Severity `json:"severity,omitempty"`
	Body      string   `json:"body"`
}

// LoadReview reads a review file. A missing file is not an error: most runs have
// no review yet. Every verdict and comment reads as source "llm" regardless of
// what the file claims: Redline attributes the reading, the file does not.
func LoadReview(path string) (*Review, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Review
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	for k, v := range r.Verdicts {
		v.Source = SourceLLM
		r.Verdicts[k] = v
	}
	return &r, nil
}

// CommentFindings turns the agent's line comments into findings so they carry a
// fingerprint, sit on their line in the drawer, and render beside the observed
// findings. Call before Finalize, which stamps the fingerprints. A comment with
// no body is dropped: it has nothing to say and nothing to key on.
func (r *Review) CommentFindings() []Finding {
	if r == nil {
		return nil
	}
	out := make([]Finding, 0, len(r.Comments))
	for _, c := range r.Comments {
		if c.Body == "" {
			continue
		}
		sev := c.Severity
		if sev == "" {
			sev = CategoryReview.DefaultSeverity()
		}
		out = append(out, Finding{
			File:      c.File,
			Line:      c.Line,
			StartLine: c.StartLine,
			Rule:      "agent-comment",
			Substrate: "redline/review",
			Category:  CategoryReview,
			Severity:  sev,
			Message:   c.Body,
			Source:    SourceLLM,
		})
	}
	return out
}

// MergeVerdicts attaches verdicts to findings by fingerprint. Call after
// Finalize, which stamps the fingerprints the verdicts key on. A verdict whose
// fingerprint matches no finding is dropped: it named a finding this run does
// not have, which the agent can see for itself in the report.
func (r *Report) MergeVerdicts(verdicts map[string]Verdict) {
	if len(verdicts) == 0 {
		return
	}
	for i := range r.Findings {
		if v, ok := verdicts[r.Findings[i].Fingerprint]; ok {
			vv := v
			r.Findings[i].Verdict = &vv
		}
	}
}
