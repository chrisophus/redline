package review

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// The catalogue carries the three calls that write a walkthrough on every run
// but the one where nothing sharing it writes one. That saves 855 tokens,
// which is not the point: the point is that a findings pass offered
// set_overview has been measured using it and having every call refused.

func catalogueNames(r *Result) []string {
	var out []string
	for _, t := range callTools(r.describes(), r.recaps(), r.pulls(), r.looks()) {
		out = append(out, t.Name)
	}
	return out
}

func describingCalls(names []string) []string {
	var out []string
	for _, n := range names {
		if n == CallOverview || n == CallFile || n == CallCohort {
			out = append(out, n)
		}
	}
	return out
}

// The narrowing happens only where the describing call is somewhere its
// prefix cannot be read from. Everywhere else the catalogue is what it always
// was, because a judging call whose tools differ from the describing call's
// does not match the prefix that call wrote and pays a full write to save a
// fifth of a cent.
func TestTheCatalogueNarrowsOnlyWhenTheDescribingCallIsElsewhere(t *testing.T) {
	base := func() Options {
		return Options{
			API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true,
			Cache: true, CacheTTL: CacheTTL5m, Cohorts: 1,
		}
	}
	for _, tc := range []struct {
		name      string
		opts      func() Options
		describes bool
	}{
		{"one model, one wire", base, true},
		{"the describing call on another wire", func() Options {
			o := base()
			o.Describing = Endpoint{API: APIOpenAI, Model: "gpt-5.6-luna"}
			return o
		}, false},
		{"the describing call on another model, same wire", func() Options {
			o := base()
			o.Describing = Endpoint{Model: "claude-haiku-4"}
			return o
		}, false},
		{"no describing stage", func() Options {
			o := base()
			o.Synopsis = false
			return o
		}, true},
		{"a reused walkthrough, which nothing describes", func() Options {
			o := base()
			o.ReuseSynopsis = &findings.Review{Overview: "from disk"}
			return o
		}, false},
		{"a staged run, whose stage one describes on this wire", func() Options {
			o := base()
			o.Cohorts = 4
			return o
		}, true},
		{"the cache off, so there is no prefix to match", func() Options {
			o := base()
			o.Cache = false
			o.Describing = Endpoint{Model: "claude-haiku-4"}
			return o
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts().withDefaults().judgingCatalogueDescribes(); got != tc.describes {
				t.Errorf("judgingCatalogueDescribes()=%v, want %v", got, tc.describes)
			}
		})
	}
}

// The describing call carries its own tools whatever the judging call
// decided. It clones the judging result, so without this it would inherit a
// narrowed catalogue in exactly the case that narrows it, and arrive with
// nothing to answer with.
func TestTheDescribingCallAlwaysCarriesItsOwnTools(t *testing.T) {
	opts := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true,
		Cache: true, CacheTTL: CacheTTL5m, Cohorts: 1, MaxTokens: 1000,
		Describing: Endpoint{API: APIOpenAI, Model: "gpt-5.6-luna"},
	}.withDefaults()
	res, err := Assemble(deferredInput(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := describingCalls(catalogueNames(res)); len(got) != 0 {
		t.Errorf("the judging catalogue still carries %v", got)
	}
	syn := res.synopsisRequest(opts.describing(), deferredInput())
	for _, want := range []string{CallOverview, CallFile, CallCohort} {
		if !slices.Contains(catalogueNames(syn), want) {
			t.Errorf("the describing call cannot call %s", want)
		}
	}
}

// The judging call and the ruling send the same tool array, whichever
// catalogue the run chose. The ruling reads the prefix the judging call
// wrote, and the tools are part of the bytes that prefix is keyed on.
func TestTheRulingSendsTheJudgingCallsCatalogue(t *testing.T) {
	for _, apart := range []bool{false, true} {
		opts := Options{
			API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true,
			Cache: true, CacheTTL: CacheTTL5m, Cohorts: 1, MaxTokens: 1000,
		}
		if apart {
			opts.Describing = Endpoint{API: APIOpenAI, Model: "gpt-5.6-luna"}
		}
		opts = opts.withDefaults()
		res, err := Assemble(deferredInput(), opts)
		if err != nil {
			t.Fatal(err)
		}
		judging := res.judgingRequest(findings.Review{Overview: "x"}, false)
		ruling := judging.ruleRequest(deferredInput(), opts, nil, nil)
		a, _ := json.Marshal(callTools(judging.describes(), judging.recaps(), judging.pulls(), judging.looks()))
		b, _ := json.Marshal(callTools(ruling.describes(), ruling.recaps(), ruling.pulls(), ruling.looks()))
		if string(a) != string(b) {
			t.Errorf("apart=%v: the ruling's catalogue differs from the judging call's", apart)
		}
	}
}

// A describing call that failed on another model leaves this run without a
// walkthrough rather than asking the judging call to write one. There is no
// cached prefix to make that cheap, and putting the two jobs back under one
// output cap is the failure the split exists to prevent.
func TestAFailedDescribingCallElsewhereDoesNotFallBack(t *testing.T) {
	apart := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true,
		Cache: true, CacheTTL: CacheTTL5m, Cohorts: 1,
		Describing: Endpoint{API: APIOpenAI, Model: "gpt-5.6-luna"},
	}.withDefaults()
	if apart.describingSharesPrefix() {
		t.Error("a describing call on another wire shares no prefix with the judging call")
	}
	together := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true,
		Cache: true, CacheTTL: CacheTTL5m, Cohorts: 1,
	}.withDefaults()
	if !together.describingSharesPrefix() {
		t.Error("the default shape shares one prefix across both calls")
	}
	// And the report still says why there is no walkthrough, rather than
	// leaving a review with no summary reading as one with nothing to say.
	res := &Result{}
	applySynopsis(res, "claude-sonnet-5", described{
		Model: "gpt-5.6-luna", Failed: "the describing call returned no overview",
	})
	if res.Review.Overview == "" {
		t.Error("a run with no walkthrough must say so")
	}
	if res.Synopsis {
		t.Error("a failed describing call did not write the walkthrough")
	}
}

// The estimate counts the tool array. It rides on every call, so an estimate
// without it quotes a price for a request smaller than the one that is sent,
// and promptParts had been listing a tools line the total beside it left out.
// ruleRequest has always counted it; this is the rest of the requests
// catching up.
func TestTheEstimateCountsTheCatalogue(t *testing.T) {
	opts := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true,
		Cache: true, CacheTTL: CacheTTL5m, Cohorts: 1, MaxTokens: 1000,
	}.withDefaults()
	res, err := Assemble(deferredInput(), opts)
	if err != nil {
		t.Fatal(err)
	}
	var summed int
	for _, p := range res.Parts {
		summed += p.Tokens
	}
	tools := toolsTokens(res.describes(), res.recaps(), res.pulls(), res.looks())
	if tools == 0 {
		t.Fatal("this fixture sends no tools, so it cannot test that they are counted")
	}
	if res.InputEstimate < tools {
		t.Errorf("the estimate of %d does not even cover the %d-token catalogue it sends",
			res.InputEstimate, tools)
	}
	// The parts are what a reader is shown the total is made of, so a total
	// below their sum is a breakdown that does not add up.
	if res.InputEstimate < summed {
		t.Errorf("the estimate is %d against parts summing to %d", res.InputEstimate, summed)
	}
}

// Narrowing the catalogue shows up in the price, which is the only reason the
// estimate has to know about it at all.
func TestANarrowedCatalogueIsCheaperToSend(t *testing.T) {
	base := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true,
		Cache: true, CacheTTL: CacheTTL5m, Cohorts: 1, MaxTokens: 1000,
	}
	wide, err := Assemble(deferredInput(), base.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	apart := base
	apart.Describing = Endpoint{API: APIOpenAI, Model: "gpt-5.6-luna"}
	narrow, err := Assemble(deferredInput(), apart.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if narrow.InputEstimate >= wide.InputEstimate {
		t.Errorf("narrowing the catalogue did not shrink the request: %d against %d",
			narrow.InputEstimate, wide.InputEstimate)
	}
}

// set_recap is on the catalogue only for a run that was told which commit the
// previous review ran against, and the describing instruction names the files
// that moved since it. A pass asked for a recap with no list would be
// guessing: the diff it is shown is against the merge base and nothing in it
// records when any of it landed.
func TestTheRecapToolAndItsSectionArriveTogether(t *testing.T) {
	base := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true,
		Cache: true, CacheTTL: CacheTTL5m, Cohorts: 1, MaxTokens: 1000,
	}
	named := func(opts Options) []string {
		res, err := Assemble(deferredInput(), opts.withDefaults())
		if err != nil {
			t.Fatal(err)
		}
		syn := res.synopsisRequest(opts.withDefaults(), deferredInput())
		var out []string
		for _, tool := range callTools(syn.describes(), syn.recaps(), syn.pulls(), syn.looks()) {
			out = append(out, tool.Name)
		}
		return out
	}
	if slices.Contains(named(base), CallRecap) {
		t.Error("a first review was offered set_recap with nothing to recap")
	}
	since := base
	since.SinceReview = "abc1234"
	since.SinceFiles = []string{"internal/review/review.go"}
	if !slices.Contains(named(since), CallRecap) {
		t.Error("a review given a previous commit cannot call set_recap")
	}
	tail := synopsisTail(since.withDefaults(), deferredInput())
	for _, want := range []string{"abc1234", "set_recap", "internal/review/review.go"} {
		if !strings.Contains(tail, want) {
			t.Errorf("the describing instruction is missing %q", want)
		}
	}
	if plain := synopsisTail(base.withDefaults(), deferredInput()); strings.Contains(plain, "set_recap") {
		t.Error("a first review's instruction mentions a tool it was not given")
	}
}

// A run where nothing moved since the last review still gets the tool and is
// told to say so, rather than being left to invent a change.
func TestARunWithNothingNewIsToldSo(t *testing.T) {
	opts := Options{
		API: APIAnthropic, Model: "claude-sonnet-5", Synopsis: true, Cohorts: 1,
		SinceReview: "abc1234",
	}.withDefaults()
	tail := synopsisTail(opts, deferredInput())
	if !strings.Contains(tail, "No file has changed since that commit") {
		t.Errorf("a run with no new files is not told to say so:\n%s", tail)
	}
}
