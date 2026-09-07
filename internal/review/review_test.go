package review

import (
	"context"
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
