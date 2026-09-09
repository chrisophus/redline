package envelope

import "testing"

// Two providers resolving the same change arrive at the same functions by
// different routes. Before kept lines joined seen, the model was shown that
// code twice and charged for it twice: seen held the diff alone.
func TestTheSameLinesFromTwoProvidersArePaidForOnce(t *testing.T) {
	body := "func Insert() error {\n\treturn nil\n}\n"
	exact := &Envelope{Expansions: []Expansion{{
		Role: RoleEnclosing, File: "internal/store/user.go",
		StartLine: 40, EndLine: 42, Symbol: "Insert", Content: body,
	}}}
	other := &Envelope{Expansions: []Expansion{{
		Role: Role("neighbor"), File: "internal/store/user.go",
		StartLine: 40, EndLine: 42, Symbol: "Insert", Content: body,
	}}}

	got := FitAll([]*Envelope{exact, other}, 100_000, nil)
	if len(got.Kept) != 1 {
		t.Fatalf("kept %d expansions, want the one piece of code once", len(got.Kept))
	}
	// The higher-ranked role is the stronger claim, so it is the one kept.
	if got.Kept[0].Role != RoleEnclosing {
		t.Errorf("kept the %q framing; the ranked role is the stronger claim", got.Kept[0].Role)
	}
	if got.Redundant != 1 {
		t.Errorf("Redundant = %d; the duplicate must be counted, not silently dropped", got.Redundant)
	}
	if got.RedundantTokens == 0 {
		t.Error("the tokens the duplicate would have cost are not reported")
	}
}

// A partial overlap is not a duplicate: the lines the second expansion adds
// are still worth sending.
func TestAPartialOverlapIsStillSent(t *testing.T) {
	first := &Envelope{Expansions: []Expansion{{
		Role: RoleEnclosing, File: "a.go", StartLine: 10, EndLine: 12,
		Symbol: "A", Content: "one\ntwo\nthree\n",
	}}}
	second := &Envelope{Expansions: []Expansion{{
		Role: RoleType, File: "a.go", StartLine: 12, EndLine: 20,
		Symbol: "B", Content: "three\nfour\n",
	}}}
	got := FitAll([]*Envelope{first, second}, 100_000, nil)
	if len(got.Kept) != 2 {
		t.Errorf("kept %d, want both: the second carries lines the first did not", len(got.Kept))
	}
}

// History's content is commit messages rather than the source at those lines,
// so it neither becomes redundant nor makes anything else redundant.
func TestHistoryDoesNotSuppressTheCodeAtTheSameLines(t *testing.T) {
	hist := &Envelope{Expansions: []Expansion{{
		Role: RoleHistory, File: "a.go", StartLine: 10, EndLine: 12,
		Symbol: "A", Content: "commit deadbeef: remove the guard\n",
	}}}
	code := &Envelope{Expansions: []Expansion{{
		Role: RoleEnclosing, File: "a.go", StartLine: 10, EndLine: 12,
		Symbol: "A", Content: "one\ntwo\nthree\n",
	}}}
	got := FitAll([]*Envelope{hist, code}, 100_000, nil)
	if len(got.Kept) != 2 {
		t.Errorf("kept %d, want both: history is not the code at those lines", len(got.Kept))
	}
}
