package findings

import (
	"encoding/json"
	"os"
)

// Review is the agent's judgments of a run's findings, keyed by fingerprint.
// The agent reads findings.json, rules on the findings it was asked to judge,
// and writes this file; Redline merges it back on the next run. Redline never
// writes it — like comments.json, it is the reviewer's own state, and a re-run
// must not clobber it.
type Review struct {
	Verdicts map[string]Verdict `json:"verdicts"`
}

// LoadReview reads a review file. A missing file is not an error: most runs
// have no verdicts yet. Every verdict reads as source "llm" regardless of what
// the file claims — Redline attributes the reading, the file does not get to.
func LoadReview(path string) (map[string]Verdict, error) {
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
	return r.Verdicts, nil
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
