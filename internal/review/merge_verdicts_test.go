package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// Merge replaced the whole verdict map with whatever was on disk, which was
// right while only a person or an agent wrote verdicts and wrong the moment
// `redline review` learned to. Every ruling the model made was discarded on
// the second run onward.
func TestMergeKeepsAnExistingRulingAndTakesANewOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	existing := findings.Review{
		Overview: "an earlier review",
		Verdicts: map[string]findings.Verdict{
			"fp-human": {Ruling: "justified", Rationale: "a person decided this"},
		},
	}
	buf, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}

	fresh := findings.Review{
		Overview: "a new review",
		Verdicts: map[string]findings.Verdict{
			"fp-human": {Ruling: "should-fix", Rationale: "the model disagrees"},
			"fp-new":   {Ruling: "rule-noisy", Rationale: "errcheck fires on every deferred Close"},
		},
	}
	if err := Merge(path, fresh); err != nil {
		t.Fatal(err)
	}
	got, err := findings.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	// A ruling already on disk is someone's judgment and survives.
	if got.Verdicts["fp-human"].Ruling != "justified" {
		t.Errorf("fp-human = %q, want the existing ruling kept", got.Verdicts["fp-human"].Ruling)
	}
	// A finding nobody had ruled on takes the new one.
	if got.Verdicts["fp-new"].Ruling != "rule-noisy" {
		t.Errorf("fp-new = %q; the model's new ruling was discarded", got.Verdicts["fp-new"].Ruling)
	}
	if got.Overview != "a new review" {
		t.Errorf("overview = %q, want the new prose", got.Overview)
	}
}
