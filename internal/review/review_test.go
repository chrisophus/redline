package review

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

func priors() *findings.Report {
	r := &findings.Report{Findings: []findings.Finding{
		{Rule: "migration-add-not-null-no-default", Substrate: "redline/sql",
			File: "m/0002.up.sql", Category: findings.CategorySchema,
			Severity: findings.SeverityWarning,
			Message:  "adds NOT NULL column users.tenant_id with no default",
			Observed: "ADD COLUMN tenant_id uuid NOT NULL"},
		{Rule: "agent-comment", Substrate: "redline/review", File: "a.go", Line: 2,
			Category: findings.CategoryReview, Severity: findings.SeverityInfo,
			Message: "a previous reviewer said this", Source: findings.SourceLLM},
	}}
	r.Finalize()
	return r
}

func TestPromptGivesPriorsAPrintableId(t *testing.T) {
	in := Input{Report: priors()}
	got, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(got.Prompt, 0) {
		t.Fatal("the prompt carries NUL bytes; a model cannot echo a raw fingerprint back")
	}
	id := priors().Findings[0].ID
	if id == "" || !strings.Contains(got.Prompt, "["+id+"]") {
		t.Fatalf("prior finding id %q is not referenceable in the prompt", id)
	}
}

func TestPromptDoesNotPresentAPriorReviewAsEstablishedFact(t *testing.T) {
	got, err := Assemble(Input{Report: priors()}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Prompt, "a previous reviewer said this") {
		t.Fatal("another reviewer's remark is not an established fact and must not be shown as one")
	}
}

func TestPromptSaysWhenNothingWasEstablished(t *testing.T) {
	got, err := Assemble(Input{Report: &findings.Report{}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "None.") {
		t.Fatal("an empty prior list must be stated, not omitted")
	}
}

func TestPromptNamesProducersThatDidNotRun(t *testing.T) {
	got, err := Assemble(Input{Absent: []string{"gorefactor (context): not on PATH"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "did not run") || !strings.Contains(got.Prompt, "not a pass") {
		t.Fatal("a missing input must be distinguishable from a clean result")
	}
}

func TestPromptSummarizesGeneratedFilesInsteadOfPastingThem(t *testing.T) {
	rep := &findings.Report{}
	rep.Coverage.Generated = []string{"api.pb.go"}
	got, err := Assemble(Input{Report: rep}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "api.pb.go") {
		t.Fatal("an exclusion the reader cannot see is indistinguishable from a file that never changed")
	}
	if strings.Contains(got.Prompt, "```diff") {
		t.Fatal("generated content must not be pasted in")
	}
}

func TestContextBlockIsBudgetedAndLabelled(t *testing.T) {
	env := &envelope.Envelope{
		SchemaVersion: envelope.SchemaVersion,
		Provider:      envelope.Provider{Name: "prov", Version: "v9", Language: "go"},
		Expansions: []envelope.Expansion{
			{Role: envelope.RoleCaller, Symbol: "Caller", File: "c.go",
				StartLine: 3, EndLine: 4, Content: "callIt()"},
		},
	}
	got, err := Assemble(Input{Envelopes: []*envelope.Envelope{env}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "prov v9") {
		t.Fatal("the reader of a fixture must be able to tell which provider resolved it")
	}
	if !strings.Contains(got.Prompt, "caller") {
		t.Fatal("the model must be told a caller is a caller")
	}
}

func TestAssembleIsDeterministic(t *testing.T) {
	in := Input{Report: priors(), Change: &change.Set{Files: []change.File{
		{Path: "a.go", Status: "modified", Added: 1, Diff: "+x"},
	}}}
	first, _ := Assemble(in, Options{})
	for i := 0; i < 10; i++ {
		got, _ := Assemble(in, Options{})
		if got.Prompt != first.Prompt {
			t.Fatal("the same session must assemble the same prompt; a fixture cannot replay otherwise")
		}
	}
}

func TestTripwireRefusesBeforeSpending(t *testing.T) {
	big := strings.Repeat("x ", 400_000)
	in := Input{Change: &change.Set{Files: []change.File{{Path: "a.go", Diff: big}}}}
	_, err := Run(context.Background(), in, Options{MaxCostUSD: 0.01})
	if err == nil {
		t.Fatal("a request estimated above the tripwire must be refused before it is sent")
	}
	if !strings.Contains(err.Error(), "tripwire") {
		t.Fatalf("error should name the tripwire, got %v", err)
	}
}

func TestDryRunCallsNothing(t *testing.T) {
	got, err := Run(context.Background(), Input{Report: &findings.Report{}}, Options{DryRun: true})
	if err != nil {
		t.Fatalf("a dry run must work with no credentials at all: %v", err)
	}
	if got.Prompt == "" {
		t.Fatal("a dry run exists to show exactly what would be sent")
	}
	if got.Usage.InputTokens != 0 {
		t.Fatal("a dry run must not report usage it did not incur")
	}
}

func TestDefaultModelIsTheOneTheCostTargetWasBuiltOn(t *testing.T) {
	got, _ := Assemble(Input{}, Options{})
	if got.Model != DefaultModel {
		t.Fatalf("model = %q", got.Model)
	}
	if _, ok := LookupPricing(got.Model); !ok {
		t.Fatal("the default model must have a rate, or every run prints an unknown cost")
	}
}

func TestCostKeepsTokenTypesApart(t *testing.T) {
	u := Usage{InputTokens: 1_000_000}
	plain, _ := u.Cost("claude-sonnet-5")
	cached, _ := Usage{CacheReadTokens: 1_000_000}.Cost("claude-sonnet-5")
	if cached >= plain {
		t.Fatal("a cache read must not price the same as fresh input, or a caching event reads as a context-size event")
	}
	written, _ := Usage{CacheWriteTokens: 1_000_000}.Cost("claude-sonnet-5")
	if written <= plain {
		t.Fatal("a cache write costs above base input")
	}
}

func TestUnknownModelPricesAsUnknownNotFree(t *testing.T) {
	_, ok := Usage{InputTokens: 100}.Cost("some-other-model")
	if ok {
		t.Fatal("an unknown model must not price at zero")
	}
	if !strings.Contains(FormatCost(0, false), "?") {
		t.Fatal("an unknown cost must render as unknown")
	}
}

func TestMergeKeepsTheJudgmentsAlreadyInTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	existing := `{"overview":"old","verdicts":{"fp1":{"ruling":"justified"}},
	"mutationVerdicts":{"a.go:1:X":{"ruling":"needs-test"}}}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := findings.Review{Overview: "new", Comments: []findings.ReviewComment{
		{File: "a.go", Line: 1, Body: "fresh remark"},
	}}
	if err := Merge(path, fresh); err != nil {
		t.Fatal(err)
	}
	got, err := findings.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Overview != "new" || len(got.Comments) != 1 {
		t.Fatal("the prose and comments are replaced by the new review")
	}
	if len(got.Verdicts) != 1 {
		t.Fatal("a verdict a reviewer recorded must survive a re-review; overwriting it discards their work")
	}
	if len(got.MutationVerdicts) != 1 {
		t.Fatal("mutation verdicts must survive too")
	}
}

func TestMergeIntoAFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	if err := Merge(path, findings.Review{Overview: "x"}); err != nil {
		t.Fatal(err)
	}
	got, err := findings.LoadReview(path)
	if err != nil || got == nil || got.Overview != "x" {
		t.Fatalf("review = %v, err = %v", got, err)
	}
}

func TestSchemaForcesEveryFieldToBePresent(t *testing.T) {
	s := outputSchema()
	req, _ := s["required"].([]string)
	if len(req) != 3 {
		t.Fatalf("required = %v", req)
	}
	props := s["properties"].(map[string]any)
	comments := props["comments"].(map[string]any)
	item := comments["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Fatal("a comment must not carry fields the contract does not define")
	}
	itemReq := item["required"].([]string)
	var hasRelated, hasConfidence bool
	for _, r := range itemReq {
		if r == "relatedFindings" {
			hasRelated = true
		}
		if r == "confidence" {
			hasConfidence = true
		}
	}
	if !hasRelated || !hasConfidence {
		t.Fatal("the fields that carry the correlation and the fold must be required, or they get dropped quietly")
	}
}

// The ceiling has to bound the whole request. A ceiling that governed only
// the context block would let a large diff carry the total anywhere, which is
// what happened the first time this was wired to a real provider: 205k input
// tokens against a 120k ceiling.
func TestCeilingBoundsTheWholeRequestNotJustTheContext(t *testing.T) {
	var xs []envelope.Expansion
	for i := 0; i < 200; i++ {
		xs = append(xs, envelope.Expansion{
			Role: envelope.RoleCaller, File: "c.go", StartLine: i,
			Content: strings.Repeat("callIt()\n", 200),
		})
	}
	in := Input{
		Change:    &change.Set{Files: []change.File{{Path: "a.go", Diff: strings.Repeat("+line\n", 2000)}}},
		Envelopes: []*envelope.Envelope{{Expansions: xs}},
	}
	got, err := Assemble(in, Options{Ceiling: 20000})
	if err != nil {
		t.Fatal(err)
	}
	if got.InputEstimate > 20000 {
		t.Fatalf("input estimate %d exceeds the 20000 ceiling; cost per review is not a constant",
			got.InputEstimate)
	}
	if got.Budget.DroppedTotal() == 0 {
		t.Fatal("context should have been trimmed to fit alongside the diff")
	}
}

// A diff bigger than the whole ceiling is reported, not silently trimmed. A
// review of a diff with the middle cut out is worse than an honest refusal.
func TestADiffLargerThanTheCeilingIsReported(t *testing.T) {
	in := Input{
		Change: &change.Set{Files: []change.File{{Path: "a.go", Diff: strings.Repeat("+line\n", 20000)}}},
		Envelopes: []*envelope.Envelope{{Expansions: []envelope.Expansion{
			{Role: envelope.RoleEnclosing, Content: "func F() {}"},
		}}},
	}
	got, err := Assemble(in, Options{Ceiling: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if !got.OverCeiling {
		t.Fatal("a change too large for one review must say so")
	}
	if got.ContextRoom != 0 {
		t.Fatalf("no context can fit, got room for %d tokens", got.ContextRoom)
	}
	if len(got.Budget.Kept) != 0 {
		t.Fatal("nothing may be kept when there is no room")
	}
	if !strings.Contains(got.Prompt, "+line") {
		t.Fatal("the diff itself is never dropped; a review without it is not a review")
	}
}

func TestContextFitsWhenThereIsRoom(t *testing.T) {
	in := Input{
		Change: &change.Set{Files: []change.File{{Path: "a.go", Diff: "+one line\n"}}},
		Envelopes: []*envelope.Envelope{{Expansions: []envelope.Expansion{
			{Role: envelope.RoleCaller, File: "c.go", StartLine: 1, EndLine: 1, Content: "callIt()"},
		}}},
	}
	got, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.OverCeiling {
		t.Fatal("a small change is not over the ceiling")
	}
	if len(got.Budget.Kept) != 1 || !strings.Contains(got.Prompt, "callIt()") {
		t.Fatal("context that fits must be sent")
	}
}

// The target is an average across reviews, not a cap on each one. These pin
// the arithmetic that claim rests on.
func TestLedgerRecordsAndAverages(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []float64{0.05, 0.10, 0.90, 0.20, 0.25} {
		r := &Result{Model: "claude-sonnet-5", CostUSD: c, CostKnown: true,
			Duration: 2 * 1e9, Usage: Usage{InputTokens: 1000}}
		if err := Record(dir, r, "high"); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ReadLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("entries = %d", len(entries))
	}
	s := Summarize(entries)
	if s.Count != 5 {
		t.Fatalf("count = %d", s.Count)
	}
	if got := s.Mean; got < 0.29 || got > 0.31 {
		t.Fatalf("mean = %.4f, want 0.30", got)
	}
	if s.Median != 0.20 {
		t.Fatalf("median = %.4f, want 0.20", s.Median)
	}
	// The tail has to stay visible beside the average: one expensive review
	// among cheap ones is exactly what a mean alone hides.
	if s.Max != 0.90 {
		t.Fatalf("max = %.4f", s.Max)
	}
	if s.Min != 0.05 {
		t.Fatalf("min = %.4f", s.Min)
	}
}

func TestUnknownRatesAreExcludedNotCountedAsZero(t *testing.T) {
	dir := t.TempDir()
	if err := Record(dir, &Result{Model: "claude-sonnet-5", CostUSD: 0.40, CostKnown: true}, ""); err != nil {
		t.Fatal(err)
	}
	if err := Record(dir, &Result{Model: "mystery", CostKnown: false}, ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := ReadLedger(dir)
	s := Summarize(entries)
	if s.Count != 1 || s.Unknown != 1 {
		t.Fatalf("count/unknown = %d/%d", s.Count, s.Unknown)
	}
	if s.Mean != 0.40 {
		t.Fatalf("mean = %.4f: an unpriced review must not drag the average down as a zero", s.Mean)
	}
	if !strings.Contains(s.String(), "no rate") {
		t.Fatalf("the excluded reviews must be named, got %q", s.String())
	}
}

func TestEmptyLedgerSaysSoRatherThanReportingZero(t *testing.T) {
	entries, err := ReadLedger(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := Summarize(entries).String(); !strings.Contains(got, "no reviews") {
		t.Fatalf("got %q", got)
	}
}

// A small change must be cheap. If the ceiling were also the budget, every
// review would cost the same and the average would be the cap.
func TestASmallChangeCostsFarLessThanTheCeiling(t *testing.T) {
	in := Input{Change: &change.Set{Files: []change.File{
		{Path: "a.go", Status: "modified", Added: 2, Diff: "+two\n+lines\n"},
	}}}
	got, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.InputEstimate > 5000 {
		t.Fatalf("a two-line change assembled %d input tokens", got.InputEstimate)
	}
	cost, ok := EstimateCost(got.Model, got.InputEstimate, 4000)
	if !ok || cost > 0.10 {
		t.Fatalf("a two-line change should cost cents, got %s", FormatCost(cost, ok))
	}
}

func TestExploreCatalogueCostsNothingUntilAsked(t *testing.T) {
	kept := []envelope.Expansion{
		{Role: envelope.RoleHistory, Symbol: "Drain", File: "q.go", StartLine: 6, EndLine: 6,
			Content: strings.Repeat("commit log line\n", 400)},
		{Role: envelope.RoleCaller, Symbol: "Drain", File: "c.go", StartLine: 10, EndLine: 12,
			Content: strings.Repeat("callIt()\n", 400)},
	}
	cat := catalogue(kept)
	if strings.Contains(cat, "commit log line") || strings.Contains(cat, "callIt()") {
		t.Fatal("the catalogue must list what is available without sending it")
	}
	for _, want := range []string{"e0", "e1", "history", "caller", "q.go:6-6"} {
		if !strings.Contains(cat, want) {
			t.Errorf("catalogue is missing %q", want)
		}
	}
	if got := envelope.EstimateTokens(cat); got > 200 {
		t.Fatalf("a two-entry catalogue cost %d tokens; it must stay cheap enough to always send", got)
	}
}

func TestFetchReturnsOnlyWhatWasAskedFor(t *testing.T) {
	kept := []envelope.Expansion{
		{Role: envelope.RoleHistory, Symbol: "A", File: "a.go", Content: "history body"},
		{Role: envelope.RoleCaller, Symbol: "B", File: "b.go", Content: "caller body"},
	}
	got := fetch(kept, []string{"e1"})
	if !strings.Contains(got, "caller body") {
		t.Fatal("the requested entry must come back in full")
	}
	if strings.Contains(got, "history body") {
		t.Fatal("fetching one entry must not send the others")
	}
}

func TestFetchIsHonestAboutBadIds(t *testing.T) {
	kept := []envelope.Expansion{{Role: envelope.RoleCaller, Content: "x"}}
	got := fetch(kept, []string{"e9", "nonsense"})
	if !strings.Contains(got, "no such id") {
		t.Fatalf("an id that does not exist must say so rather than return nothing: %q", got)
	}
}

func TestFetchDeduplicatesRepeatedIds(t *testing.T) {
	kept := []envelope.Expansion{{Role: envelope.RoleCaller, Symbol: "A", Content: "body"}}
	got := fetch(kept, []string{"e0", "e0", "e0"})
	if strings.Count(got, "body") != 1 {
		t.Fatal("asking twice must not be charged twice")
	}
}

// The cap is a governor in explore mode, not a tripwire: the point of the mode
// is to spend more, so what stops it has to be the limit itself.
func TestExploreCapCountsTheResendThatHasNotHappenedYet(t *testing.T) {
	res := &Result{CostUSD: 0.30, CostKnown: true}
	msg := &anthropic.BetaMessage{}
	msg.Usage.InputTokens = 100_000
	msg.Usage.OutputTokens = 1_000
	// Another turn resends 101k tokens, about 20 cents on Sonnet, which would
	// take 0.30 past a 0.50 cap.
	if !capReached(res, Options{Model: "claude-sonnet-5", MaxCostUSD: 0.50}, msg) {
		t.Fatal("the cap must account for the resend the next turn would pay for")
	}
	if capReached(res, Options{Model: "claude-sonnet-5", MaxCostUSD: 2.00}, msg) {
		t.Fatal("a cap with room left must not stop the loop")
	}
}

func TestExploreCapDoesNotFireOnAnUnpricedModel(t *testing.T) {
	res := &Result{CostUSD: 0, CostKnown: false}
	msg := &anthropic.BetaMessage{}
	if capReached(res, Options{Model: "mystery", MaxCostUSD: 0.01}, msg) {
		t.Fatal("a model with no rate cannot be governed by a dollar cap; do not pretend otherwise")
	}
}

func TestModeDefaultsToOneShot(t *testing.T) {
	if got := (Options{}).withDefaults(); got.Mode != ModeOneShot {
		t.Fatalf("mode = %q; explore costs more and must be asked for", got.Mode)
	}
}
