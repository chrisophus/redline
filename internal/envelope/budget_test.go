package envelope

import (
	"strings"
	"testing"
)

func exp(role Role, prio int, file string, line int, body string) Expansion {
	return Expansion{Role: role, Priority: prio, File: file, StartLine: line,
		EndLine: line, Symbol: file, Content: body}
}

func TestFitKeepsRoleOrderBeforePriority(t *testing.T) {
	e := &Envelope{Expansions: []Expansion{
		exp(RoleHistory, 1000, "h.go", 1, "h"),
		exp(RoleEnclosing, 0, "e.go", 1, "e"),
		exp(RoleCaller, 500, "c.go", 1, "c"),
	}}
	got := Fit(e, DefaultCeiling)
	want := []Role{RoleEnclosing, RoleCaller, RoleHistory}
	for i, x := range got.Kept {
		if x.Role != want[i] {
			t.Fatalf("kept[%d] role = %q, want %q: a provider must not outrank a role with a big priority",
				i, x.Role, want[i])
		}
	}
}

func TestFitOrdersByPriorityWithinARole(t *testing.T) {
	e := &Envelope{Expansions: []Expansion{
		exp(RoleCaller, 1, "a.go", 1, "low"),
		exp(RoleCaller, 9, "b.go", 1, "high"),
	}}
	got := Fit(e, DefaultCeiling)
	if got.Kept[0].Content != "high" {
		t.Fatalf("within a role the higher priority hint comes first, got %q", got.Kept[0].Content)
	}
}

func TestFitIsDeterministicForEqualRanks(t *testing.T) {
	mk := func() *Envelope {
		return &Envelope{Expansions: []Expansion{
			exp(RoleCaller, 5, "z.go", 1, "z"),
			exp(RoleCaller, 5, "a.go", 9, "a9"),
			exp(RoleCaller, 5, "a.go", 2, "a2"),
		}}
	}
	first := Fit(mk(), DefaultCeiling).Render()
	for i := 0; i < 20; i++ {
		if got := Fit(mk(), DefaultCeiling).Render(); got != first {
			t.Fatal("same envelope produced a different context block; an eval cannot reproduce its own input")
		}
	}
	order := Fit(mk(), DefaultCeiling).Kept
	if order[0].Content != "a2" || order[1].Content != "a9" || order[2].Content != "z" {
		t.Fatalf("tiebreak is file then line, got %q %q %q",
			order[0].Content, order[1].Content, order[2].Content)
	}
}

func TestFitDropsOnlyWhatDoesNotFit(t *testing.T) {
	big := strings.Repeat("x", 4000) // about 1000 tokens
	e := &Envelope{Expansions: []Expansion{
		exp(RoleEnclosing, 0, "big.go", 1, big),
		exp(RoleCaller, 0, "small.go", 1, "tiny"),
	}}
	got := Fit(e, 100)
	if len(got.Kept) != 1 || got.Kept[0].Content != "tiny" {
		t.Fatalf("one oversized expansion must not starve every cheaper one behind it; kept %d", len(got.Kept))
	}
	if got.Dropped[RoleEnclosing] != 1 {
		t.Fatalf("dropped count by role = %v, want enclosing=1", got.Dropped)
	}
}

func TestFitNeverExceedsTheCeiling(t *testing.T) {
	var xs []Expansion
	for i := 0; i < 50; i++ {
		xs = append(xs, exp(RoleCaller, i, "f.go", i, strings.Repeat("y", 400)))
	}
	got := Fit(&Envelope{Expansions: xs}, 500)
	if got.Tokens > 500 {
		t.Fatalf("budget overran: %d tokens against a 500 ceiling", got.Tokens)
	}
}

func TestSummaryNamesWhatTheModelWasNotShown(t *testing.T) {
	e := &Envelope{Expansions: []Expansion{
		exp(RoleTest, 0, "t.go", 1, strings.Repeat("x", 4000)),
	}}
	got := Fit(e, 10)
	if !strings.Contains(got.Summary(), "test=1") {
		t.Fatalf("a truncated context must say so, got %q", got.Summary())
	}
	if Fit(&Envelope{}, 10).Summary() != "" {
		t.Fatal("nothing dropped must produce no line")
	}
}

func TestRenderLabelsEachExpansionWithItsRole(t *testing.T) {
	e := &Envelope{Expansions: []Expansion{exp(RoleCaller, 0, "c.go", 7, "call()")}}
	got := Fit(e, DefaultCeiling).Render()
	if !strings.Contains(got, "caller") || !strings.Contains(got, "c.go:7-7") {
		t.Fatalf("render must name what each block is and where it came from, got %q", got)
	}
}

// Context that only repeats the diff is padding: it costs budget and tells the
// model nothing. On a change that adds new files it is most of the envelope,
// because the enclosing declaration of a function in a new file is the file,
// and the diff already shows the file in full.
func TestFitDropsExpansionsAlreadyInTheDiff(t *testing.T) {
	seen := Seen{}
	for i := 1; i <= 10; i++ {
		seen.Add("new.go", i)
	}
	e := &Envelope{Expansions: []Expansion{
		{Role: RoleEnclosing, File: "new.go", StartLine: 2, EndLine: 8, Content: "func F() {}"},
		{Role: RoleCaller, File: "old.go", StartLine: 40, EndLine: 42, Content: "F()"},
	}}
	got := FitSeen(e, DefaultCeiling, seen)
	if got.Redundant != 1 {
		t.Fatalf("redundant = %d, want 1", got.Redundant)
	}
	if got.RedundantTokens == 0 {
		t.Fatal("the saving must be counted, or nobody can tell padding from context")
	}
	if len(got.Kept) != 1 || got.Kept[0].File != "old.go" {
		t.Fatalf("kept = %+v, want only the caller outside the diff", got.Kept)
	}
}

func TestFitKeepsAnExpansionThatOnlyPartlyOverlapsTheDiff(t *testing.T) {
	seen := Seen{}
	for i := 20; i <= 22; i++ {
		seen.Add("f.go", i)
	}
	// A three-line hunk inside a forty-line function: the other thirty-seven
	// lines are exactly what the enclosing role exists to supply.
	e := &Envelope{Expansions: []Expansion{
		{Role: RoleEnclosing, File: "f.go", StartLine: 1, EndLine: 40, Content: "big function"},
	}}
	got := FitSeen(e, DefaultCeiling, seen)
	if got.Redundant != 0 || len(got.Kept) != 1 {
		t.Fatal("partial overlap is not duplication")
	}
}

func TestHistoryIsNeverRedundant(t *testing.T) {
	seen := Seen{}
	for i := 1; i <= 50; i++ {
		seen.Add("q.go", i)
	}
	// History content is commit messages and prior revisions. Nothing in a
	// diff of the current tree can contain it, whatever lines it names.
	e := &Envelope{Expansions: []Expansion{
		{Role: RoleHistory, File: "q.go", StartLine: 5, EndLine: 9,
			Content: "commit abc\n\n    skip empty entries: a nil panicked in production"},
	}}
	got := FitSeen(e, DefaultCeiling, seen)
	if got.Redundant != 0 || len(got.Kept) != 1 {
		t.Fatal("history is the one expansion a diff can never contain; it must survive dedup")
	}
}

func TestFitWithNoSeenSetKeepsEverything(t *testing.T) {
	e := &Envelope{Expansions: []Expansion{
		{Role: RoleEnclosing, File: "a.go", StartLine: 1, EndLine: 2, Content: "x"},
	}}
	if got := Fit(e, DefaultCeiling); got.Redundant != 0 || len(got.Kept) != 1 {
		t.Fatal("without a diff to compare against, nothing is known to be redundant")
	}
}
