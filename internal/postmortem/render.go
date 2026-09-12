package postmortem

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
)

// wrapAt is where prose folds. A finding's body is a paragraph and a
// terminal is not, and the alternative to folding it is a report you read by
// scrolling sideways.
const wrapAt = 78

// Render is the whole postmortem as text.
//
// It is ordered the way the run was: what the reviewer proposed, what the
// scout did about each proposal, what the ruling decided, then the search
// itself end to end. A reader who wants one finding stops at the first
// section, and a reader asking why the lookups came back empty reads the
// second, which is the question the whole file exists for.
func (t *Trace) Render() string {
	var b strings.Builder
	t.header(&b)
	t.counts(&b)
	for _, f := range t.Findings {
		t.finding(&b, f)
	}
	if len(t.Findings) == 0 {
		b.WriteString("\nThe reviewer proposed nothing, so there was nothing to look up or rule on.\n")
	}
	t.unanswered(&b)
	t.search(&b)
	t.notes(&b)
	return b.String()
}

func (t *Trace) header(b *strings.Builder) {
	target := t.Target
	if target == "" {
		target = "a change"
	}
	fmt.Fprintf(b, "review of %s\n", target)
	if t.Revision != "" {
		fmt.Fprintf(b, "  change     %s\n", t.Revision)
	}
	if !t.Wrote.IsZero() {
		fmt.Fprintf(b, "  reviewed   %s\n", t.Wrote.Format("2006-01-02 15:04:05 MST"))
	}
	model := t.Model
	if t.API != "" {
		model += " over " + t.API
	}
	if t.Effort != "" {
		model += ", effort " + t.Effort
	}
	if t.Samples > 1 {
		model += fmt.Sprintf(", %d samples unioned", t.Samples)
	}
	if strings.TrimSpace(model) != "" {
		fmt.Fprintf(b, "  reviewer   %s\n", model)
	}
	switch {
	case t.Lookup.Ran:
		scout := t.Lookup.Model
		if t.Lookup.Effort != "" {
			scout += ", effort " + t.Lookup.Effort
		}
		fmt.Fprintf(b, "  scout      %s\n", strings.TrimPrefix(scout, ", "))
	case t.Lookup.Error != "":
		fmt.Fprintf(b, "  scout      did not run: %s\n", t.Lookup.Error)
	default:
		b.WriteString("  scout      not asked anything\n")
	}
	if t.CostUSD > 0 || t.CostKnown {
		fmt.Fprintf(b, "  cost       %s in total, %s of it the lookups\n",
			review.FormatCost(t.CostUSD, t.CostKnown),
			review.FormatCost(t.Lookup.CostUSD, t.Lookup.CostKnown))
	}
	switch {
	case t.VerifyFailed != "":
		fmt.Fprintf(b, "  checking   ran and produced nothing: %s\n", t.VerifyFailed)
	case t.Verified:
		b.WriteString("  checking   ran; only a finding a ruling kept was posted\n")
	default:
		b.WriteString("  checking   did not run, so every finding below was posted unchecked\n")
	}
}

// counts is the line that says which of the two failures this run had. A
// review that proposed nine and posted two is either noisy or badly answered,
// and the split between questions asked and questions answered is what tells
// them apart.
func (t *Trace) counts(b *strings.Builder) {
	asked, answered := 0, 0
	byVerdict := map[string]int{}
	for _, f := range t.Findings {
		if f.Asked {
			asked++
			if len(t.answersFor(f.ID)) > 0 {
				answered++
			}
		}
		v := f.Ruling.Verdict
		if v == "" {
			v = "unruled"
		}
		byVerdict[v]++
	}
	fmt.Fprintf(b, "\n%s proposed, %s asked, %d of them with something filed against it\n",
		plural(len(t.Findings), "finding"), plural(asked, "question"), answered)
	if n := t.restedOnALookup(); n > 0 {
		fmt.Fprintf(b, "%s ruled on a line the lookups filed rather than on the diff\n",
			plural(n, "finding"))
	}
	if len(byVerdict) > 0 {
		keys := make([]string, 0, len(byVerdict))
		for k := range byVerdict {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, byVerdict[k]))
		}
		fmt.Fprintf(b, "rulings: %s\n", strings.Join(parts, ", "))
	}
	if t.Lookup.Ran {
		line := fmt.Sprintf("the lookups: %s, %s, %s filed, %s",
			plural(t.Lookup.Turns, "turn"), plural(len(t.Lookup.Calls), "tool call"),
			plural(len(t.Lookup.Filed), "record"),
			review.FormatCost(t.Lookup.CostUSD, t.Lookup.CostKnown))
		if t.Lookup.CapHit {
			line += ", stopped at the cost cap"
		}
		if n := len(t.Lookup.Filed) - len(t.Lookup.Resolved); n > 0 && len(t.Lookup.Resolved) > 0 {
			// Filed and resolved differ when a record could not be read or
			// duplicated another, and a reader chasing a missing answer
			// should not have to count two lists to notice.
			line += fmt.Sprintf("; %d of those did not reach the ruling", n)
		}
		b.WriteString(line + "\n")
	}
}

func (t *Trace) finding(b *strings.Builder, f Finding) {
	where := f.File
	if f.Line > 0 {
		where = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	head := fmt.Sprintf("\n%s  %s", f.ID, where)
	if f.Severity != "" {
		head += "  " + string(f.Severity)
	}
	if f.Confidence != "" {
		head += "  " + string(f.Confidence) + " confidence"
	}
	b.WriteString(head + "\n")
	b.WriteString(wrap(f.Body, "    ", "    "))

	b.WriteString(indent("asked      ", question(f)))
	t.lookupFor(b, f)
	b.WriteString(indent("ruling     ", ruling(f.Ruling, f.EvidenceFrom)))
}

// question is what the reviewer said would settle its own finding, which is
// the thing the scout was sent to do.
func question(f Finding) string {
	q := f.Question
	if q.Kind == "" {
		return "nothing: the reviewer named no check, so nothing could be looked up"
	}
	s := string(q.Kind)
	if q.Ask != "" {
		s += ": " + q.Ask
	}
	if q.Subject != "" {
		s += fmt.Sprintf(" (subject: %s)", q.Subject)
	}
	switch {
	case q.Kind == findings.QuestionDiff:
		s += "\nthe diff settles it, so no lookup was sent"
	case q.Kind == findings.QuestionNone:
		s += "\nnothing would settle it, so no lookup was sent"
	case !f.Asked:
		// Answerable and not asked: already answered on this pull request, or
		// the pass never got as far as asking.
		s += "\nnot sent to the lookups"
	}
	return s
}

// lookupFor is what the scout came back with for one finding, which is the
// only place the two halves meet: a record says which question it answers, so
// a finding with no record against it was checked by nothing.
func (t *Trace) lookupFor(b *strings.Builder, f Finding) {
	if !f.Asked {
		return
	}
	got := t.answersFor(f.ID)
	if len(got) == 0 {
		b.WriteString(indent("looked up  ",
			"nothing was filed against this question, so the ruling had no answer to read"))
		return
	}
	lines := []string{plural(len(got), "range") + " the ruling was shown"}
	for _, r := range got {
		lines = append(lines, resolvedLine(r))
	}
	b.WriteString(indent("looked up  ", strings.Join(lines, "\n")))
}

func resolvedLine(r Resolved) string {
	s := fmt.Sprintf("%s %s:%d-%d", r.Role, r.File, r.StartLine, r.EndLine)
	if r.Symbol != "" {
		s += " " + r.Symbol
	}
	if r.FoundVia != "" {
		s += " (found by " + r.FoundVia + ")"
	}
	return s
}

func ruling(r findings.Ruling, from string) string {
	if r.Verdict == "" {
		return "none: nothing ruled on this finding, so it was posted as the reviewer wrote it"
	}
	s := r.Verdict
	if r.Verdict == findings.VerifiedKept {
		s += " and posted"
	} else {
		s += " and not posted"
	}
	if r.Evidence != "" {
		s += "\nevidence (" + source(r, from) + "): " + r.Evidence
	}
	if r.Why != "" {
		s += "\n" + r.Why
	}
	if r.Analysis != "" {
		// The working, not the conclusion. A verdict that reads wrong is
		// usually wrong somewhere in here, and this is the only place it is
		// kept: the report shows it beside the finding, and a withdrawn
		// finding is not on the report the author sees.
		s += "\nworking: " + r.Analysis
	}
	return s
}

// source says where the line a ruling rested on came from, which is the
// question the lookups are paid to answer. A ruling that quotes something
// nobody showed it is quoting its own memory, and the pass records that rather
// than trusting it.
func source(r findings.Ruling, from string) string {
	switch {
	case from == FromLookup:
		return "from a range the lookups filed"
	case from == FromElsewhere:
		return "from the diff or the findings, not from a lookup"
	case r.Grounded:
		return "found in what it was shown"
	}
	return "not found in what it was shown"
}

// unanswered is the list this whole command is for: the questions that went
// out and came back with nothing. Each one is a finding the ruling could only
// call unverifiable, and the cause is above it in the search.
func (t *Trace) unanswered(b *strings.Builder) {
	var open []Finding
	for _, f := range t.Findings {
		if f.Asked && len(t.answersFor(f.ID)) == 0 {
			open = append(open, f)
		}
	}
	if len(open) == 0 {
		return
	}
	fmt.Fprintf(b, "\nquestions the lookups did not answer (%d)\n", len(open))
	for _, f := range open {
		q := f.Question
		ask := q.Subject
		if ask == "" {
			ask = q.Ask
		}
		fmt.Fprintf(b, "  %s  %s %s\n", f.ID, q.Kind, strings.TrimSpace(ask))
	}
	if t.Lookup.CapHit {
		b.WriteString("  the search stopped at its cost cap, so some of these were never reached\n")
	}
}

// search is the run end to end, which is where a question with no answer gets
// explained: a grep that matched nothing, a path the scout mistyped, a record
// refused for a reason it had no turn left to act on.
func (t *Trace) search(b *strings.Builder) {
	if len(t.Lookup.Calls) == 0 {
		return
	}
	b.WriteString("\nwhat the scout did\n")
	turn := 0
	for _, c := range t.Lookup.Calls {
		if c.Turn != turn {
			turn = c.Turn
			fmt.Fprintf(b, "  turn %d\n", turn)
		}
		mark := " "
		if c.Failed {
			mark = "!"
		}
		fmt.Fprintf(b, "  %s %s %s\n", mark, c.Tool, c.Args)
		fmt.Fprintf(b, "      %s\n", c.Result)
	}
}

func (t *Trace) notes(b *strings.Builder) {
	if len(t.Lookup.Notes) == 0 {
		return
	}
	b.WriteString("\nthe scout's notes\n")
	for _, n := range t.Lookup.Notes {
		b.WriteString(wrap(n, "  - ", "    "))
	}
}

// restedOnALookup counts the rulings that quoted a range the scout fetched.
// It is the measurement the whole second stage is for: a run where nothing
// rested on a lookup either had a change the diff settled by itself or had a
// scout that fetched the wrong things.
func (t *Trace) restedOnALookup() int {
	n := 0
	for _, f := range t.Findings {
		if f.EvidenceFrom == FromLookup {
			n++
		}
	}
	return n
}

// answersFor is what the scout filed against one finding. Resolved is read
// rather than Filed: what matters to a ruling is what it was shown, and a
// record whose file could not be read is filed and never shown.
func (t *Trace) answersFor(id string) []Resolved {
	var out []Resolved
	for _, r := range t.Lookup.Resolved {
		if r.Answers == id {
			out = append(out, r)
		}
	}
	return out
}

// indent prints a labelled block: the label once, the rest lined up under it,
// each line folded to the width. A ruling's reason is a paragraph the model
// wrote and nothing bounds its length.
func indent(label, body string) string {
	body = strings.TrimRight(body, "\n")
	if strings.TrimSpace(body) == "" {
		return ""
	}
	rest := "    " + strings.Repeat(" ", len(label))
	var b strings.Builder
	for i, line := range strings.Split(body, "\n") {
		first := rest
		if i == 0 {
			first = "    " + label
		}
		b.WriteString(wrap(line, first, rest))
	}
	return b.String()
}

// wrap folds a paragraph to the terminal width, under one indent for the
// first line and another for the rest, so a bullet keeps its bullet and its
// continuation lines line up under the text rather than under the dash.
func wrap(s, first, rest string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	prefix := first
	for _, para := range strings.Split(s, "\n") {
		line := prefix
		for _, word := range strings.Fields(para) {
			if len(line)+len(word)+1 > wrapAt && strings.TrimSpace(line) != "" {
				b.WriteString(strings.TrimRight(line, " ") + "\n")
				line, prefix = rest, rest
			}
			line += word + " "
		}
		if strings.TrimSpace(line) != "" {
			b.WriteString(strings.TrimRight(line, " ") + "\n")
			prefix = rest
		}
	}
	return b.String()
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
