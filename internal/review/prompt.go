package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

// systemPrompt is the harness half: the output contract, the silence rules,
// and what makes a finding worth writing. It says nothing about any
// language. The language half arrives in the envelope's promptFragment,
// authored by whoever wrote the provider, and is concatenated below.
//
// Three instructions here carry most of the weight.
//
// Not restating priors is what moves the model's attention off what the
// tools already caught and onto what static analysis structurally cannot
// see. Connecting two priors is named as valuable because it is the one
// thing no single producer can do, and a model will not volunteer it unless
// told the connection is the finding.
//
// Zero findings being valid is the hardest of the three to get and the one
// most homegrown reviewers miss. A reviewer that always finds something is
// not a reviewer, it is a generator, and the first time it invents a problem
// on a clean change is the last time anyone reads its output.
const systemPrompt = `You are reviewing one change in a code repository, once, in a single pass.

You have no tools. Everything you get to see is below. If a question cannot be
answered from what is here, do not guess at it and do not raise it: say nothing
about it. An unanswerable question raised as a finding costs the reader more
than it saves.

You are given findings that deterministic tools already produced for this
change. Treat them as established and already on the report.

- Do not restate them. A finding that repeats one of them is worse than no
  finding, because it makes the reader read the same thing twice and trust the
  list less.
- Connecting two of them IS a finding, and it is the most valuable thing you
  can produce here. A migration that adds a non-nullable column and a struct
  field that cannot express absence are each unremarkable alone. Together they
  say the write path is about to break. When you make that connection, set
  category to "correlation" and put the fingerprints of both priors in
  relatedFindings.
- Reference a prior by its fingerprint rather than describing it again.

What is worth reporting, given tools have already run:

- The change does not do what its commits and its shape say it does.
- An invariant that holds elsewhere in this code no longer holds here.
- An error path that cannot be reached, or one that is reached and swallowed.
- A caller you were shown that this change breaks.
- Two facts in the material below that contradict each other.
- A change that undoes an earlier deliberate fix, when the history shows one.

What is not worth reporting:

- Style, formatting, and naming, unless the change makes the code wrong.
- Anything a linter would catch. One already ran.
- Test coverage as a number. That is measured elsewhere.
- Praise, summaries of what the diff plainly shows, or advice to "consider"
  something without saying what breaks if it is not done.
- Anything you would qualify with "may", "might", or "could potentially" and
  cannot follow with a concrete consequence.

Zero findings is a valid and expected result. Roughly three in ten real
changes deserve no comment at all. When this is one of them, return an empty
comments array and say so in the overview. Do not pad. Do not find something
because finding something feels like the job.

Set confidence honestly. Report a finding you are unsure of with confidence
"low" rather than withholding it. Low-confidence findings are folded away on
the report, so an uncertain finding costs the reader nothing and a withheld
one costs them the finding.

Write plainly. One or two sentences per comment, naming the specific thing and
what happens because of it.`

// Build assembles the whole user-side prompt for one review.
func (in Input) build(budget envelope.Budgeted) string {
	var b strings.Builder
	b.WriteString(in.changeSection())
	b.WriteString(in.priorsSection())
	b.WriteString(in.absentSection())
	if ctx := budget.Render(); ctx != "" {
		b.WriteString("## Context beyond the diff\n")
		b.WriteString("\nResolved by " + providerNames(in.Envelopes) + ". Each block says what it is: ")
		b.WriteString("an enclosing declaration, a caller of something this change touched, ")
		b.WriteString("a type in a changed signature, a sibling implementation, a test, or prior history of these lines.\n")
		b.WriteString(ctx)
		b.WriteString("\n")
	}
	b.WriteString(in.diffSection())
	return b.String()
}

func providerNames(envs []*envelope.Envelope) string {
	var names []string
	for _, e := range envs {
		if e != nil {
			names = append(names, providerName(e))
		}
	}
	if len(names) == 0 {
		return "the context provider"
	}
	return strings.Join(names, " and ")
}

func providerName(e *envelope.Envelope) string {
	if e == nil || e.Provider.Name == "" {
		return "the context provider"
	}
	if e.Provider.Version == "" {
		return e.Provider.Name
	}
	return e.Provider.Name + " " + e.Provider.Version
}

func (in Input) changeSection() string {
	var b strings.Builder
	b.WriteString("## The change\n\n")
	if in.Change == nil {
		b.WriteString("No file-level detail was available.\n\n")
		if gen := in.generatedLine(); gen != "" {
			b.WriteString(gen + "\n\n")
		}
		return b.String()
	}
	if len(in.Change.Commits) > 0 {
		b.WriteString("Commits:\n")
		for _, c := range in.Change.Commits {
			b.WriteString("- " + strings.TrimSpace(c.Subject) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Files:\n")
	for _, f := range in.Change.Files {
		fmt.Fprintf(&b, "- %s (%s, +%d -%d)\n", f.Path, f.Status, f.Added, f.Removed)
	}
	b.WriteString("\n")
	if gen := in.generatedLine(); gen != "" {
		b.WriteString(gen + "\n\n")
	}
	return b.String()
}

// generatedLine is the whole of what generated files get: a count. Pasting
// them in would spend thirty thousand tokens on text no reviewer would ever
// act on, and dropping them silently would hide that they moved at all.
func (in Input) generatedLine() string {
	var paths []string
	if in.Report != nil {
		paths = append(paths, in.Report.Coverage.Generated...)
	}
	for _, e := range in.Envelopes {
		if e != nil {
			paths = append(paths, e.GeneratedFiles()...)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)
	uniq := paths[:0]
	var last string
	for _, p := range paths {
		if p != last {
			uniq = append(uniq, p)
			last = p
		}
	}
	return fmt.Sprintf("%d generated file(s) also changed and are not shown: %s. "+
		"They are machine output. Judge the source they were generated from, not them.",
		len(uniq), strings.Join(uniq, ", "))
}

// priorsSection is wave one's output, marked as already known. Each finding
// carries its fingerprint, which is the id wave two references it by.
func (in Input) priorsSection() string {
	if in.Report == nil || len(in.Report.Findings) == 0 {
		var b strings.Builder
		b.WriteString("## Findings already established\n\n")
		b.WriteString("None. The deterministic checks that ran found nothing to report.\n\n")
		return b.String()
	}
	var b strings.Builder
	b.WriteString("## Findings already established\n\n")
	b.WriteString("These are on the report already. Do not restate them. ")
	b.WriteString("Each is written as [id]; put that id in relatedFindings to reference it.\n\n")
	for _, f := range in.Report.Findings {
		if f.Source == findings.SourceLLM {
			// A previous reviewer's remark is not an established fact and
			// must not be presented to this one as though it were.
			continue
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if loc == "" && f.Anchor != nil {
			loc = f.Anchor.Kind + " " + f.Anchor.ID
		}
		fmt.Fprintf(&b, "- [%s] %s · %s · %s\n  %s\n",
			f.ID, f.Severity, f.Substrate, loc, f.Message)
		if f.Observed != "" {
			fmt.Fprintf(&b, "  observed: %s\n", f.Observed)
		}
	}
	b.WriteString("\n")
	if len(in.Report.Unknowns) > 0 {
		b.WriteString("Not determined by any check:\n")
		for _, u := range in.Report.Unknowns {
			fmt.Fprintf(&b, "- %s: %s\n", u.Substrate, u.Message)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// absentSection names producers that did not run. A producer that errored
// degrades to a missing input rather than blocking the review, and the model
// has to be told which inputs are missing: a check that did not run and a
// check that came back clean look identical from here.
func (in Input) absentSection() string {
	if len(in.Absent) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Checks that did not run\n\n")
	for _, a := range in.Absent {
		b.WriteString("- " + a + "\n")
	}
	b.WriteString("\nTreat these areas as unexamined. Their silence is not a pass.\n\n")
	return b.String()
}

func (in Input) diffSection() string {
	if in.Change == nil || len(in.Change.Files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## The diff\n\n")
	for _, f := range in.Change.Files {
		if f.Diff == "" {
			continue
		}
		fmt.Fprintf(&b, "### %s\n```diff\n%s\n```\n\n", f.Path, strings.TrimRight(f.Diff, "\n"))
	}
	return b.String()
}

// diffTokens estimates what the diff costs, so the caller can tell a change
// too large to review from one the ceiling merely trimmed.
func diffTokens(c *change.Set) int {
	if c == nil {
		return 0
	}
	var n int
	for _, f := range c.Files {
		n += envelope.EstimateTokens(f.Diff)
	}
	return n
}
