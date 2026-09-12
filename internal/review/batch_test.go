package review

import (
	"context"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
)

// batchInput is the smallest thing RunBatch will assemble: one changed file
// with a diff, and an empty report, so the guards below are reached rather
// than short-circuited by an input that could not have been sent anyway.
func batchInput() Input {
	return Input{
		Report: &findings.Report{},
		Change: &change.Set{Files: []change.File{{
			Path: "internal/foo.go", Status: "modified", Added: 1,
			Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n",
		}}},
	}
}

// The guards exist so a caller learns on the spot that what they asked for has
// no batched form, rather than paying for a batch that answers a different
// question than the one they meant. Each is checked by the shape of its
// message, because the message is the whole value of failing early.
func TestRunBatchRefusesWhatCannotBeBatched(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"explore", Options{Mode: ModeExplore}, "single-shot"},
		{"samples", Options{Samples: 3}, "--samples"},
		{"verify", Options{Verify: true}, "checking pass"},
		{"openai", Options{API: APIOpenAI}, "only implemented against the Anthropic API"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := RunBatch(context.Background(), []Input{batchInput()}, tc.opts)
			if err == nil {
				t.Fatalf("wanted a refusal mentioning %q, got none", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wanted a refusal mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

// Nothing to review is not an error. A sweep narrowed to no fixtures should
// cost nothing and say nothing, not fail.
func TestRunBatchEmptyIsNotAnError(t *testing.T) {
	res, errs, err := RunBatch(context.Background(), nil, Options{})
	if err != nil || res != nil || errs != nil {
		t.Fatalf("wanted nothing back for no inputs, got %v %v %v", res, errs, err)
	}
}

// The tripwire has to fire before the batch is submitted, not after: a batch
// is billed as a whole and there is no refund for noticing on the way out.
// It is also the one guard that must be priced at the batch tier, since that
// is the rate the request will actually bill at.
func TestRunBatchPricesTheTripwireAtTheBatchRate(t *testing.T) {
	in := batchInput()
	// A cap this large makes one request's ceiling cost exceed any sane
	// tripwire at full rate. Half of it is what the guard must compare.
	opts := Options{Model: "claude-sonnet-5", MaxTokens: 100_000}

	full, ok := CeilingCost(opts.Model, 1000, opts.MaxTokens)
	if !ok {
		t.Fatal("the test model should be in the price table")
	}
	// Just under half the full-rate ceiling: refused if the guard prices at
	// full rate, allowed if it prices at the batch rate. Allowed here means
	// the call gets as far as the wire, which this test does not want, so
	// the assertion is on the refusal that does happen.
	opts.MaxCostUSD = full * BatchDiscount * 0.9
	_, _, err := RunBatch(context.Background(), []Input{in}, opts)
	if err == nil || !strings.Contains(err.Error(), "tripwire") {
		t.Fatalf("wanted the tripwire to fire just under the batch-rate ceiling, got %v", err)
	}
	if strings.Contains(err.Error(), FormatCost(full, true)) {
		t.Fatalf("the tripwire quoted the full-rate ceiling %s; a batched request bills at half that",
			FormatCost(full, true))
	}
}

// A batched row is half-price by construction, so folding it into the
// distribution would report the tool as having got cheaper for the person
// waiting on a review. Summarize counts those rows and leaves them out.
func TestSummarizeExcludesBatchedRuns(t *testing.T) {
	entries := []Entry{
		{Known: true, CostUSD: 1.00},
		{Known: true, CostUSD: 1.00},
		{Known: true, CostUSD: 0.10, Batched: true},
	}
	s := Summarize(entries)
	if s.Count != 2 {
		t.Fatalf("wanted 2 interactive reviews counted, got %d", s.Count)
	}
	if s.Batched != 1 {
		t.Fatalf("wanted 1 batched review named, got %d", s.Batched)
	}
	if s.Mean != 1.00 {
		t.Fatalf("the batched row dragged the mean to %.4f", s.Mean)
	}
	if !strings.Contains(s.String(), "batch tier") {
		t.Fatalf("the summary should say what was excluded: %q", s.String())
	}
}

// The cap is the difference between a review that is short and one that was
// cut off, and those cost the same. A ledger that cannot tell them apart
// cannot say whether a zero-finding run was a clean pass.
func TestSummarizeNamesTruncatedRuns(t *testing.T) {
	s := Summarize([]Entry{
		{Known: true, CostUSD: 1.00},
		{Known: true, CostUSD: 1.00, Truncated: true},
	})
	if s.Truncated != 1 {
		t.Fatalf("wanted 1 truncated review counted, got %d", s.Truncated)
	}
	if !strings.Contains(s.String(), "output cap") {
		t.Fatalf("the summary should name the cap: %q", s.String())
	}
}
