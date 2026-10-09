package review

import (
	"context"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// The describing call can go out on a model of its own. Two things have to
// hold for that to be worth having: the request has to reach the right
// endpoint with the right credential, and what it costs has to reach the
// ledger in a form it can still read. These are the tests for both.

// A review that names no describing endpoint makes the calls it always made.
// This is the regression guard on every run that does not use this feature,
// which is most of them.
func TestAnUnsetDescribingEndpointChangesNothing(t *testing.T) {
	opts := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Effort: "medium",
		BaseURL: "https://proxy.example/v1", APIKey: "k", APIUser: "u",
	}
	// Field by field rather than with ==, because Options carries the
	// progress and debug callbacks and a struct holding a func cannot be
	// compared.
	got := opts.describing()
	if got.API != opts.API || got.Model != opts.Model || got.Effort != opts.Effort ||
		got.BaseURL != opts.BaseURL || got.APIKey != opts.APIKey || got.APIUser != opts.APIUser {
		t.Errorf("an unset describing endpoint changed the options:\n got %+v\nwant %+v", got, opts)
	}
}

// Naming a model alone moves that one call. The wire, the endpoint, the
// credential and the effort stay where the review put them, because a cheaper
// model on the same vendor is the simplest version of this and it should need
// one flag.
func TestDescribingModelAloneMovesOnlyTheModel(t *testing.T) {
	opts := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Effort: "medium",
		BaseURL: "https://proxy.example/v1", APIKey: "k", APIUser: "u",
		Describing: Endpoint{Model: "claude-haiku-4"},
	}
	d := opts.describing()
	if d.Model != "claude-haiku-4" {
		t.Errorf("the describing call ran on %q, not the model it was given", d.Model)
	}
	if d.API != opts.API || d.BaseURL != opts.BaseURL || d.APIKey != opts.APIKey ||
		d.APIUser != opts.APIUser || d.Effort != opts.Effort {
		t.Errorf("naming a model moved something else too:\n got %+v\nwant the judging call's wire", d)
	}
}

// Switching wires drops the endpoint, the key and the caller name rather than
// carrying them across. Sending the judging call's Anthropic base URL and key
// to an OpenAI endpoint is a request to the wrong place with a credential it
// will not accept, and it fails after the whole prompt has been uploaded.
func TestSwitchingWiresDropsTheEndpointAndCredential(t *testing.T) {
	opts := Options{
		API: APIAnthropic, Model: "claude-sonnet-5",
		BaseURL: "https://anthropic-proxy.example", APIKey: "anthropic-key", APIUser: "u",
		Describing: Endpoint{API: APIOpenAI},
	}
	d := opts.describing()
	if d.BaseURL != "" || d.APIKey != "" || d.APIUser != "" {
		t.Errorf("the judging call's endpoint or credential survived a change of wire: %+v", d)
	}
	// And the model goes with them: the judging model's name means nothing on
	// the wire that did not serve it.
	if d.Model != DefaultOpenAIModel {
		t.Errorf("a describing call that switched wires kept %q, not the new wire's default", d.Model)
	}
}

// A wire switch that also names a model uses that model, and takes the
// endpoint and credential given for it.
func TestSwitchingWiresTakesTheEndpointItIsGiven(t *testing.T) {
	opts := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", APIKey: "anthropic-key",
		Describing: Endpoint{
			API: APIOpenAI, Model: "gpt-5-mini",
			BaseURL: "https://openai-proxy.example/v1", APIKey: "openai-key",
		},
	}
	d := opts.describing()
	if d.API != APIOpenAI || d.Model != "gpt-5-mini" ||
		d.BaseURL != "https://openai-proxy.example/v1" || d.APIKey != "openai-key" {
		t.Errorf("the describing call did not go out on the endpoint it was given: %+v", d)
	}
}

// A bare credential names no endpoint. Without this an exported key in the
// environment, or a caller filling the field defensively, would move a call
// nobody asked to move.
func TestACredentialAloneIsNotADescribingEndpoint(t *testing.T) {
	opts := Options{
		API: APIAnthropic, Model: "claude-sonnet-5",
		Describing: Endpoint{APIKey: "stray"},
	}
	if got := opts.describing(); got.Model != opts.Model || got.APIKey != opts.APIKey {
		t.Errorf("a stray key moved the describing call: %+v", got)
	}
}

// The describing call's tokens stay out of Usage when another model spent
// them, and its cost is carried across as a number instead. Usage is a token
// count with no model attached, so folding them in would bill them at the
// judging model's rate.
func TestAnotherModelsDescribingCallIsPricedWhereItRan(t *testing.T) {
	res := &Result{Usage: Usage{InputTokens: 100_000, OutputTokens: 4_000}}
	applySynopsis(res, "claude-sonnet-5", described{
		Walkthrough: reviewWithOverview("what the change does"),
		Usage:       Usage{InputTokens: 200_000},
		Written:     3_000,
		Model:       "gpt-5-mini",
		CostUSD:     0.056,
		CostKnown:   true,
	})
	if res.Usage.InputTokens != 100_000 {
		t.Errorf("the describing call's input joined the judging call's usage: %d", res.Usage.InputTokens)
	}
	if res.SynopsisModel != "gpt-5-mini" {
		t.Errorf("the walkthrough's model was not recorded: %q", res.SynopsisModel)
	}
	if res.SynopsisCostUSD != 0.056 {
		t.Errorf("the describing call's cost was not carried across: %v", res.SynopsisCostUSD)
	}
	// 100k input and 4k output on Sonnet 5, plus the describing call's own
	// price. The walkthrough's 3k output is not in it: that call was not
	// billed at this model's rates.
	judging, _ := Usage{InputTokens: 100_000, OutputTokens: 4_000}.Cost("claude-sonnet-5")
	if want := judging + 0.056; !closeTo(res.CostUSD, want) {
		t.Errorf("the run cost %v, want %v", res.CostUSD, want)
	}
	if !res.CostKnown {
		t.Error("both models are priced, so the total is known")
	}
}

// On one model nothing moves: the tokens join Usage and the walkthrough's
// output is priced with the rest, which is what this did before a second
// model was reachable.
func TestOneModelPricesTheDescribingCallWithTheRest(t *testing.T) {
	res := &Result{Usage: Usage{InputTokens: 100_000, OutputTokens: 4_000}}
	applySynopsis(res, "claude-sonnet-5", described{
		Walkthrough: reviewWithOverview("what the change does"),
		Usage:       Usage{InputTokens: 200_000},
		Written:     3_000,
		Model:       "claude-sonnet-5",
		CostUSD:     0.23,
		CostKnown:   true,
	})
	if res.Usage.InputTokens != 300_000 {
		t.Errorf("the describing call's input did not join the usage: %d", res.Usage.InputTokens)
	}
	if res.SynopsisCostUSD != 0 {
		t.Errorf("a same-model describing call was priced twice: %v", res.SynopsisCostUSD)
	}
	want, _ := Usage{InputTokens: 300_000, OutputTokens: 7_000}.Cost("claude-sonnet-5")
	if !closeTo(res.CostUSD, want) {
		t.Errorf("the run cost %v, want %v", res.CostUSD, want)
	}
}

// A describing model with no entry in the price table makes the whole run's
// cost unknown. The alternative is a confident total that is missing one of
// its two calls, which is the one thing the price table's own comment says it
// will not do.
func TestAnUnpricedDescribingModelMakesTheTotalUnknown(t *testing.T) {
	// A name no row can ever match, rather than a real model that is unpriced
	// today. The first version of this test named one, and adding its rate
	// turned the test into a skip: it went on passing and stopped checking
	// anything.
	const unpriced = "a-model-this-table-has-never-heard-of"
	if _, ok := LookupPricing(unpriced); ok {
		t.Fatalf("%q resolved to a rate; this test needs a name that cannot", unpriced)
	}
	res := &Result{Usage: Usage{InputTokens: 100_000, OutputTokens: 4_000}}
	applySynopsis(res, "claude-sonnet-5", described{
		Walkthrough: reviewWithOverview("what the change does"),
		Usage:       Usage{InputTokens: 200_000},
		Written:     3_000,
		Model:       unpriced,
	})
	if res.CostKnown {
		t.Error("one of the two calls ran on a model with no price, so the total is not known")
	}
}

// A failed describing call is still billed, and on another model it is still
// billed there. Losing it would make a run that paid for two calls read as a
// run that paid for one.
func TestAFailedDescribingCallOnAnotherModelStillCosts(t *testing.T) {
	res := &Result{Usage: Usage{InputTokens: 100_000, OutputTokens: 4_000}}
	applySynopsis(res, "claude-sonnet-5", described{
		Usage:     Usage{InputTokens: 200_000},
		Written:   120,
		Model:     "gpt-5-mini",
		CostUSD:   0.05,
		CostKnown: true,
		Failed:    "the describing call returned no overview",
	})
	if res.Synopsis {
		t.Error("a failed describing call was recorded as having written the walkthrough")
	}
	if res.SynopsisCostUSD != 0.05 {
		t.Errorf("a failed describing call's cost was dropped: %v", res.SynopsisCostUSD)
	}
	judging, _ := Usage{InputTokens: 100_000, OutputTokens: 4_000}.Cost("claude-sonnet-5")
	if want := judging + 0.05; !closeTo(res.CostUSD, want) {
		t.Errorf("the run cost %v, want %v", res.CostUSD, want)
	}
}

func closeTo(a, b float64) bool {
	const epsilon = 1e-9
	d := a - b
	return d < epsilon && d > -epsilon
}

// reviewWithOverview is a walkthrough that describe would call successful:
// an overview and a line for one file.
func reviewWithOverview(overview string) findings.Review {
	return findings.Review{
		Overview: overview,
		Files:    map[string]string{"internal/review/review.go": "what this file does"},
	}
}

// An unknown wire for the describing call is refused rather than sent. runOnce
// treats anything that is not the OpenAI wire as Anthropic's, so without this
// a typo asks Anthropic for a model it does not serve and the describing stage
// reports it as a call that produced no walkthrough.
func TestAnUnknownDescribingWireIsRefused(t *testing.T) {
	_, err := Run(context.Background(), Input{}, Options{
		Model: "claude-sonnet-5", Describing: Endpoint{API: "openal"},
	})
	if err == nil || !strings.Contains(err.Error(), "describing call") {
		t.Errorf("a typo in the describing wire was not refused: %v", err)
	}
}

// Luna resolves to its own row and not to gpt-5's. The component guard in
// LookupPricing is what stops "gpt-5.6-luna" borrowing "gpt-5"'s rate, and a
// borrowed rate here would misprice every describing call by a factor of
// several in both directions.
func TestLunaResolvesToItsOwnRate(t *testing.T) {
	p, ok := LookupPricing("gpt-5.6-luna")
	if !ok {
		t.Fatal("gpt-5.6-luna has no rate")
	}
	if p.InPerM != 0.20 || p.OutPerM != 1.20 {
		t.Errorf("gpt-5.6-luna priced at %+v, not the rate it was given", p)
	}
	// The direction is asserted as well as the pair, because the two numbers
	// arrived transposed once and every cost line downstream depends on them
	// being the right way round.
	if p.OutPerM <= p.InPerM {
		t.Errorf("gpt-5.6-luna's output is not above its input: %+v", p)
	}
	if base, _ := LookupPricing("gpt-5"); p == base {
		t.Error("gpt-5.6-luna resolved to gpt-5's rate through the prefix match")
	}
}

// A reused walkthrough keeps the commit and files its recap was written
// against. Dropping them left review.json with the old paragraph and no
// baseline, after a --reuse-synopsis run that could not pass --since.
func TestAReusedRecapKeepsItsBaseline(t *testing.T) {
	w := reviewWithOverview("what the change does")
	w.Recap, w.RecapSince, w.RecapFiles = "the loop moved", "abc123", []string{"a.go"}
	res := &Result{}
	applySynopsis(res, "claude-sonnet-5", described{Walkthrough: w, Model: "claude-sonnet-5"})
	if res.Review.Recap != "the loop moved" || res.Review.RecapSince != "abc123" ||
		len(res.Review.RecapFiles) != 1 || res.Review.RecapFiles[0] != "a.go" {
		t.Errorf("the reused recap lost its baseline: %+v", res.Review)
	}
}
