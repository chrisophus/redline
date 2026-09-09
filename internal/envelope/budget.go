package envelope

import (
	"fmt"
	"sort"
	"strings"
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

// Tokens are estimated from character count, because tokenizing properly
// means linking a tokenizer for one specific model and this number has to
// hold for any of them.
//
// The ratio is measured, not assumed. Four measured runs of `redline review`
// on real changes came in at 2.24, 2.29 and 2.34 characters per token, against
// payloads that were Go source, unified diffs and JSON expansion details — the
// mix this tool actually sends. The old 3.5 was a rule of thumb borrowed from
// English prose, and it under-counted those requests by 1.49x to 1.53x, which
// is the wrong direction for a number a ceiling is enforced against: a request
// estimated at 249,767 tokens was admitted under a 250,000 ceiling and then
// sent 372,844. So the divisor is 7/3, i.e. 2.33 characters per token, which
// errs high on prose — the harmless side.
//
// Anything that needs a real count should ask the API's own token counter,
// which is free and exact. This is the offline approximation for budgeting.
const (
	charsPerTokenNum = 3 // divide by 7/3, i.e. 2.33 characters per token
	charsPerTokenDen = 7
)

// EstimateTokens prices a string. Deliberately crude: the division rounds
// up, and the ratio it divides by is measured on code rather than borrowed
// from prose, so the result tracks a real count to within a few percent
// instead of the fifty it used to be short by.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s)*charsPerTokenNum + charsPerTokenDen - 1) / charsPerTokenDen
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
	// Excluded counts expansions a caller's Filter held back before
	// anything competed for the budget. Counted for the same reason
	// Redundant is: context withheld silently reads like context that was
	// never resolved.
	Excluded int
	// ExcludedTokens is what sending them would have cost.
	ExcludedTokens int
	// ExcludedWhat names the class the filter held back, for the summary.
	ExcludedWhat string
}

// Filter decides which expansions never reach the budget at all. It is the
// caller's, not the provider's: a provider resolves what surrounds a change
// and does not get to decide what a review is willing to pay for.
//
// Ranking still reads role and priority alone. A filter is a different
// question, asked once, before the ranking: is this kind of context wanted at
// all. Drop returning true holds the expansion back and counts it in
// Excluded. What names the class in the summary, so the report can say what
// was withheld without this package knowing what it was.
type Filter struct {
	What string
	Drop func(Expansion) bool
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
		var parts []string
		if b.Redundant > 0 {
			parts = append(parts, fmt.Sprintf("%d expansion(s) were already in the diff and were not repeated to the model, saving about %d tokens",
				b.Redundant, b.RedundantTokens))
		}
		if b.Excluded > 0 {
			parts = append(parts, b.excludedClause())
		}
		return strings.Join(parts, ". ")
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
	if b.Excluded > 0 {
		s += ". " + b.excludedClause()
	}
	return s
}

func (b Budgeted) excludedClause() string {
	what := b.ExcludedWhat
	if what == "" {
		what = "context the review does not send"
	}
	return fmt.Sprintf("%d expansion(s) carried %s and were held back, saving about %d tokens",
		b.Excluded, what, b.ExcludedTokens)
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
	return FitFilter(e, ceiling, seen, Filter{})
}

// FitFilter is FitSeen with a caller's filter applied first.
func FitFilter(e *Envelope, ceiling int, seen Seen, filter Filter) Budgeted {
	out := Budgeted{Dropped: map[Role]int{}, Ceiling: ceiling, ExcludedWhat: filter.What}
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

	// Kept lines join seen as the walk goes, so the same code is paid for
	// once however many providers resolved it.
	//
	// Without this, seen held the diff alone and two providers sending the
	// same declaration both got charged for it. That is not hypothetical:
	// gorefactor resolves the whole vocabulary exhaustively, and a second
	// provider looking at the same change arrives at the same functions by a
	// different route. Whichever role ranks higher is the stronger claim and
	// wins; the other is counted as redundant, which the budget summary
	// already reports.
	if seen == nil {
		seen = Seen{}
	}

	// One pass, highest rank first. An expansion that does not fit is
	// dropped and the walk continues: a single large caller must not
	// starve every cheaper expansion behind it.
	for _, x := range ranked {
		cost := x.Tokens()
		if filter.Drop != nil && filter.Drop(x) {
			out.Excluded++
			out.ExcludedTokens += cost
			continue
		}
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
		markSeen(seen, x)
	}
	return out
}

// markSeen records what a kept expansion has now shown the model.
//
// History is left out for the reason unseen exempts it: its content is commit
// messages and prior revisions rather than the current source at those lines,
// so it does not make the lines it names redundant for anyone else.
func markSeen(seen Seen, x Expansion) {
	if x.Role == RoleHistory || x.File == "" || x.StartLine <= 0 {
		return
	}
	for line := x.StartLine; line <= x.EndLine; line++ {
		seen.Add(x.File, line)
	}
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
	return FitAllFilter(envs, ceiling, seen, Filter{})
}

// FitAllFilter is FitAll with a caller's filter applied to every provider's
// expansions alike. What a review will not pay for is Redline's decision, so
// one filter governs all of them.
func FitAllFilter(envs []*Envelope, ceiling int, seen Seen, filter Filter) Budgeted {
	merged := &Envelope{}
	for _, e := range envs {
		if e == nil {
			continue
		}
		merged.Expansions = append(merged.Expansions, e.Expansions...)
	}
	return FitFilter(merged, ceiling, seen, filter)
}
