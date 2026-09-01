package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccason/redline/internal/change"
	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/pane"
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
}

// SaveSession writes the run so post can work from it without re-observing.
func SaveSession(dir string, res *Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s := session{Report: res.Report, Change: res.Change, Renders: res.Renders,
		Evidence: res.Evidence}
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
		Evidence: s.Evidence}
	if res.Evidence == nil {
		res.Evidence = map[string]pane.Artifact{}
	}
	if s.Change != nil {
		res.Target = s.Change.Target
	}
	return res, nil
}
