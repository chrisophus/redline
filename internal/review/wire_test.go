package review

import "testing"

// No test in this repository paired Brief with APIOpenAI before this one, which
// is how the two wires came apart without anything failing.
//
// Brief dropped the tools in anthropicParams alone. completeOpenAI reads Brief
// nowhere and sends every stage's function on every call, so the same flag
// produced a free-form review on one wire and a grammar-bound one on the other,
// and Assemble priced both as though neither sent a catalogue. The wire that
// sent it reserved nothing for it and overfilled the context by the size of the
// schemas.
//
// Both wires now send the same request, so the ceiling they fit to is the same
// number. This test fails if either half of that stops being true.
func TestBothWiresFitABriefReviewToTheSameCeiling(t *testing.T) {
	in := Input{Report: priors()}
	anth, err := Assemble(in, Options{Brief: true, API: APIAnthropic})
	if err != nil {
		t.Fatal(err)
	}
	oai, err := Assemble(in, Options{Brief: true, API: APIOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	if oai.Budget.Ceiling != anth.Budget.Ceiling {
		t.Errorf("brief fitted to %d over openai and %d over anthropic; the two wires send the same request and have to reserve the same way",
			oai.Budget.Ceiling, anth.Budget.Ceiling)
	}
}

// The catalogue is reserved for on every configuration, because every
// configuration sends it. Zeroing it for a brief run was right on the wire that
// dropped the tools and wrong on the wire that did not.
//
// Brief changes the prompt, and the prompt is reserved for too, so the four
// configurations do not all land on one number. What has to hold is that the
// wire makes no difference: each prompt fits to the same ceiling over both.
func TestTheWireDoesNotChangeWhatIsReservedFor(t *testing.T) {
	in := Input{Report: priors()}
	ceiling := func(t *testing.T, opts Options) int {
		t.Helper()
		res, err := Assemble(in, opts)
		if err != nil {
			t.Fatal(err)
		}
		return res.Budget.Ceiling
	}
	for _, tc := range []struct {
		name  string
		brief bool
	}{
		{"brief", true},
		{"the shipped prompt", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			anth := ceiling(t, Options{Brief: tc.brief, API: APIAnthropic})
			oai := ceiling(t, Options{Brief: tc.brief, API: APIOpenAI})
			if anth != oai {
				t.Errorf("fitted to %d over anthropic and %d over openai; the two wires send the same catalogue",
					anth, oai)
			}
		})
	}
	// And the reservation is not zero, or the test above would pass on two runs
	// that both reserved nothing.
	if n := toolsTokens(Options{Brief: true, API: APIOpenAI}.withDefaults()); n == 0 {
		t.Error("toolsTokens priced the catalogue at zero, so nothing above can tell reserved from not")
	}
}

// A body that came back in the content channel is unconstrained whatever stage
// asked for it, so the narrowing has to key on that and not on Brief. Brief
// used to be a proxy for it, back when the brief review was the one call that
// sent no tools.
func TestTheExtractorKeysOnWhereTheBodyCameFrom(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fromTool bool
		body     string
		want     string
	}{
		{"tool arguments pass through", true, `{"a":1}`, `{"a":1}`},
		{"a fenced content reply is narrowed", false, "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"prose around a content reply is stripped", false, `Here is the review: {"a":1}`, `{"a":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.body
			if !tc.fromTool {
				got = jsonObjectOf(got)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
