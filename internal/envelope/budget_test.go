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
