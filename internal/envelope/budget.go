package envelope

import (
	"fmt"
	"sort"
)

// DefaultCeiling bounds one review's whole request.
//
// It is a tail bound, not a per-review budget. The cost target is an average
// across reviews: most changes are small and cost cents, a few are large and
// cost more, and holding every review to the average would trim context from
// exactly the large changes that most need it. So the ceiling is set where a
// review stops being worth doing in one turn rather than where the average
// sits, and the average is measured instead of assumed. See internal/review's
// ledger.
//
// At Sonnet rates 250k input tokens is about fifty cents, so a review that
// actually reaches this cap is an outlier by construction, and the ledger
// will show it as one.
const DefaultCeiling = 250_000

// charsPerToken is the estimate used to price an expansion. Redline cannot
// tokenize without linking a tokenizer for a specific model, and the budget
// is a ceiling rather than an accounting record, so an estimate that errs
// toward over-counting is the right tool. Four characters per token is the
// usual rule of thumb for source code.
const charsPerToken = 4

// EstimateTokens prices a string. Deliberately crude and deliberately
// pessimistic: rounding up means a change never exceeds the ceiling it was
// budgeted against.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + charsPerToken - 1) / charsPerToken
}

// Tokens is what one expansion costs, including the header Redline renders
// above it. Providers do not get to price their own expansions.
func (x Expansion) Tokens() int {
	return EstimateTokens(x.Content) + EstimateTokens(x.header())
}

func (x Expansion) header() string {
	loc := x.File
	if x.StartLine > 0 {
		loc = fmt.Sprintf("%s:%d-%d", x.File, x.StartLine, x.EndLine)
	}
	return fmt.Sprintf("── %s · %s · %s ──\n", x.Role, x.Symbol, loc)
}

// Seen is the lines the model is already being shown, per file, from the diff
// itself. Redline knows this and a provider does not: the provider resolves
// what surrounds a change without knowing how the change will be presented.
//
// Deduplicating against it is the difference between context and padding. An
// expansion whose every line is already in the diff costs budget and tells the
// model nothing, and on a change that adds new files most expansions are
// exactly that: the enclosing declaration of a function in a brand new file is
// the file, and the file is already there in full.
type Seen map[string]map[int]bool

// Add records a line as already shown.
func (s Seen) Add(file string, line int) {
	if s == nil || file == "" {
		return
	}
	if s[file] == nil {
		s[file] = map[int]bool{}
	}
	s[file][line] = true
}

// unseen counts how many of an expansion's lines the model has not been shown.
//
// History is exempt and always counts as unseen. It carries a line span like
// everything else, but its content is commit messages and prior revisions
// rather than the current source at those lines, so no diff of the working
// tree can contain it. Treating the span at face value here deletes the one
// expansion that catches a change undoing a deliberate fix, which is the
// role's whole reason for existing.
func (s Seen) unseen(x Expansion) int {
	if x.Role == RoleHistory {
		return 1
	}
	if x.File == "" || x.StartLine <= 0 || x.EndLine < x.StartLine {
		return 1
	}
	shown := s[x.File]
	if shown == nil {
		return x.EndLine - x.StartLine + 1
	}
	var n int
	for line := x.StartLine; line <= x.EndLine; line++ {
		if !shown[line] {
			n++
		}
	}
	return n
}

// Budgeted is the result of fitting an envelope's expansions to a ceiling.
type Budgeted struct {
	Kept []Expansion
	// Dropped counts what did not fit, by role, so the report can say what
	// the model was not shown. A context pack that silently truncates reads
	// exactly like one that had nothing more to give.
	Dropped map[Role]int
	// Tokens is the estimated cost of Kept.
	Tokens int
	// Ceiling is what it was fitted to.
	Ceiling int
	// Redundant counts expansions dropped because every line of them was
	// already in the diff. Counted rather than silently discarded: a
	// provider whose output is mostly redundant is worth knowing about, and
	// on a change that adds new files that is most of it.
	Redundant int
	// RedundantTokens is what keeping them would have cost.
	RedundantTokens int
}

// DroppedTotal is how many expansions did not fit.
func (b Budgeted) DroppedTotal() int {
	var n int
	for _, c := range b.Dropped {
		n += c
	}
	return n
}

// Summary is one line for the report and the unknowns list, or empty when
// everything fit.
func (b Budgeted) Summary() string {
	if b.DroppedTotal() == 0 {
		if b.Redundant > 0 {
			return fmt.Sprintf("%d expansion(s) were already in the diff and were not repeated to the model, saving about %d tokens",
				b.Redundant, b.RedundantTokens)
		}
		return ""
	}
	roles := make([]Role, 0, len(b.Dropped))
	for r := range b.Dropped {
		roles = append(roles, r)
	}
	sort.Slice(roles, func(i, j int) bool {
		a, _ := roles[i].Rank()
		c, _ := roles[j].Rank()
		if a != c {
			return a < c
		}
		return roles[i] < roles[j]
	})
	s := fmt.Sprintf("%d expansion(s) exceeded the %d-token context ceiling and were not shown to the model:",
		b.DroppedTotal(), b.Ceiling)
	for _, r := range roles {
		s += fmt.Sprintf(" %s=%d", r, b.Dropped[r])
	}
	if b.Redundant > 0 {
		s += fmt.Sprintf(". A further %d were already in the diff and were not repeated, saving about %d tokens",
			b.Redundant, b.RedundantTokens)
	}
	return s
}

// Fit ranks an envelope's expansions and keeps as many as the ceiling allows.
//
// Ranking reads role and priority and nothing else. It never looks inside
// Content, which is the property that lets one implementation serve every
// language: Redline cannot tell Go from SQL here and does not need to.
//
// Order is total and deterministic — role rank, then priority descending,
// then file, line, and symbol — so the same envelope produces the same
// context block on every run. An eval that cannot reproduce its own input is
// measuring noise.
func Fit(e *Envelope, ceiling int) Budgeted {
	return FitSeen(e, ceiling, nil)
}

// FitSeen is Fit against the lines the model is already being shown, so
// context that only restates the diff is dropped before anything competes for
// the budget.
func FitSeen(e *Envelope, ceiling int, seen Seen) Budgeted {
	out := Budgeted{Dropped: map[Role]int{}, Ceiling: ceiling}
	if e == nil {
		return out
	}
	ranked := make([]Expansion, len(e.Expansions))
	copy(ranked, e.Expansions)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		ra, _ := a.Role.Rank()
		rb, _ := b.Role.Rank()
		if ra != rb {
			return ra < rb
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		return a.Symbol < b.Symbol
	})

	// One pass, highest rank first. An expansion that does not fit is
	// dropped and the walk continues: a single large caller must not
	// starve every cheaper expansion behind it.
	for _, x := range ranked {
		cost := x.Tokens()
		if seen != nil && seen.unseen(x) == 0 {
			out.Redundant++
			out.RedundantTokens += cost
			continue
		}
		if out.Tokens+cost > ceiling {
			out.Dropped[x.Role]++
			continue
		}
		out.Kept = append(out.Kept, x)
		out.Tokens += cost
	}
	return out
}

// Render writes the kept expansions as the context block the model reads.
// Grouped by role, in rank order, each under a header naming what it is —
// the model is told a caller is a caller, because "here is some code" and
// "here is who calls the thing you changed" support very different findings.
func (b Budgeted) Render() string {
	if len(b.Kept) == 0 {
		return ""
	}
	var sb []byte
	var current Role
	for i, x := range b.Kept {
		if i == 0 || x.Role != current {
			current = x.Role
			sb = append(sb, "\n"...)
		}
		sb = append(sb, x.header()...)
		sb = append(sb, x.Content...)
		if len(x.Content) > 0 && x.Content[len(x.Content)-1] != '\n' {
			sb = append(sb, '\n')
		}
	}
	return string(sb)
}

// FitAll budgets several providers' envelopes together against one ceiling.
//
// A repository with a Go provider and a TypeScript provider gets one context
// block, not two competing ones, and the ranking is the same: role first,
// then the provider's hint. Neither provider can spend more of the budget by
// scoring its own expansions higher, because role rank is Redline's.
func FitAll(envs []*Envelope, ceiling int, seen Seen) Budgeted {
	merged := &Envelope{}
	for _, e := range envs {
		if e == nil {
			continue
		}
		merged.Expansions = append(merged.Expansions, e.Expansions...)
	}
	return FitSeen(merged, ceiling, seen)
}
