package history

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// sizeBands are the upper bounds, in lines added plus removed, of the groups
// the size table sorts reviews into. The last group is everything larger.
var sizeBands = []int{100, 500, 2000}

// worstShown is how many reviews the lookup section names.
const worstShown = 5

// Group is a set of reviews summarised on one table row.
type Group struct {
	Label      string
	Runs       int
	MedianCost float64
	// Priced is how many of the runs had a known cost, which is what the
	// median is over.
	Priced      int
	MedianTurns int
	MedianLines int
	HitLimit    int
	Proposed    int
	Kept        int
	Misses      int
}

func groupOf(label string, runs []Run) Group {
	g := Group{Label: label, Runs: len(runs)}
	var costs []float64
	var turns, lines []int
	for _, r := range runs {
		if r.CostKnown {
			costs = append(costs, r.CostUSD)
		}
		if r.Turns > 0 {
			turns = append(turns, r.Turns)
		}
		if r.SizeKnown {
			lines = append(lines, r.Lines())
		}
		if len(r.Limits) > 0 {
			g.HitLimit++
		}
		g.Proposed += r.Proposed
		g.Kept += r.Kept
		g.Misses += r.Misses
	}
	g.Priced = len(costs)
	sort.Float64s(costs)
	if len(costs) > 0 {
		g.MedianCost = costs[(len(costs)-1)/2]
	}
	g.MedianTurns = medianInt(turns)
	g.MedianLines = medianInt(lines)
	return g
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	sort.Ints(v)
	return v[(len(v)-1)/2]
}

// BySize groups the reviews whose change size is known into the size bands,
// leaving out a band with no reviews in it.
func (rep *Report) BySize() []Group {
	buckets := make([][]Run, len(sizeBands)+1)
	for _, r := range rep.Runs {
		if !r.SizeKnown {
			continue
		}
		i := sort.SearchInts(sizeBands, r.Lines())
		buckets[i] = append(buckets[i], r)
	}
	var out []Group
	for i, runs := range buckets {
		if len(runs) == 0 {
			continue
		}
		label := "over " + comma(sizeBands[len(sizeBands)-1])
		if i < len(sizeBands) {
			label = "up to " + comma(sizeBands[i])
		}
		out = append(out, groupOf(label, runs))
	}
	return out
}

// ByShape groups the reviews that have a cost row by how many calls judged
// the change. The shape is only on the cost row, so a review without one is
// left out.
func (rep *Report) ByShape() []Group {
	byCohorts := map[int][]Run{}
	for _, r := range rep.Runs {
		if !r.InLedger {
			continue
		}
		n := 1
		if r.Split() {
			n = r.Cohorts
		}
		byCohorts[n] = append(byCohorts[n], r)
	}
	keys := make([]int, 0, len(byCohorts))
	for k := range byCohorts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var out []Group
	for _, k := range keys {
		label := "one call"
		if k > 1 {
			label = "split in " + strconv.Itoa(k)
		}
		out = append(out, groupOf(label, byCohorts[k]))
	}
	return out
}

// Render is the report as text.
func (rep *Report) Render() string {
	var b strings.Builder
	if len(rep.Runs) == 0 {
		fmt.Fprintf(&b, "No saved reviews under %s.\n", rep.Root)
		if rep.NoReview > 0 {
			fmt.Fprintf(&b, "Saved sessions with a run and no model review: %d.\n", rep.NoReview)
		}
		rep.notes(&b)
		return b.String()
	}
	rep.header(&b)
	rep.sizes(&b)
	rep.limits(&b)
	rep.shapes(&b)
	rep.lookups(&b)
	rep.scout(&b)
	rep.replay(&b)
	rep.notes(&b)
	return b.String()
}

func (rep *Report) header(b *strings.Builder) {
	first, last := rep.Runs[0].When, rep.Runs[len(rep.Runs)-1].When
	fmt.Fprintf(b, "%s under %s, %s to %s\n", plural(len(rep.Runs), "saved review"), rep.Root,
		first.Format("2006-01-02"), last.Format("2006-01-02"))
	var total float64
	var unpriced, noSize, noLedger int
	for _, r := range rep.Runs {
		if r.CostKnown {
			total += r.CostUSD
		} else {
			unpriced++
		}
		if !r.SizeKnown {
			noSize++
		}
		if !r.InLedger {
			noLedger++
		}
	}
	fmt.Fprintf(b, "  total cost          %s", money(total))
	if unpriced > 0 {
		fmt.Fprintf(b, " (%s had no known price and are not counted)", plural(unpriced, "review"))
	}
	b.WriteString("\n")
	if noSize > 0 {
		fmt.Fprintf(b, "  size unknown        %s: the session beside the review is of a later change\n",
			plural(noSize, "review"))
	}
	if noLedger > 0 {
		fmt.Fprintf(b, "  no cost row         %s: how the change was split and what size the prompt was are unknown for these\n",
			plural(noLedger, "review"))
	}
	if rep.NoReview > 0 {
		fmt.Fprintf(b, "  never reviewed      %s with a run and no model review, left out\n",
			plural(rep.NoReview, "saved session"))
	}
}

func groupTable(b *strings.Builder, first string, groups []Group, lines bool) {
	w := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	head := "  " + first + "\treviews\t"
	if lines {
		head += "median lines\t"
	}
	_, _ = fmt.Fprintln(w, head+"median cost\tmedian turns\thit a limit\tproposed\tkept\tmisses")
	for _, g := range groups {
		row := fmt.Sprintf("  %s\t%d\t", g.Label, g.Runs)
		if lines {
			row += comma(g.MedianLines) + "\t"
		}
		cost := "unknown"
		if g.Priced > 0 {
			cost = money(g.MedianCost)
		}
		_, _ = fmt.Fprintf(w, "%s%s\t%d\t%d\t%d\t%d\t%d\n", row, cost, g.MedianTurns, g.HitLimit, g.Proposed, g.Kept, g.Misses)
	}
	_ = w.Flush()
}

func (rep *Report) sizes(b *strings.Builder) {
	groups := rep.BySize()
	if len(groups) == 0 {
		return
	}
	b.WriteString("\nBy size of the change (lines added plus removed)\n")
	groupTable(b, "lines", groups, false)
	b.WriteString("  proposed and kept are findings summed over the group, and misses are defects\n" +
		"  recorded with `redline miss` against a review in it.\n")
}

func (rep *Report) limits(b *strings.Builder) {
	var hit []Run
	var cost float64
	for _, r := range rep.Runs {
		if len(r.Limits) == 0 {
			continue
		}
		hit = append(hit, r)
		if r.CostKnown {
			cost += r.CostUSD
		}
	}
	b.WriteString("\nReviews that ran into a limit\n")
	if len(hit) == 0 {
		b.WriteString("  None. Every review finished inside its size, token and turn limits.\n")
		return
	}
	for _, r := range hit {
		fmt.Fprintf(b, "  %s  %s  %s\n", r.name(), r.size(), r.costText())
		for _, l := range r.Limits {
			fmt.Fprintf(b, "      %s\n", l)
		}
	}
	fmt.Fprintf(b, "  %d of %s ran into a limit, and those cost %s between them.\n",
		len(hit), plural(len(rep.Runs), "review"), money(cost))
}

func (rep *Report) shapes(b *strings.Builder) {
	groups := rep.ByShape()
	if len(groups) == 0 {
		return
	}
	b.WriteString("\nOne call against a split\n")
	groupTable(b, "shape", groups, true)
	if len(groups) == 1 {
		fmt.Fprintf(b, "  Every review with a cost row ran as %s, so there is nothing to compare it with.\n", groups[0].Label)
	}
}

func (rep *Report) lookups(b *strings.Builder) {
	var with []Run
	var tot Lookups
	for _, r := range rep.Runs {
		if r.Lookups.Made == 0 {
			continue
		}
		with = append(with, r)
		tot.Made += r.Lookups.Made
		tot.Empty += r.Lookups.Empty
		tot.Repeated += r.Lookups.Repeated
		tot.OnFinding += r.Lookups.OnFinding
	}
	b.WriteString("\nLookups the review made itself\n")
	if len(with) == 0 {
		b.WriteString("  No saved review recorded a lookup.\n")
		return
	}
	fmt.Fprintf(b, "  %s made %s.\n", plural(len(with), "review"), plural(tot.Made, "lookup"))
	fmt.Fprintf(b, "    %s came back empty\n", share(tot.Empty, tot.Made))
	fmt.Fprintf(b, "    %s repeated an earlier lookup in the same review\n", share(tot.Repeated, tot.Made))
	fmt.Fprintf(b, "    %s named a file the review filed a finding on\n", share(tot.OnFinding, tot.Made))
	b.WriteString("  A lookup can count on more than one line. The trace does not record which\n" +
		"  turn a lookup was made on, so what the empty and repeated ones cost is not known.\n")

	sort.SliceStable(with, func(i, j int) bool { return with[i].wasted() > with[j].wasted() })
	shown := 0
	for _, r := range with {
		if r.wasted() == 0 || shown == worstShown {
			break
		}
		if shown == 0 {
			b.WriteString("  Most empty or repeated lookups:\n")
		}
		shown++
		l := r.Lookups
		fmt.Fprintf(b, "    %s  %d of %s empty, %d repeated, %d on a file with a finding; %s, %s\n",
			r.name(), l.Empty, plural(l.Made, "lookup"), l.Repeated, l.OnFinding, plural(r.Turns, "turn"), r.costText())
	}
}

func (rep *Report) scout(b *strings.Builder) {
	var n, turns, calls, failed, repeated, capped, asked, fetched int
	var cost float64
	for _, r := range rep.Runs {
		s := r.Scout
		if !s.Ran {
			continue
		}
		n++
		turns += s.Turns
		calls += s.Calls
		failed += s.Failed
		repeated += s.Repeated
		asked += s.Asked
		fetched += s.FromFetch
		cost += s.CostUSD
		if s.CapHit {
			capped++
		}
	}
	if n == 0 {
		return
	}
	b.WriteString("\nLookups behind --verify\n")
	fmt.Fprintf(b, "  %s ran them: %s, %s, %s.\n", plural(n, "review"), plural(turns, "turn"), plural(calls, "call"), money(cost))
	fmt.Fprintf(b, "    %s were refused or failed\n", share(failed, calls))
	fmt.Fprintf(b, "    %s repeated an earlier call in the same review\n", share(repeated, calls))
	fmt.Fprintf(b, "    %s stopped at the cost cap\n", share(capped, n))
	fmt.Fprintf(b, "  %s went to the lookups, and %s quoted a line the lookups fetched.\n",
		plural(asked, "finding"), plural(fetched, "ruling"))
}

func (rep *Report) notes(b *strings.Builder) {
	for _, n := range rep.Notes {
		fmt.Fprintf(b, "\nnote: %s\n", n)
	}
}

// wasted is the count the lookup section ranks reviews by.
func (r Run) wasted() int { return r.Lookups.Empty + r.Lookups.Repeated }

func (r Run) name() string {
	n := r.Session
	if len(r.Head) >= 7 {
		n += " " + r.Head[:7]
	}
	return n + " " + r.When.Format("2006-01-02")
}

func (r Run) size() string {
	var parts []string
	if r.SizeKnown {
		parts = append(parts, fmt.Sprintf("%s in %s", plural(r.Lines(), "line"), plural(r.Files, "file")))
	} else {
		parts = append(parts, "size unknown")
	}
	if r.InLedger && r.Split() {
		parts = append(parts, "split in "+strconv.Itoa(r.Cohorts))
	} else if r.InLedger {
		parts = append(parts, "one call")
	}
	return strings.Join(parts, ", ")
}

func (r Run) costText() string {
	if !r.CostKnown {
		return "cost unknown"
	}
	return money(r.CostUSD)
}

func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

// share is "n of m (p%)", or "none" when m is zero.
func share(n, m int) string {
	if m == 0 {
		return "none"
	}
	return fmt.Sprintf("%d of %d (%d%%)", n, m, (n*100+m/2)/m)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return comma(n) + " " + noun + "s"
}

// comma writes n with a separator every three digits.
func comma(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// replay is what the model said, set against what happened.
func (rep *Report) replay(b *strings.Builder) {
	rp := rep.Replay
	if rp == nil {
		return
	}
	fmt.Fprintf(b, "\nWhat %s would have decided\n", rp.Model)
	cost := "cost unknown, the model is not in the price table"
	if rp.CostKnown {
		cost = fmt.Sprintf("$%.4f", rp.CostUSD)
	}
	fmt.Fprintf(b, "  %s answered, %s input tokens, %s\n", plural(rp.Asks, "question"), comma(int(rp.Usage.InputTokens)), cost)
	if rp.Failed > 0 {
		fmt.Fprintf(b, "  %s got no usable answer. The first: %s\n", plural(rp.Failed, "question"), rp.FirstError)
	}
	if rp.Skipped > 0 {
		fmt.Fprintf(b, "  %s were not asked because of --max-asks\n", plural(rp.Skipped, "question"))
	}
	rep.replayShape(b)
	rep.replayLookups(b)
}

func (rep *Report) replayShape(b *strings.Builder) {
	said := map[string][]Run{}
	for _, r := range rep.Runs {
		if r.Asked != nil && r.Asked.Shape != "" {
			said[r.Asked.Shape] = append(said[r.Asked.Shape], r)
		}
	}
	if len(said) == 0 {
		return
	}
	b.WriteString("\n  One call or a split, asked with the file list and the start of each diff\n")
	w := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "    it said\treviews\tran in one call\tran split\thit a limit\tmisses\tmedian lines\tmedian cost")
	for _, word := range []string{SaidSingle, SaidSplit} {
		runs := said[word]
		if len(runs) == 0 {
			continue
		}
		g := groupOf(word, runs)
		var one, split int
		for _, r := range runs {
			if r.InLedger && r.Split() {
				split++
			} else if r.InLedger {
				one++
			}
		}
		cost := "unknown"
		if g.Priced > 0 {
			cost = money(g.MedianCost)
		}
		_, _ = fmt.Fprintf(w, "    %s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
			word, g.Runs, one, split, g.HitLimit, g.Misses, comma(g.MedianLines), cost)
	}
	_ = w.Flush()

	// The two cases where its answer differs from what was done.
	var limited, limitedSplit, wasSplit, wasSplitSingle int
	for word, runs := range said {
		for _, r := range runs {
			if r.InLedger && !r.Split() && len(r.Limits) > 0 {
				limited++
				if word == SaidSplit {
					limitedSplit++
				}
			}
			if r.Split() {
				wasSplit++
				if word == SaidSingle {
					wasSplitSingle++
				}
			}
		}
	}
	if limited > 0 {
		fmt.Fprintf(b, "    Reviews that ran in one call and hit a limit: %d. It would have split %d of them.\n",
			limited, limitedSplit)
	}
	if wasSplit > 0 {
		fmt.Fprintf(b, "    Reviews that were split: %d. It would have sent %d of them in one call.\n",
			wasSplit, wasSplitSingle)
	}
}

func (rep *Report) replayLookups(b *strings.Builder) {
	type tally struct{ n, empty, repeated, wasted, onFinding int }
	said := map[string]*tally{SaidLook: {}, SaidStop: {}}
	var all tally
	for _, r := range rep.Runs {
		for _, l := range r.Each {
			t := said[l.Said]
			if t == nil {
				continue
			}
			for _, c := range []*tally{t, &all} {
				c.n++
				if l.Empty {
					c.empty++
				}
				if l.Repeated {
					c.repeated++
				}
				if l.Empty || l.Repeated {
					c.wasted++
				}
				if l.OnFinding {
					c.onFinding++
				}
			}
		}
	}
	if all.n == 0 {
		return
	}
	b.WriteString("\n  Each lookup, asked before it was made with the lookups before it\n")
	w := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "    it said\tlookups\tcame back empty\trepeated\ton a file with a finding")
	for _, word := range []string{SaidLook, SaidStop} {
		t := said[word]
		_, _ = fmt.Fprintf(w, "    %s\t%d\t%d\t%d\t%d\n", word, t.n, t.empty, t.repeated, t.onFinding)
	}
	_ = w.Flush()
	stop := said[SaidStop]
	fmt.Fprintf(b, "    Stopping where it said would have skipped %s empty or repeated lookups\n"+
		"    and %s lookups on a file the review filed a finding on.\n",
		share(stop.wasted, all.wasted), share(stop.onFinding, all.onFinding))
	b.WriteString("    Each lookup was asked about on its own. In a real review the first stop would\n" +
		"    have ended the looking, so the lookups after it would not have been made either.\n")
}
