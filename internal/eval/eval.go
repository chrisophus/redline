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
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
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
	// reference. Checked against the fixture's own findings: the comment's
	// references are resolved the way the report resolves them, and a
	// reference that lands nowhere, or lands on some other rule, is not a
	// correlation caught.
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

// Reject is a particular wrong finding, labelled after a review made it.
//
// Quiet is a topic a review should stay off, written in advance by whoever
// wrote the fixture. Reject is the other direction: a comment a review
// actually produced, read afterwards, and judged wrong. It is the shape of
// Expectation on purpose, because it is scored the same way and written from
// the same evidence, and the two lists together are what make a comment
// classifiable at all.
//
// This is what closes the hole the field found. Everything unmatched used to
// land in Extra, which is documented as not wrong, so a reviewer could double
// its output with invention and score identically. A false positive nobody
// wrote down is a false positive nobody can tune against.
type Reject struct {
	Key  string `json:"key"`
	What string `json:"what"`
	// Why the finding is wrong, in the words of whoever judged it. A reader
	// deciding whether to trust this label needs the reason, not the verdict.
	Why string `json:"why"`
	// Source says who judged it: "author" for a reader of a posted review,
	// "fixture" for the fixture's own author reading a dumped sample. An
	// author's dismissal is evidence about the reviewer and it is not ground
	// truth, so the label records which kind it is rather than flattening
	// them.
	Source string   `json:"source"`
	File   string   `json:"file"`
	AnyOf  []string `json:"any_of"`
	AllOf  []string `json:"all_of"`
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
	Reject               []Reject      `json:"reject"`
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
	// Rejected holds the keys of labelled wrong findings this review made
	// again. Counted with QuietViolations as a false positive; kept apart so
	// a reviewer straying onto a forbidden topic can be told from one
	// repeating a specific mistake somebody already read and judged.
	Rejected []string
	// Extra counts comments that matched no expectation, violated no quiet
	// rule and matched no reject. Not scored as wrong. A review may
	// legitimately find something the annotation's author did not, and until
	// somebody reads one it is unlabelled rather than wrong.
	Extra int
	// CleanHeld is meaningful for a clean fixture: it stayed silent.
	CleanHeld bool
	// Samples is how many independent reviews were scored, and CaughtIn how
	// many of them caught each expectation.
	//
	// A review is not a stable function of its input, and measurement put
	// the variance at total: across forty-odd samples of one fixture, no
	// finding was ever produced by two samples. So "did this run catch it"
	// is close to a coin flip, and a single-sample score is not a
	// measurement of a configuration. It is what made a k=1 sweep report
	// 0 of 14 on a fixture where three samples find 7, and nearly shipped a
	// prompt change on the strength of it.
	//
	// Caught above is the union: caught by at least one sample. CaughtIn is
	// the rate, which is the number to compare configurations on.
	Samples  int
	CaughtIn map[string]int
	// RejectedIn is the same rate for the labelled wrong findings. Precision
	// varies across samples exactly as recall does, and the union reports a
	// mistake one sample in three made as though every sample made it, which
	// overstates a rare invention and understates a reliable one.
	RejectedIn map[string]int
}

// Score compares one review against one annotation.
func Score(f Fixture, rev findings.Review) Scorecard {
	sc := Scorecard{
		Fixture:  f.Annotation.Name,
		Clean:    f.Annotation.Clean,
		Comments: len(rev.Comments),
	}
	// The frozen session's own findings, which is what a correlation's
	// references have to resolve against: the reviewer was shown these and
	// nothing else.
	var prior *findings.Report
	if f.Session != nil {
		prior = &f.Session.Report
	}
	matched := make([]bool, len(rev.Comments))
	for _, exp := range f.Annotation.Expect {
		hit := -1
		for i, c := range rev.Comments {
			if expectationMatches(exp, c, prior) {
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
	// After the expectations, so a comment that satisfies one is not also
	// counted as a mistake: a reject written loosely enough to overlap a real
	// defect is a bad label, and the labels test is where that is caught.
	for i, c := range rev.Comments {
		if matched[i] {
			continue
		}
		for _, r := range f.Annotation.Reject {
			if rejectMatches(r, c) {
				sc.Rejected = append(sc.Rejected, r.Key)
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

// ScoreSamples scores k independent reviews of one fixture: the union of what
// they caught, plus the rate at which each expectation was caught.
//
// This is the shape a variable producer has to be measured in. Score on one
// review answers "did this run catch it", which the variance makes close to
// a coin flip; the rate answers "how reliably does this configuration catch
// it", which is the question a comparison between configurations is asking.
//
// Comments, Extra and QuietViolations are the union's, because that is what a
// reader of the merged review would see, and CleanHeld requires every sample
// to have stayed silent: a clean change that one sample in three comments on
// is not a clean result.
func ScoreSamples(f Fixture, revs []findings.Review) Scorecard {
	if len(revs) == 0 {
		return Scorecard{Fixture: f.Annotation.Name, Clean: f.Annotation.Clean}
	}
	caughtIn := map[string]int{}
	rejectedIn := map[string]int{}
	cleanHeld := true
	for _, rev := range revs {
		one := Score(f, rev)
		for _, key := range one.Caught {
			caughtIn[key]++
		}
		for _, key := range one.Rejected {
			rejectedIn[key]++
		}
		cleanHeld = cleanHeld && one.CleanHeld
	}
	sc := Score(f, unionOf(revs))
	sc.Samples = len(revs)
	sc.CaughtIn = caughtIn
	sc.RejectedIn = rejectedIn
	sc.CleanHeld = cleanHeld
	return sc
}

// unionOf merges samples the way the producer does, so the score is of the
// review a reader would be handed. Identity is the file plus the message with
// its digits collapsed: two samples describing one defect are one finding.
func unionOf(revs []findings.Review) findings.Review {
	var out findings.Review
	seen := map[string]bool{}
	for _, rev := range revs {
		if len(rev.Overview) > len(out.Overview) {
			out.Overview = rev.Overview
		}
		for _, c := range rev.Comments {
			// The producer's own key, not a copy of it. The score has to be of
			// the review a reader is handed, and two implementations of "these
			// are the same finding" would make it a score of something else.
			key := review.UnionKey(c)
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Comments = append(out.Comments, c)
		}
	}
	return out
}

func expectationMatches(exp Expectation, c findings.ReviewComment, prior *findings.Report) bool {
	if exp.File != "" && c.File != exp.File {
		return false
	}
	if exp.Correlation {
		if c.Category != findings.CategoryCorrelation || len(c.RelatedFindings) == 0 {
			return false
		}
	}
	if len(exp.RelatesToRules) > 0 && !referencesRules(exp.RelatesToRules, c, prior) {
		return false
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

// referencesRules resolves a comment's references against the frozen
// session's findings, the same way the report resolves them when it renders
// the link, and reports whether they cover every rule the expectation names.
// Counting a non-empty relatedFindings as enough made the correlation score
// meaningless: a stale id, an invented id and [""] all read as caught, which
// is the one number this milestone is measured on.
func referencesRules(rules []string, c findings.ReviewComment, prior *findings.Report) bool {
	if prior == nil || c.Category != findings.CategoryCorrelation {
		return false
	}
	got := make(map[string]bool, len(c.RelatedFindings))
	for _, ref := range c.RelatedFindings {
		if f := prior.FindRef(ref); f != nil {
			got[f.Rule] = true
		}
	}
	for _, want := range rules {
		if !got[want] {
			return false
		}
	}
	return true
}

// rejectMatches reports whether a comment is the labelled wrong finding.
//
// A correlation is not exempt, unlike a quiet rule. Quiet names a topic, and a
// correlation that uses the topic's vocabulary is doing the job it exists for;
// reject names a claim, and a claim does not become true because it arrived
// with references attached.
func rejectMatches(r Reject, c findings.ReviewComment) bool {
	if r.File != "" && c.File != r.File {
		return false
	}
	body := strings.ToLower(c.Body)
	for _, want := range r.AllOf {
		if !strings.Contains(body, strings.ToLower(want)) {
			return false
		}
	}
	if len(r.AnyOf) == 0 {
		// AllOf alone is a complete label. With neither, the entry would
		// match every comment on the file, which is a labelling mistake
		// rather than a review that got everything wrong.
		return len(r.AllOf) > 0
	}
	for _, want := range r.AnyOf {
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
	Rejected        int
	Extra           int
	CleanFixtures   int
	CleanHeld       int
	// Samples is the per-fixture sample count when every fixture used the
	// same one, and CaughtSampleHits the number of (expectation, sample)
	// pairs that hit. Together they are the rate: a configuration that
	// catches an expectation in one sample of three is not the equal of one
	// that catches it in three, and the union hides that difference.
	Samples          int
	CaughtSampleHits int
}

// FalsePositives is what a configuration got wrong: a topic the annotation
// forbade, plus a specific finding somebody read and judged wrong. Both are
// mistakes, they are counted together because a reader of the table wants one
// number, and Totals keeps them apart because a reader diagnosing a
// regression wants two.
func (t Totals) FalsePositives() int { return t.QuietViolations + t.Rejected }

// Sum aggregates. Expected counts the whole expectation set, optional ones
// included whether or not they were caught: a caught optional lands in Caught
// and a missed one in MissedOptional, so leaving MissedOptional out moved the
// denominator with the result. Two configurations then printed 5/5 and 4/4
// for the same fixture set, and the comparison table's rows are only worth
// reading side by side if the number under the line is the same.
func Sum(cards []Scorecard) Totals {
	var t Totals
	for _, c := range cards {
		t.Fixtures++
		t.Expected += len(c.Caught) + len(c.Missed) + len(c.MissedOptional)
		t.Caught += len(c.Caught)
		t.Missed += len(c.Missed)
		t.KnownGaps += len(c.KnownGaps)
		t.QuietViolations += len(c.QuietViolations)
		t.Rejected += len(c.Rejected)
		t.Extra += c.Extra
		if c.Clean {
			t.CleanFixtures++
			if c.CleanHeld {
				t.CleanHeld++
			}
		}
		for _, n := range c.CaughtIn {
			t.CaughtSampleHits += n
		}
		switch {
		case t.Samples == 0:
			t.Samples = c.Samples
		case c.Samples != 0 && c.Samples != t.Samples:
			// Mixed sample counts make the rate meaningless rather than
			// merely imprecise, so it is withheld instead of averaged.
			t.Samples = -1
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
	// The rate is the column to compare on. Caught is the union, which rises
	// with the sample count for a producer whose samples do not overlap, so
	// two rows are only comparable on it when both took the same number.
	rate := "n/a"
	if t.Samples > 0 && t.Expected > 0 {
		rate = fmt.Sprintf("%.0f%% of %d×%d", 100*float64(t.CaughtSampleHits)/float64(t.Expected*t.Samples),
			t.Expected, t.Samples)
	}
	// A model the price table does not carry costs an unknown amount, not
	// zero. Printing $0.0000 for it makes the cheapest-looking row in the
	// comparison the one nobody priced.
	cost := fmt.Sprintf("$%.4f", medianCostUSD)
	if math.IsNaN(medianCostUSD) {
		cost = "unpriced"
	}
	// False positives and unlabelled extras are different claims and the
	// table has to show both. A configuration that halves the extras by
	// inventing labelled-wrong findings instead is worse, and one number
	// cannot say so.
	fp := fmt.Sprintf("%d", t.FalsePositives())
	if t.Rejected > 0 && t.QuietViolations > 0 {
		fp = fmt.Sprintf("%d (%d quiet, %d rejected)", t.FalsePositives(), t.QuietViolations, t.Rejected)
	}
	return fmt.Sprintf("| %s | %s | %d/%d | %s | %s | %d | %s |",
		label, cost, t.Caught, t.Expected, rate, fp, t.Extra, clean)
}

// TableHeader is the header for Table's rows.
const TableHeader = "| Config | Median cost | Caught (union) | Caught rate | False positives | Unlabelled | Clean held |\n" +
	"|---|---|---|---|---|---|---|"
