package findings

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Review is the agent's layer over a run, read from review.json. The agent reads
// findings.json, writes this file, and Redline merges it back on the next run.
// Redline never writes it, the way it never writes comments.json: it is the
// reviewer's own state, and a re-run must not clobber it.
//
// Overview and Files are prose about the change. Comments are line remarks that
// become findings marked source llm. Verdicts are judgments keyed by a finding's
// fingerprint.
type Review struct {
	Overview string             `json:"overview,omitempty"`
	Files    map[string]string  `json:"files,omitempty"`
	Comments []ReviewComment    `json:"comments,omitempty"`
	Verdicts map[string]Verdict `json:"verdicts,omitempty"`
	// MutationVerdicts are judgments of surviving mutants, keyed by the
	// survivor's key (mutation.survived[].key in findings.json). Source llm.
	MutationVerdicts map[string]Verdict `json:"mutationVerdicts,omitempty"`
}

// ReviewComment is one remark the agent left on a line, shaped like a GitHub
// review comment. It becomes a finding so it renders on the diff line in the
// drawer beside Redline's own findings.
type ReviewComment struct {
	File      string   `json:"file"`
	Line      int      `json:"line,omitempty"`
	StartLine int      `json:"startLine,omitempty"`
	Side      string   `json:"side,omitempty"`
	Severity  Severity `json:"severity,omitempty"`
	Body      string   `json:"body"`
	// RelatedFindings are fingerprints from findings.json this comment
	// builds on. A comment that carries them is a correlation: it connects
	// facts the reviewer was given rather than restating one of them.
	RelatedFindings []string `json:"relatedFindings,omitempty"`
	// Confidence folds a weak remark away on the report without asking the
	// reviewer to withhold it.
	Confidence Confidence `json:"confidence,omitempty"`
	// Category overrides the default. Only "correlation" is accepted; every
	// other value falls back to the review default, because a reviewer
	// labelling its own remark as a schema measurement would put an opinion
	// in the place the report reserves for facts.
	Category Category `json:"category,omitempty"`
}

// reviewWire is the on-disk shape before aliases and flexible fields normalize.
type reviewWire struct {
	Overview         string             `json:"overview"`
	WhatItDoes       string             `json:"what_it_does"`
	Files            json.RawMessage    `json:"files"`
	Comments         json.RawMessage    `json:"comments"`
	Findings         json.RawMessage    `json:"findings"`
	Verdicts         map[string]Verdict `json:"verdicts"`
	MutationVerdicts map[string]Verdict `json:"mutationVerdicts"`
}

type reviewCommentWire struct {
	File            string   `json:"file"`
	Path            string   `json:"path"`
	Line            int      `json:"line"`
	StartLine       int      `json:"startLine"`
	Side            string   `json:"side"`
	Severity        string   `json:"severity"`
	Body            string   `json:"body"`
	RelatedFindings []string `json:"relatedFindings"`
	Related         []string `json:"related"`
	Confidence      string   `json:"confidence"`
	Category        string   `json:"category"`
}

type reviewFileEntry struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

// LoadReview reads a review file. A missing file is not an error: most runs have
// no review yet. Every verdict and comment reads as source "llm" regardless of
// what the file claims: Redline attributes the reading, the file does not.
//
// Aliases accepted for cross-tool compatibility: what_it_does for overview;
// findings for comments; path for file; high/medium/low severities.
func LoadReview(path string) (*Review, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var wire reviewWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	r := &Review{Verdicts: wire.Verdicts}
	r.MutationVerdicts = wire.MutationVerdicts
	r.Overview = strings.TrimSpace(wire.Overview)
	if r.Overview == "" {
		r.Overview = strings.TrimSpace(wire.WhatItDoes)
	}
	files, err := parseReviewFiles(wire.Files)
	if err != nil {
		return nil, fmt.Errorf("review files: %w", err)
	}
	r.Files = files
	comments, err := parseReviewComments(wire.Comments, wire.Findings)
	if err != nil {
		return nil, fmt.Errorf("review comments: %w", err)
	}
	r.Comments = comments
	for k, v := range r.Verdicts {
		v.Source = SourceLLM
		r.Verdicts[k] = v
	}
	for k, v := range r.MutationVerdicts {
		v.Source = SourceLLM
		r.MutationVerdicts[k] = v
	}
	return r, nil
}

func parseReviewFiles(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var asMap map[string]string
	if err := json.Unmarshal(raw, &asMap); err == nil {
		return asMap, nil
	}
	var asList []reviewFileEntry
	if err := json.Unmarshal(raw, &asList); err != nil {
		return nil, fmt.Errorf("want object or array of {path, summary}")
	}
	out := make(map[string]string, len(asList))
	for _, e := range asList {
		if e.Path == "" {
			continue
		}
		out[e.Path] = e.Summary
	}
	return out, nil
}

func parseReviewComments(commentsRaw, findingsRaw json.RawMessage) ([]ReviewComment, error) {
	raw := commentsRaw
	if len(raw) == 0 || string(raw) == "null" {
		raw = findingsRaw
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var wires []reviewCommentWire
	if err := json.Unmarshal(raw, &wires); err != nil {
		return nil, err
	}
	out := make([]ReviewComment, 0, len(wires))
	for _, w := range wires {
		file := strings.TrimSpace(w.File)
		if file == "" {
			file = strings.TrimSpace(w.Path)
		}
		related := w.RelatedFindings
		if len(related) == 0 {
			related = w.Related
		}
		out = append(out, ReviewComment{
			File:            file,
			Line:            w.Line,
			StartLine:       w.StartLine,
			Side:            w.Side,
			Severity:        Severity(strings.TrimSpace(w.Severity)),
			Body:            w.Body,
			RelatedFindings: related,
			Confidence:      Confidence(strings.TrimSpace(w.Confidence)),
			Category:        Category(strings.TrimSpace(w.Category)),
		})
	}
	return out, nil
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
		cat, rule := CategoryReview, "agent-comment"
		if c.Category == CategoryCorrelation {
			cat, rule = CategoryCorrelation, "correlation"
		}
		sev := c.Severity
		if strings.TrimSpace(string(sev)) == "" {
			sev = cat.DefaultSeverity()
		}
		out = append(out, Finding{
			File:            c.File,
			Line:            c.Line,
			StartLine:       c.StartLine,
			Rule:            rule,
			Substrate:       "redline/review",
			Category:        cat,
			Severity:        normalizeSeverityFor(sev, cat),
			Message:         c.Body,
			Source:          SourceLLM,
			RelatedFindings: c.RelatedFindings,
			Confidence:      NormalizeConfidence(c.Confidence),
		})
	}
	return out
}

// normalizeSeverity maps whatever the review file wrote onto the three
// severities the rest of the pipeline understands. Case and whitespace are
// forgiven; anything else falls back to the review default rather than
// flowing through verbatim, where an unknown value would outrank real errors
// in Sort, appear in no severity tile, and never match the post gate's
// blocking list.
func normalizeSeverity(s Severity) Severity {
	return normalizeSeverityFor(s, CategoryReview)
}

// normalizeSeverityFor maps whatever the review file wrote onto the three
// severities, falling back to the category's own default rather than letting
// an unknown value through, where it would outrank real errors in Sort,
// appear in no severity tile, and never match the post gate's blocking list.
func normalizeSeverityFor(s Severity, cat Category) Severity {
	switch Severity(strings.ToLower(strings.TrimSpace(string(s)))) {
	case SeverityError, "high", "critical":
		return SeverityError
	case SeverityWarning, "medium", "med":
		return SeverityWarning
	case SeverityInfo, "low", "hint":
		return SeverityInfo
	}
	return cat.DefaultSeverity()
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
