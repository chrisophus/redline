package review

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// batchServer answers the two calls RunBatch makes: the submit, which reports
// a batch that has already ended so the poll loop is skipped, and the results,
// which come back as one JSON object per line.
func batchServer(t *testing.T, results ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/results") {
			for _, line := range results {
				_, _ = io.WriteString(w, line+"\n")
			}
			return
		}
		_, _ = io.WriteString(w, `{"id":"batch_1","type":"message_batch","processing_status":"ended"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// batchResult renders one finished request, with the review body as the text
// the model returned.
func batchResult(customID, body string) string {
	msg := map[string]any{
		"id": "msg_" + customID, "type": "message", "role": "assistant",
		"model": "claude-sonnet-5", "stop_reason": "end_turn",
		"content": []any{map[string]any{"type": "text", "text": body}},
		"usage":   map[string]any{"input_tokens": 1000, "output_tokens": 100},
	}
	b, _ := json.Marshal(map[string]any{
		"custom_id": customID,
		"result":    map[string]any{"type": "succeeded", "message": msg},
	})
	return string(b)
}

// The endpoint returns results in whatever order they finished, so the one
// invariant of this file is that every result is placed by its custom id.
// Reversed results are the cheapest way to hold it to that: paired by
// position, every sweep would score each fixture against another's review.
func TestRunBatchPlacesResultsByCustomIDNotPosition(t *testing.T) {
	first := strings.Replace(reviewBody, "Adds a thing.", "the review of request zero", 1)
	second := strings.Replace(reviewBody, "Adds a thing.", "the review of request one", 1)
	srv := batchServer(t,
		batchResult("1", second),
		batchResult("0", first),
	)
	opts := Options{Model: "claude-sonnet-5", APIKey: "test", BaseURL: srv.URL, MaxCostUSD: 100}

	res, errs, err := RunBatch(context.Background(), []Input{batchInput(), batchInput()}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range errs {
		if e != nil {
			t.Fatalf("request %d came back with %v", i, e)
		}
	}
	if got := res[0].Review.Overview; got != "the review of request zero" {
		t.Errorf("slot 0 holds %q", got)
	}
	if got := res[1].Review.Overview; got != "the review of request one" {
		t.Errorf("slot 1 holds %q", got)
	}
	// Billed at the tier that ran it, and reported as such.
	if !res[0].Batched {
		t.Error("a batched result does not say it was batched, so the ledger counts it as interactive")
	}
	full, _ := (Usage{InputTokens: 1000, OutputTokens: 100}).Cost(opts.Model)
	if res[0].CostUSD != full*BatchDiscount {
		t.Errorf("cost = %v, want the batch tier's half of %v", res[0].CostUSD, full)
	}
}

// One request failing is not the batch failing: the others were paid for. A
// canceled or expired result carries no message, and a reason nobody gave has
// to read as one nobody gave rather than as an empty error.
func TestRunBatchReportsAFailedRequestInItsOwnSlot(t *testing.T) {
	srv := batchServer(t,
		batchResult("0", reviewBody),
		`{"custom_id":"1","result":{"type":"canceled"}}`,
	)
	opts := Options{Model: "claude-sonnet-5", APIKey: "test", BaseURL: srv.URL, MaxCostUSD: 100}

	res, errs, err := RunBatch(context.Background(), []Input{batchInput(), batchInput()}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if errs[0] != nil {
		t.Errorf("the request that succeeded was failed by the one that did not: %v", errs[0])
	}
	if res[0].Review.Overview == "" {
		t.Error("the succeeded request lost its review")
	}
	if errs[1] == nil {
		t.Fatal("a canceled request came back as a review")
	}
	for _, want := range []string{"canceled", "no reason"} {
		if !strings.Contains(errs[1].Error(), want) {
			t.Errorf("the failure does not say %q: %v", want, errs[1])
		}
	}
}

// A result for a request nobody sent means the pairing cannot be trusted at
// all, and a slot with no result at all is not silently a clean review.
func TestRunBatchRefusesAnUnknownCustomIDAndNamesAMissingOne(t *testing.T) {
	opts := Options{Model: "claude-sonnet-5", APIKey: "test", BaseURL: "", MaxCostUSD: 100}

	opts.BaseURL = batchServer(t, batchResult("7", reviewBody)).URL
	if _, _, err := RunBatch(context.Background(), []Input{batchInput()}, opts); err == nil ||
		!strings.Contains(err.Error(), "custom_id") {
		t.Fatalf("a result for a request nobody sent was accepted: %v", err)
	}

	opts.BaseURL = batchServer(t, batchResult("0", reviewBody)).URL
	_, errs, err := RunBatch(context.Background(), []Input{batchInput(), batchInput()}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if errs[1] == nil || !strings.Contains(errs[1].Error(), "no result") {
		t.Fatalf("a request with no result came back clean: %v", errs[1])
	}
}
