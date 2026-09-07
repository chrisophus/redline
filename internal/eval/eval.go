// Package eval scores a review against hand-written expectations.
//
// A fixture is a frozen session plus an annotation. The session is real
// Redline output, written by `run` and never re-derived: the diff, the
// wave-one findings, the context envelopes, and the producers that did not
// run. The annotation says what a good review of that change should have
// caught, and what it should have stayed quiet about.
//
// Freezing rather than re-deriving is the whole point. A fixture that
// re-observed its repository would measure whatever the panes do today, and
// the review's input would move underneath the score. It also means the
// fixtures stay usable when a second provider arrives: the envelope in the
// file is the one the review was scored against.
//
// Scoring is keyword and location matching, which is crude. It is crude on
// purpose: it costs nothing, runs offline, and gives the same answer twice.
// A model judging another model's review costs money on every eval run and
// makes the measuring stick as noisy as the thing being measured. The cost of
// the crudeness is that a correct finding phrased unusually scores as a miss,
// so the keyword lists are written wide and a disagreement is a reason to
// read the review rather than to trust the number.
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/run"
)

// Expectation is one thing a good review should have said.
type Expectation struct {
	Key  string `json:"key"`
	What string `json:"what"`
	// File, when set, requires the comment to land on that file.
	File string `json:"file"`
	// AnyOf matches when the comment body contains at least one entry.
	AnyOf []string `json:"any_of"`
	// AllOf matches only when the body contains every entry.
	AllOf []string `json:"all_of"`
	// Correlation requires the matching comment to be a correlation
	// carrying references, rather than a remark that happens to use the
	// same words.
	Correlation bool `json:"correlation"`
	// RelatesToRules names the wave-one rules the correlation should
	// reference. Checked against the fixture's own findings.
	RelatesToRules []string `json:"relates_to_rules"`
	// Optional expectations are worth having and are not scored as misses.
	Optional bool `json:"optional"`
	// KnownGap records that nothing in Redline can catch this yet. It still
	// scores as a miss, and the miss is labelled rather than hidden: a
	// fixture that quietly stopped counting would take the gap off the
	// board.
	KnownGap     string `json:"known_gap"`
	NeedsHistory bool   `json:"needs_history"`
	Note         string `json:"note"`
}

// Quiet is something a review should not have said.
type Quiet struct {
	About string `json:"about"`
	Note  string `json:"note"`
	// MatchesWithoutCorrelation flags a comment containing any of these
	// words unless it is a correlation. A correlation that happens to use
	// the same vocabulary is doing the job, not padding.
	MatchesWithoutCorrelation []string `json:"matches_without_correlation"`
}

// Annotation is the hand-written half of a fixture.
type Annotation struct {
	Name    string `json:"name"`
	Origin  string `json:"origin"`
	Summary string `json:"summary"`
	// Clean marks a change that deserves no comment at all. These are the
	// hard cases: roughly three in ten real changes are clean, and a
	// reviewer that cannot return empty is not a reviewer.
	Clean                bool          `json:"clean"`
	WhyThisFixtureExists string        `json:"why_this_fixture_exists"`
	Expect               []Expectation `json:"expect"`
	Quiet                []Quiet       `json:"quiet"`
}

// Fixture is one frozen change with its annotation.
type Fixture struct {
	Dir        string
	Annotation Annotation
	Session    *run.Result
}

// Load reads every fixture under dir.
func Load(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Fixture
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		f, err := LoadOne(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Annotation.Name < out[j].Annotation.Name })
	return out, nil
}

// LoadOne reads a single fixture directory.
func LoadOne(dir string) (Fixture, error) {
	var f Fixture
	f.Dir = dir
	buf, err := os.ReadFile(filepath.Join(dir, "annotation.json"))
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(buf, &f.Annotation); err != nil {
		return f, fmt.Errorf("annotation.json: %w", err)
	}
	sess, err := run.LoadSession(dir)
	if err != nil {
		return f, err
	}
	f.Session = sess
	return f, nil
}

// Scorecard is one fixture's result.
type Scorecard struct {
	Fixture  string
	Clean    bool
	Comments int
	// Caught, Missed and MissedOptional hold expectation keys.
	Caught         []string
	Missed         []string
	MissedOptional []string
	// KnownGaps are misses that nothing shipping can catch yet, named so a
	// score can be read without mistaking a gap for a regression.
	KnownGaps []string
	// QuietViolations are the false positives that matter most: a reviewer
	// saying the thing the annotation says it should not.
	QuietViolations []string
	// Extra counts comments that matched no expectation and violated no
	// quiet rule. Not scored as wrong. A review may legitimately find
	// something the annotation's author did not.
	Extra int
	// CleanHeld is meaningful for a clean fixture: it stayed silent.
	CleanHeld bool
}

// Score compares one review against one annotation.
func Score(f Fixture, rev findings.Review) Scorecard {
	sc := Scorecard{
		Fixture:  f.Annotation.Name,
		Clean:    f.Annotation.Clean,
		Comments: len(rev.Comments),
	}
	matched := make([]bool, len(rev.Comments))
	for _, exp := range f.Annotation.Expect {
		hit := -1
		for i, c := range rev.Comments {
			if expectationMatches(exp, c) {
				hit = i
				break
			}
		}
		switch {
		case hit >= 0:
			matched[hit] = true
			sc.Caught = append(sc.Caught, exp.Key)
		case exp.Optional:
			sc.MissedOptional = append(sc.MissedOptional, exp.Key)
		default:
			sc.Missed = append(sc.Missed, exp.Key)
			if exp.KnownGap != "" {
				sc.KnownGaps = append(sc.KnownGaps, exp.Key)
			}
		}
	}
	for i, c := range rev.Comments {
		for _, q := range f.Annotation.Quiet {
			if quietViolated(q, c) {
				sc.QuietViolations = append(sc.QuietViolations, q.About)
				matched[i] = true
				break
			}
		}
	}
	for i := range rev.Comments {
		if !matched[i] {
			sc.Extra++
		}
	}
	sc.CleanHeld = !f.Annotation.Clean || len(rev.Comments) == 0
	return sc
}

func expectationMatches(exp Expectation, c findings.ReviewComment) bool {
	if exp.File != "" && c.File != exp.File {
		return false
	}
	if exp.Correlation {
		if c.Category != findings.CategoryCorrelation || len(c.RelatedFindings) == 0 {
			return false
		}
	}
	body := strings.ToLower(c.Body)
	for _, want := range exp.AllOf {
		if !strings.Contains(body, strings.ToLower(want)) {
			return false
		}
	}
	if len(exp.AnyOf) == 0 {
		return true
	}
	for _, want := range exp.AnyOf {
		if strings.Contains(body, strings.ToLower(want)) {
			return true
		}
	}
	return false
}

func quietViolated(q Quiet, c findings.ReviewComment) bool {
	if c.Category == findings.CategoryCorrelation {
		return false
	}
	body := strings.ToLower(c.Body)
	for _, w := range q.MatchesWithoutCorrelation {
		if strings.Contains(body, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// Totals aggregates scorecards into the numbers a configuration is judged on.
type Totals struct {
	Fixtures        int
	Expected        int
	Caught          int
	Missed          int
	KnownGaps       int
	QuietViolations int
	Extra           int
	CleanFixtures   int
	CleanHeld       int
}

// Sum aggregates.
func Sum(cards []Scorecard) Totals {
	var t Totals
	for _, c := range cards {
		t.Fixtures++
		t.Expected += len(c.Caught) + len(c.Missed)
		t.Caught += len(c.Caught)
		t.Missed += len(c.Missed)
		t.KnownGaps += len(c.KnownGaps)
		t.QuietViolations += len(c.QuietViolations)
		t.Extra += c.Extra
		if c.Clean {
			t.CleanFixtures++
			if c.CleanHeld {
				t.CleanHeld++
			}
		}
	}
	return t
}

// Table renders the comparison the tuning phase is built around. One row per
// configuration; the caller supplies the label and the median cost.
func Table(label string, medianCostUSD float64, t Totals) string {
	clean := "n/a"
	if t.CleanFixtures > 0 {
		clean = fmt.Sprintf("%d/%d", t.CleanHeld, t.CleanFixtures)
	}
	return fmt.Sprintf("| %s | $%.4f | %d/%d | %d | %d | %s |",
		label, medianCostUSD, t.Caught, t.Expected, t.QuietViolations, t.Extra, clean)
}

// TableHeader is the header for Table's rows.
const TableHeader = "| Config | Median cost | Caught | False positives | Extra | Clean held |\n" +
	"|---|---|---|---|---|---|"
