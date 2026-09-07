package eval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
)

const fixtureDir = "../../testdata/fixtures"

func load(t *testing.T) []Fixture {
	t.Helper()
	fx, err := Load(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx) == 0 {
		t.Fatal("no fixtures; the eval would pass vacuously")
	}
	return fx
}

// Every fixture must load offline, from its own frozen files, with no git
// repository and no network. A fixture that needed either would stop working
// the moment the commit it came from was rewritten.
func TestFixturesLoadOffline(t *testing.T) {
	for _, f := range load(t) {
		if f.Session == nil || f.Session.Change == nil {
			t.Errorf("%s: session did not load", f.Annotation.Name)
			continue
		}
		if f.Annotation.Summary == "" {
			t.Errorf("%s: no summary; a fixture nobody described is not a measuring stick", f.Annotation.Name)
		}
		if len(f.Session.Change.Files) == 0 {
			t.Errorf("%s: frozen change has no files", f.Annotation.Name)
		}
	}
}

// The fixture set must keep covering the cases the design argues about. A set
// that drifts into whatever was convenient stops testing the hard half.
func TestFixtureSetCoversTheDeliberateCases(t *testing.T) {
	fx := load(t)
	var clean, correlation, knownGap, history int
	for _, f := range fx {
		if f.Annotation.Clean {
			clean++
		}
		for _, e := range f.Annotation.Expect {
			if e.Correlation {
				correlation++
			}
			if e.KnownGap != "" {
				knownGap++
			}
			if e.NeedsHistory {
				history++
			}
		}
	}
	if clean == 0 {
		t.Error("no fixture expects a clean review; the 29 percent is the harder target")
	}
	if correlation == 0 {
		t.Error("no fixture requires correlating two producers; that case is the argument for a second wave")
	}
	if knownGap == 0 {
		t.Error("no fixture states a known gap; gaps that leave the fixture set stop being tracked")
	}
	if history == 0 {
		t.Error("no fixture needs history; undoing a deliberate fix is invisible without it")
	}
}

// The correlation fixture is only a test of correlation if its wave-one
// output actually contains the fact to correlate against.
func TestCorrelationFixtureCarriesItsPrior(t *testing.T) {
	f, err := LoadOne(fixtureDir + "/correlation-not-null-column")
	if err != nil {
		t.Fatal(err)
	}
	var found *findings.Finding
	for i, fd := range f.Session.Report.Findings {
		if fd.Rule == "migration-add-not-null-no-default" {
			found = &f.Session.Report.Findings[i]
		}
	}
	if found == nil {
		t.Fatal("the fixture's frozen session has no NOT NULL finding, so there is nothing to correlate")
	}
	if found.ID == "" {
		t.Fatal("the prior needs a printable id for a review to reference it")
	}
	if found.Anchor == nil || !strings.Contains(found.Anchor.ID, "tenant_id") {
		t.Fatalf("the prior must name the column: anchor = %+v", found.Anchor)
	}
}

// Phase 1's exit criterion, and it costs nothing: every fixture's context has
// to fit the ceiling one review is budgeted for.
func TestEveryFixtureBudgetsWithinTheCeiling(t *testing.T) {
	for _, f := range load(t) {
		in := review.Input{
			Report: &f.Session.Report, Change: f.Session.Change,
			Envelopes: f.Session.Envelopes, Absent: f.Session.ContextAbsent,
		}
		got, err := review.Assemble(in, review.Options{})
		if err != nil {
			t.Errorf("%s: %v", f.Annotation.Name, err)
			continue
		}
		if got.InputEstimate > envelope.DefaultCeiling {
			t.Errorf("%s: %d input tokens exceeds the %d ceiling",
				f.Annotation.Name, got.InputEstimate, envelope.DefaultCeiling)
		}
		if got.Budget.DroppedTotal() > 0 {
			t.Logf("%s: %s", f.Annotation.Name, got.Budget.Summary())
		}
	}
}

func TestAssembledPromptsAreReproducible(t *testing.T) {
	for _, f := range load(t) {
		in := review.Input{
			Report: &f.Session.Report, Change: f.Session.Change,
			Envelopes: f.Session.Envelopes, Absent: f.Session.ContextAbsent,
		}
		first, err := review.Assemble(in, review.Options{})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			again, err := review.Assemble(in, review.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if again.Prompt != first.Prompt {
				t.Fatalf("%s: the same fixture assembled two different prompts", f.Annotation.Name)
			}
		}
		if strings.ContainsRune(first.Prompt, 0) {
			t.Errorf("%s: prompt contains NUL bytes", f.Annotation.Name)
		}
	}
}

// A prompt that does not carry the prior findings cannot ask the model not to
// restate them, and cannot ask it to connect two.
func TestPriorFindingsReachThePrompt(t *testing.T) {
	f, err := LoadOne(fixtureDir + "/correlation-not-null-column")
	if err != nil {
		t.Fatal(err)
	}
	got, err := review.Assemble(review.Input{
		Report: &f.Session.Report, Change: f.Session.Change,
	}, review.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "tenant_id") {
		t.Fatal("the NOT NULL prior did not reach the prompt")
	}
	if !strings.Contains(got.Prompt, "Do not restate") {
		t.Fatal("the prompt must say the priors are already on the report")
	}
}

func TestScoreCountsACorrelationOnlyWhenItReferences(t *testing.T) {
	f, err := LoadOne(fixtureDir + "/correlation-not-null-column")
	if err != nil {
		t.Fatal(err)
	}
	// The right answer: a correlation carrying a reference.
	good := findings.Review{Comments: []findings.ReviewComment{{
		File: "internal/store/user.go", Line: 8,
		Body:            "TenantID is a plain string while the migration makes tenant_id NOT NULL with no default, so any insert that omits it writes an empty string.",
		Category:        findings.CategoryCorrelation,
		RelatedFindings: []string{"f2076e74fbd"},
	}}}
	if sc := Score(f, good); len(sc.Caught) != 1 {
		t.Fatalf("a correct correlation scored as %+v", sc)
	}
	// The same words with no reference and no category is the failure mode
	// this fixture exists to catch: restating the prior.
	weak := findings.Review{Comments: []findings.ReviewComment{{
		File: "internal/store/user.go", Line: 8,
		Body: "migration 0002 adds a NOT NULL column with no default",
	}}}
	sc := Score(f, weak)
	if len(sc.Caught) != 0 {
		t.Fatal("restating a prior must not score as a correlation")
	}
	if len(sc.QuietViolations) == 0 {
		t.Fatal("restating a prior is the false positive the annotation names")
	}
}

func TestScoreHoldsCleanFixturesToSilence(t *testing.T) {
	f, err := LoadOne(fixtureDir + "/golangci-action-bump")
	if err != nil {
		t.Fatal(err)
	}
	if sc := Score(f, findings.Review{}); !sc.CleanHeld {
		t.Fatal("an empty review of a clean change is the right answer")
	}
	noisy := findings.Review{Comments: []findings.ReviewComment{{
		File: ".github/workflows/ci.yml", Line: 28,
		Body: "You may want to pin this action to a SHA and check v7 for breaking changes.",
	}}}
	sc := Score(f, noisy)
	if sc.CleanHeld {
		t.Fatal("a comment on a clean change breaks the clean rate")
	}
	if len(sc.QuietViolations) == 0 {
		t.Fatal("padding a clean change is the failure this fixture measures")
	}
}

func TestSumBuildsTheComparisonTable(t *testing.T) {
	cards := []Scorecard{
		{Fixture: "a", Caught: []string{"x"}},
		{Fixture: "b", Missed: []string{"y"}, KnownGaps: []string{"y"}, Extra: 2},
		{Fixture: "c", Clean: true, CleanHeld: true},
	}
	tot := Sum(cards)
	if tot.Expected != 2 || tot.Caught != 1 || tot.Missed != 1 {
		t.Fatalf("totals = %+v", tot)
	}
	if tot.KnownGaps != 1 {
		t.Fatal("a known gap must stay visible in the totals")
	}
	row := Table("sonnet single pass", 0.2871, tot)
	if !strings.Contains(row, "1/2") || !strings.Contains(row, "1/1") {
		t.Fatalf("row = %q", row)
	}
}

// TestSweep is the paid half. It calls a model once per fixture and prints the
// comparison table, so it is opt-in: every model-side experiment costs money
// and everything above this line costs nothing.
//
//	REDLINE_EVAL_MODEL=claude-sonnet-5 go test ./internal/eval -run TestSweep -v
func TestSweep(t *testing.T) {
	model := os.Getenv("REDLINE_EVAL_MODEL")
	if model == "" {
		t.Skip("set REDLINE_EVAL_MODEL to run the paid sweep (it calls a model once per fixture)")
	}
	fx := load(t)
	var cards []Scorecard
	var costs []float64
	for _, f := range fx {
		in := review.Input{
			Report: &f.Session.Report, Change: f.Session.Change,
			Envelopes: f.Session.Envelopes, Absent: f.Session.ContextAbsent,
		}
		out, err := review.Run(context.Background(), in, review.Options{
			Model:  model,
			Effort: os.Getenv("REDLINE_EVAL_EFFORT"),
		})
		if err != nil {
			t.Errorf("%s: %v", f.Annotation.Name, err)
			continue
		}
		sc := Score(f, out.Review)
		cards = append(cards, sc)
		costs = append(costs, out.CostUSD)
		t.Logf("%s: %s caught=%v missed=%v quiet=%v extra=%d",
			f.Annotation.Name, out.Summary(), sc.Caught, sc.Missed, sc.QuietViolations, sc.Extra)
	}
	label := model
	if e := os.Getenv("REDLINE_EVAL_EFFORT"); e != "" {
		label += " effort=" + e
	}
	fmt.Println(TableHeader)
	fmt.Println(Table(label, median(costs), Sum(cards)))
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64{}, xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}
