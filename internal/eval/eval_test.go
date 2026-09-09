package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
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

// sampleFixture is a fixture with two expectations and nothing else, for
// testing how samples are scored rather than how matching works.
func sampleFixture() Fixture {
	return Fixture{
		Annotation: Annotation{
			Name: "two-defects",
			Expect: []Expectation{
				{Key: "loop", AnyOf: []string{"retry loop"}},
				{Key: "mutex", AnyOf: []string{"mutex"}},
			},
		},
		Session: &run.Result{},
	}
}

func sampleWith(bodies ...string) findings.Review {
	var rev findings.Review
	for _, b := range bodies {
		rev.Comments = append(rev.Comments, findings.ReviewComment{File: "a.go", Body: b})
	}
	return rev
}

// The union is what a reader is handed, so Caught is the union. The rate is
// what distinguishes configurations, and a single sample cannot express it:
// two disjoint samples each catching one of two expectations is a full union
// and a half rate, and those are different facts about the reviewer.
func TestScoreSamplesSeparatesTheUnionFromTheRate(t *testing.T) {
	f := sampleFixture()
	sc := ScoreSamples(f, []findings.Review{
		sampleWith("the retry loop never bounds attempts"),
		sampleWith("the mutex is never unlocked"),
	})
	if len(sc.Caught) != 2 {
		t.Errorf("union caught %v, want both", sc.Caught)
	}
	if sc.Samples != 2 {
		t.Errorf("samples = %d, want 2", sc.Samples)
	}
	if sc.CaughtIn["loop"] != 1 || sc.CaughtIn["mutex"] != 1 {
		t.Errorf("caughtIn = %v, want each expectation in exactly one sample", sc.CaughtIn)
	}
	tot := Sum([]Scorecard{sc})
	// Two expectations over two samples is four chances; two hit.
	if tot.CaughtSampleHits != 2 || tot.Samples != 2 || tot.Expected != 2 {
		t.Errorf("totals = %+v, want 2 hits of 2×2", tot)
	}
	row := Table("cfg", 0.1, tot)
	if !strings.Contains(row, "2/2") {
		t.Errorf("the union column is missing from %q", row)
	}
	if !strings.Contains(row, "50% of 2×2") {
		t.Errorf("the rate column does not distinguish this from a reliable catch: %q", row)
	}
}

// The same union with every sample catching everything is the reliable
// configuration, and the table has to show the difference.
func TestScoreSamplesRateSeparatesReliableFromLucky(t *testing.T) {
	f := sampleFixture()
	both := sampleWith("the retry loop never bounds attempts", "the mutex is never unlocked")
	reliable := Sum([]Scorecard{ScoreSamples(f, []findings.Review{both, both})})
	lucky := Sum([]Scorecard{ScoreSamples(f, []findings.Review{
		sampleWith("the retry loop never bounds attempts"),
		sampleWith("the mutex is never unlocked"),
	})})
	if reliable.Caught != lucky.Caught {
		t.Fatalf("the unions differ (%d vs %d); the rate is the only thing that should",
			reliable.Caught, lucky.Caught)
	}
	if reliable.CaughtSampleHits <= lucky.CaughtSampleHits {
		t.Errorf("the rate does not separate them: %d vs %d",
			reliable.CaughtSampleHits, lucky.CaughtSampleHits)
	}
}

// A clean fixture that one sample in three comments on is not a clean result.
// CleanHeld has to be the conjunction, or sampling would launder a false
// positive into silence.
func TestScoreSamplesCleanHeldRequiresEverySample(t *testing.T) {
	f := Fixture{
		Annotation: Annotation{Name: "clean", Clean: true},
		Session:    &run.Result{},
	}
	sc := ScoreSamples(f, []findings.Review{{}, sampleWith("I would consider renaming this")})
	if sc.CleanHeld {
		t.Error("a sample that spoke on a clean change was laundered by the union")
	}
}

// Rows are only comparable when both took the same number of samples, so a
// mixed set withholds the rate rather than averaging it into nonsense.
func TestSumWithholdsTheRateOnMixedSampleCounts(t *testing.T) {
	f := sampleFixture()
	one := ScoreSamples(f, []findings.Review{sampleWith("the retry loop never bounds attempts")})
	three := ScoreSamples(f, []findings.Review{{}, {}, sampleWith("the mutex is never unlocked")})
	row := Table("mixed", 0.1, Sum([]Scorecard{one, three}))
	if !strings.Contains(row, "n/a") {
		t.Errorf("a mixed sample count reported a rate anyway: %q", row)
	}
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
	// The reference is read out of the fixture rather than written into the
	// test: the annotation names a rule, and the score is only meaningful if
	// it resolves the comment's reference to that rule's finding.
	var prior, other string
	for _, fd := range f.Session.Report.Findings {
		switch {
		case fd.Rule == "migration-add-not-null-no-default":
			prior = fd.ID
		case other == "":
			other = fd.ID
		}
	}
	if prior == "" || other == "" {
		t.Fatalf("this fixture needs the NOT NULL prior and one other finding to reference: %+v",
			f.Session.Report.Findings)
	}
	correlation := func(refs ...string) findings.Review {
		return findings.Review{Comments: []findings.ReviewComment{{
			File: "internal/store/user.go", Line: 8,
			Body:            "TenantID is a plain string while the migration makes tenant_id NOT NULL with no default, so any insert that omits it writes an empty string.",
			Category:        findings.CategoryCorrelation,
			RelatedFindings: refs,
		}}}
	}
	// The right answer: a correlation whose reference lands on the rule the
	// annotation says it has to connect to.
	if sc := Score(f, correlation(prior)); len(sc.Caught) != 1 {
		t.Fatalf("a correct correlation scored as %+v", sc)
	}
	// An id from some other run reads as a correlation and connects nothing.
	if sc := Score(f, correlation("f00000000000")); len(sc.Caught) != 0 {
		t.Fatalf("a reference that resolves to nothing scored as caught: %+v", sc)
	}
	// Neither does one that resolves to a different finding.
	if sc := Score(f, correlation(other)); len(sc.Caught) != 0 {
		t.Fatalf("a reference to the wrong rule scored as caught: %+v", sc)
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
		// An optional expectation nobody caught. It still belongs in the
		// denominator: the configuration that does catch it counts it in
		// Caught, and a denominator that moves with the result makes two
		// rows of this table incomparable.
		{Fixture: "d", MissedOptional: []string{"z"}},
	}
	tot := Sum(cards)
	if tot.Expected != 3 || tot.Caught != 1 || tot.Missed != 1 {
		t.Fatalf("totals = %+v", tot)
	}
	if tot.KnownGaps != 1 {
		t.Fatal("a known gap must stay visible in the totals")
	}
	row := Table("sonnet single pass", 0.2871, tot)
	if !strings.Contains(row, "1/3") || !strings.Contains(row, "1/1") {
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
	// One sample per fixture is not a measurement of a configuration: the
	// samples do not overlap, so a single run's score is close to a coin
	// flip. REDLINE_EVAL_SAMPLES buys the rate instead, at k times the cost
	// and the same wall clock, because the samples go out together.
	samples := 1
	if s := os.Getenv("REDLINE_EVAL_SAMPLES"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("REDLINE_EVAL_SAMPLES=%q is not a positive count", s)
		}
		samples = n
	}
	// A matrix over models, effort levels and sample counts multiplies the
	// cost of the whole set, and most of the set is cheap noise for that
	// purpose: the richly annotated fixtures are where a configuration is
	// distinguishable. REDLINE_EVAL_FIXTURE narrows it, and naming the clean
	// one alongside keeps the clean rate in the comparison so a louder
	// configuration cannot win by padding.
	fx := load(t)
	if only := os.Getenv("REDLINE_EVAL_FIXTURE"); only != "" {
		want := map[string]bool{}
		for _, name := range strings.Split(only, ",") {
			want[strings.TrimSpace(name)] = true
		}
		var kept []Fixture
		for _, f := range fx {
			if want[f.Annotation.Name] {
				kept = append(kept, f)
				delete(want, f.Annotation.Name)
			}
		}
		for name := range want {
			t.Fatalf("REDLINE_EVAL_FIXTURE names %q, which is not a fixture", name)
		}
		fx = kept
	}
	var cards []Scorecard
	var costs []float64
	for _, f := range fx {
		in := review.Input{
			Report: &f.Session.Report, Change: f.Session.Change,
			Envelopes: f.Session.Envelopes, Absent: f.Session.ContextAbsent,
		}
		// The arm that answers whether context beyond the diff earns its
		// cost. Dropping the envelopes rather than the whole field keeps the
		// prompt's absent-context section honest: the review is told the
		// context is missing, which is what a provider failure looks like,
		// instead of being handed a change that silently had no provider.
		if os.Getenv("REDLINE_EVAL_NOCONTEXT") != "" {
			in.Envelopes = nil
		}
		var revs []findings.Review
		var cost float64
		for range samples {
			opts := review.Options{
				Model:  model,
				Effort: os.Getenv("REDLINE_EVAL_EFFORT"),
			}
			// A model on another wire is an arm like any other. The key and
			// base URL are read the same way the command reads them, so a
			// sweep and a real review reach the same endpoint.
			if os.Getenv("REDLINE_EVAL_API") == "openai" {
				opts.API = review.APIOpenAI
				opts.APIKey = os.Getenv("OPENAI_API_KEY")
				opts.BaseURL = os.Getenv("OPENAI_BASE_URL")
				opts.APIUser = os.Getenv("OPENAI_USER")
			}
			out, err := review.Run(context.Background(), in, opts)
			// A sample whose model has no rate makes the fixture's cost
			// unknown rather than smaller. Summing CostUSD would report the
			// arm as free, which is the number the whole comparison turns on.
			if out != nil {
				if out.CostKnown {
					cost += out.CostUSD
				} else {
					cost = math.NaN()
				}
			}
			if err != nil {
				t.Errorf("%s: %v", f.Annotation.Name, err)
				continue
			}
			revs = append(revs, out.Review)
			t.Logf("%s: %s", f.Annotation.Name, out.Summary())
		}
		if len(revs) == 0 {
			continue
		}
		sc := ScoreSamples(f, revs)
		cards = append(cards, sc)
		costs = append(costs, cost)
		t.Logf("%s: caught=%v caughtIn=%v missed=%v quiet=%v extra=%d",
			f.Annotation.Name, sc.Caught, sc.CaughtIn, sc.Missed, sc.QuietViolations, sc.Extra)
		// An arm's score says how many extras it wrote and never what they
		// were, so precision work has nothing to read. Keeping the reviews
		// makes the unmatched comments inspectable after the money is spent,
		// which is the only time they can be judged.
		if dir := os.Getenv("REDLINE_EVAL_DUMP"); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			name := f.Annotation.Name
			if os.Getenv("REDLINE_EVAL_NOCONTEXT") != "" {
				name += ".nocontext"
			}
			buf, err := json.MarshalIndent(struct {
				Fixture string            `json:"fixture"`
				Model   string            `json:"model"`
				Caught  []string          `json:"caught"`
				Missed  []string          `json:"missed"`
				Samples []findings.Review `json:"samples"`
			}{f.Annotation.Name, model, sc.Caught, sc.Missed, revs}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".json"), append(buf, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	label := model
	if e := os.Getenv("REDLINE_EVAL_EFFORT"); e != "" {
		label += " effort=" + e
	}
	// The configuration a row is compared under has to be readable from the
	// row: two arms whose only difference is the context, printed under the
	// same name, are not a comparison.
	if os.Getenv("REDLINE_EVAL_NOCONTEXT") != "" {
		label += " nocontext"
	}
	if samples > 1 {
		label += fmt.Sprintf(" ×%d", samples)
	}
	tot := Sum(cards)
	fmt.Println(TableHeader)
	fmt.Println(Table(label, median(costs), tot))
	fmt.Println()
	// The goal is stated against Copilot in both cost and effectiveness, so
	// print it that way. The mean is the cost number that matters: the target
	// is an average across reviews, and a median hides the expensive tail.
	fmt.Println(Scoreboard(label, mean(costs), RatesOf(cards), tot))
}

// mean is NaN when any fixture's cost is, because one unpriced model in the
// arm makes the arm's cost unknown rather than lower. Summation would do this
// on its own; it is spelled out so nobody replaces it with a skip.
func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		if math.IsNaN(x) {
			return math.NaN()
		}
		sum += x
	}
	return sum / float64(len(xs))
}

// median propagates an unknown, for the reason mean does: NaN sorts first in
// sort.Float64s, so a mixed set would quietly return the priced half.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64{}, xs...)
	for _, x := range s {
		if math.IsNaN(x) {
			return math.NaN()
		}
	}
	sort.Float64s(s)
	return s[len(s)/2]
}

func TestRatesMatchThePublishedShape(t *testing.T) {
	cards := []Scorecard{
		{Comments: 0}, {Comments: 0}, {Comments: 4}, {Comments: 6}, {Comments: 5},
	}
	r := RatesOf(cards)
	if r.Reviews != 5 || r.Silent != 2 {
		t.Fatalf("rates = %+v", r)
	}
	if got := r.CleanRate(); got != 0.4 {
		t.Fatalf("clean rate = %.2f, want 0.40", got)
	}
	// 15 comments across the three reviews that said anything, not across
	// all five: averaging the silent ones in measures the clean rate twice.
	if got := r.CommentRate(); got != 5 {
		t.Fatalf("comment rate = %.2f, want 5.0", got)
	}
}

func TestRatesOnAnEmptySetDoNotDivideByZero(t *testing.T) {
	r := RatesOf(nil)
	if r.CleanRate() != 0 || r.CommentRate() != 0 {
		t.Fatal("an empty set has no rates")
	}
	if r := (Rates{Reviews: 3, Silent: 3}); r.CommentRate() != 0 {
		t.Fatal("no review said anything, so there is no comments-per-speaking-review")
	}
}

func TestScoreboardShowsBothHalvesOfTheGoal(t *testing.T) {
	got := Scoreboard("sonnet", 0.31, RatesOf([]Scorecard{{Comments: 0}, {Comments: 5}}), Totals{Caught: 1, Expected: 2})
	for _, want := range []string{"Mean cost", "Clean rate", "Comments per speaking review", "Copilot"} {
		if !strings.Contains(got, want) {
			t.Errorf("scoreboard is missing %q:\n%s", want, got)
		}
	}
}

// The provider layer's central claim is that context beyond the diff is worth
// its cost. This keeps the measurable half of that claim in the suite: a
// provider that regresses into echoing the diff back shows up as a number.
func TestEnvelopeCarriesWhatTheDiffDoesNot(t *testing.T) {
	fx := load(t)
	cs := Contributions(fx)
	t.Log("\n" + ContributionReport(cs))

	var withContext, carrying int
	for _, c := range cs {
		if c.Produced == 0 {
			continue // no provider claimed these files
		}
		withContext++
		if c.Sent > 0 {
			carrying++
		}
		if c.Sent == 0 {
			t.Errorf("%s: the provider produced %d expansions and every one of them "+
				"was already in the diff; that context costs budget and says nothing",
				c.Fixture, c.Produced)
		}
	}
	if withContext == 0 {
		t.Skip("no fixture has a resolved envelope; run make-synthetic.sh with a provider on PATH")
	}
	t.Logf("%d/%d fixtures with an envelope send context the diff does not contain",
		carrying, withContext)
}

// History is the one role a diff can never contain, and the one that catches a
// change undoing a deliberate fix. Deduplication must not eat it.
func TestHistorySurvivesDeduplicationOnTheRevertFixture(t *testing.T) {
	f, err := LoadOne(fixtureDir + "/reverts-a-fix")
	if err != nil {
		t.Fatal(err)
	}
	got, err := review.Assemble(review.Input{
		Report: &f.Session.Report, Change: f.Session.Change,
		Envelopes: f.Session.Envelopes,
	}, review.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "panicked in production") {
		t.Fatal("the commit explaining the deleted guard did not reach the prompt; " +
			"without it this change reads as an ordinary simplification")
	}
}
