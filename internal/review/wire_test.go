package review

import "testing"

// No test in this repository paired Brief with APIOpenAI before this one, which
// is how the two wires came apart without anything failing.
//
// completeOpenAI reads Brief nowhere. It sends every stage's function on every
// call, the review included, so a brief review over that wire is the brief
// prompt carried by the strict tool grammar. Assemble zeroed the tool
// reservation for any brief run, so that call reserved nothing for a catalogue
// the wire then sent and fitted the packet to a ceiling too high by the size of
// the schemas.
func TestABriefReviewOverTheOpenAIWireStillReservesItsTools(t *testing.T) {
	in := Input{Report: priors()}
	// Both are brief, so both carry briefPrompt and the only difference left
	// between them is whether the catalogue was reserved for.
	anth, err := Assemble(in, Options{Brief: true, API: APIAnthropic})
	if err != nil {
		t.Fatal(err)
	}
	oai, err := Assemble(in, Options{Brief: true, API: APIOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	if oai.Budget.Ceiling >= anth.Budget.Ceiling {
		t.Errorf("brief fitted to %d over openai and %d over anthropic; the wire that sends the catalogue has to reserve for it",
			oai.Budget.Ceiling, anth.Budget.Ceiling)
	}
}

// The helper is the question every pricing and reserving caller has to ask,
// because Brief on its own answers a different one.
func TestBriefSendsNoToolsOnlyOnTheWireThatDropsThem(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want bool
	}{
		{"brief over anthropic", Options{Brief: true, API: APIAnthropic}, true},
		{"brief over openai", Options{Brief: true, API: APIOpenAI}, false},
		{"not brief over anthropic", Options{API: APIAnthropic}, false},
		{"not brief over openai", Options{API: APIOpenAI}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.briefSendsNoTools(); got != tc.want {
				t.Errorf("briefSendsNoTools() = %v, want %v", got, tc.want)
			}
		})
	}
}
