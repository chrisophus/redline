package eval

import (
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// Both directions, from bodies a real review wrote. A denial that still scored
// would keep crediting a reviewer for arguing a defect away; an assertion that
// stopped scoring would hide a real catch, and the hedged ones are the ones a
// loose rule would take.
func TestADenialIsToldApartFromAClaim(t *testing.T) {
	for _, tc := range []struct {
		body   string
		denies bool
	}{
		{"The condition is checked before facts are invalidated, which matches the other call sites. No defect apparent.", true},
		{"invalidateNilFacts runs after checkExpr here, but the fact was already used. This is fine; no issue found here.", true},
		{"p := &x; p.Reset() doesn't touch x's fact map entry by name, so this looks consistent, not a bug — withdrawing.", true},
		{"This is a performance concern only, not correctness, so not reported as a defect.", true},
		{"The map is only read after Run fills it. Not a bug - retracting.", true},
		{"Given this is conservative (fewer reports), it's not a correctness bug, just under-reporting by design.", true},
		{"This looks intentional, but check whether invalidateNilFacts ever fires on a plain x == nil condition — it shouldn't, so likely fine.", true},
		{"The disjunct walk returns nil on a mixed guard — this is intentional per the doc comment and matches the existing precedent; not a new defect.", true},

		// Plays down how much it matters and still says it is there.
		{"The fix advice is emitted twice though there is only one guard statement — a minor duplicate-message issue, not a correctness bug in the underlying analysis.", false},

		{"Removing it reintroduces that crash if the retry path still emits empty entries into this queue.", false},
		{"The tripwire uses cohortMaxTokens the same way, so it should be internally consistent, but the per-cohort floor defeats the divide-the-budget design goal.", false},
		{"Since Delta is used once per Run call this is fine, but note the map is never nil-checked before Observe uses it.", false},
		{"This is a narrow edge case unlikely to be exercised by real code but worth a caller check.", false},
		{"No cap exists on caller/test expansions count.", false},
		{"s.field.Method() is not tracked. A renamed import of log makes the sibling logic miss a terminating call.", false},
		{"", false},
	} {
		if got := deniesDefect(tc.body); got != tc.denies {
			t.Errorf("deniesDefect(%q) = %v, want %v", tc.body, got, tc.denies)
		}
	}
}

// The rule has to reach the score: a comment carrying every word a label asks
// for, on the right file, still does not catch it when it concludes there is
// nothing wrong.
func TestADenialCatchesNothing(t *testing.T) {
	f := Fixture{Annotation: Annotation{Name: "denial", Expect: []Expectation{
		{Key: "ordering", File: "a.go", AllOf: []string{"checkexpr", "invalidatenilfacts"}},
	}}}
	claim := findings.ReviewComment{File: "a.go", Body: "checkExpr runs before invalidateNilFacts, so a mutating call in the left conjunct is ignored and the report is wrong."}
	denial := findings.ReviewComment{File: "a.go", Body: "checkExpr runs before invalidateNilFacts, which matches every other call site. No defect apparent."}

	if sc := Score(f, findings.Review{Comments: []findings.ReviewComment{claim}}); len(sc.Caught) != 1 {
		t.Errorf("the claim caught %v, want the label", sc.Caught)
	}
	sc := Score(f, findings.Review{Comments: []findings.ReviewComment{denial}})
	if len(sc.Caught) != 0 {
		t.Errorf("the denial caught %v", sc.Caught)
	}
	if sc.Extra != 1 {
		t.Errorf("the denial scored extra=%d, want it counted as an unlabelled comment", sc.Extra)
	}
}
