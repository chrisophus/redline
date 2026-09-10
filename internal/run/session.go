package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/pane"
)

// sessionFile is the snapshot post reads, so posting describes the run that
// produced the report rather than a fresh observation of a tree that may have
// moved.
const sessionFile = "session.json"

type session struct {
	Report   findings.Report          `json:"report"`
	Change   *change.Set              `json:"change"`
	Renders  []pane.Render            `json:"renders,omitempty"`
	Evidence map[string]pane.Artifact `json:"evidence,omitempty"`
	// LineCoverage is saved so a command that re-renders from a session
	// produces the same page `run` did. Without it the coverage stripe
	// silently disappears on any re-render, which reads as "nobody measured".
	LineCoverage map[string]map[int]bool `json:"lineCoverage,omitempty"`
	// Envelopes and ContextAbsent make the session the whole input to
	// `redline review`. A fixture is a session, and a fixture that had to
	// re-derive its context from source could not reproduce the review it
	// was recorded against.
	Envelopes     []*envelope.Envelope `json:"envelopes,omitempty"`
	ContextAbsent []string             `json:"contextAbsent,omitempty"`
	// PriorReview is what this pull request already heard, saved for the same
	// reason: `review` is a pure function of the session, so the conversation
	// it was shown has to be in the file rather than fetched again at review
	// time, where a reply posted in between would change a frozen input.
	PriorReview []feedback.Thread `json:"priorReview,omitempty"`
}

// SaveSession writes the run so post can work from it without re-observing.
func SaveSession(dir string, res *Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s := session{Report: res.Report, Change: res.Change, Renders: res.Renders,
		Evidence: res.Evidence, LineCoverage: res.LineCoverage,
		Envelopes: res.Envelopes, ContextAbsent: res.ContextAbsent,
		PriorReview: res.PriorReview}
	buf, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sessionFile), append(buf, '\n'), 0o644)
}

// LoadSession reads a prior run. Missing or corrupt files are errors: post
// must not silently re-observe.
func LoadSession(dir string) (*Result, error) {
	buf, err := os.ReadFile(filepath.Join(dir, sessionFile))
	if err != nil {
		return nil, fmt.Errorf("no prior run in %s (run `redline run` first): %w", dir, err)
	}
	var s session
	if err := json.Unmarshal(buf, &s); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	res := &Result{Report: s.Report, Change: s.Change, Renders: s.Renders,
		Evidence: s.Evidence, LineCoverage: s.LineCoverage,
		Envelopes: s.Envelopes, ContextAbsent: s.ContextAbsent,
		PriorReview: s.PriorReview}
	if res.Evidence == nil {
		res.Evidence = map[string]pane.Artifact{}
	}
	if s.Change != nil {
		res.Target = s.Change.Target
	}
	return res, nil
}
