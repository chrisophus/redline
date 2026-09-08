// Package report renders a Report for a human. Four sections, in this order:
// what changed, what was checked and held, what could not be determined, and
// the findings. Most review tools ship only the fourth; section 3 is the one
// most easily skipped and the most costly to omit.
package report

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/pane"
)

// Markdown renders the report.
func Markdown(rep *findings.Report, renders []pane.Render, evidence map[string]pane.Artifact, ch *change.Set) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Redline\n\n")
	fmt.Fprintf(&b, "Base `%s` (`%s`) — %d file(s) changed, %d examined.\n\n",
		rep.BaseRef, short(rep.BaseSHA), rep.Coverage.ChangedFiles, rep.Coverage.ExaminedFiles)
	fmt.Fprintf(&b, "%d finding(s).\n\n", len(rep.Findings))
	banner(&b, rep)
	overviewSection(&b, rep)

	if pr := prLink(ch); pr != "" {
		fmt.Fprintf(&b, "%s\n\n", pr)
	}
	section1(&b, renders)
	coverageSection(&b, rep)
	mutationSection(&b, rep)
	fileSection(&b, ch, rep)
	compositionSection(&b, ch)
	section2(&b, rep)
	section3(&b, rep)
	section4(&b, rep, evidence)
	return b.String()
}

// overviewSection renders the agent's summary of the change, when it wrote
// one, marked as the agent's.
func overviewSection(b *strings.Builder, rep *findings.Report) {
	if rep.Agent == nil || rep.Agent.Overview == "" {
		return
	}
	fmt.Fprintf(b, "## Review (agent)\n\n%s\n\n", rep.Agent.Overview)
}

// mutationSection renders the diff-scoped gomutants result, when a report was
// read. A survivor is a line a test runs but nothing asserts.
func mutationSection(b *strings.Builder, rep *findings.Report) {
	m := rep.Mutation
	if m == nil {
		return
	}
	fmt.Fprintf(b, "## Mutation\n\n")
	fmt.Fprintf(b, "%d mutant(s) on the added lines survived (a test runs the line but nothing fails when it changes); %d killed. From `%s`.\n\n",
		m.Lived, m.Killed, m.Report)
	for _, fs := range m.Survived {
		for _, mt := range fs.Mutants {
			fmt.Fprintf(b, "- `%s:%d` — %s", fs.Path, mt.Line, mt.Mutator)
			if mt.Original != "" {
				fmt.Fprintf(b, ": `%s` -> `%s`", mt.Original, mt.Replacement)
			}
			if mt.Verdict != nil {
				fmt.Fprintf(b, " — agent: %s", mt.Verdict.Ruling)
				if mt.Verdict.Rationale != "" {
					fmt.Fprintf(b, " (%s)", mt.Verdict.Rationale)
				}
			}
			if repro := mt.Repro(); repro != "" {
				fmt.Fprintf(b, " — repro: `%s`", repro)
			}
			fmt.Fprintln(b)
		}
	}
	if len(m.Survived) > 0 {
		fmt.Fprintln(b)
	}
	if m.Infra > 0 {
		fmt.Fprintf(b, "%d mutant(s) on these lines could not be run: the test binary failed for a reason of its own, "+
			"so those lines were not measured. They are not killed.\n\n", m.Infra)
		for _, fs := range m.Unreliable {
			for _, mt := range fs.Mutants {
				fmt.Fprintf(b, "- `%s:%d` — %s (run failed)", fs.Path, mt.Line, mt.Mutator)
				if repro := mt.Repro(); repro != "" {
					fmt.Fprintf(b, " — repro: `%s`", repro)
				}
				fmt.Fprintln(b)
			}
		}
		if len(m.Unreliable) > 0 {
			fmt.Fprintln(b)
		}
	}
	if m.Equivalent > 0 {
		fmt.Fprintf(b, "%d mutant(s) are equivalent: the mutated program does the same thing, so no test can kill them. Not a gap.\n\n",
			m.Equivalent)
	}
}

// banner states up front when Redline covered little or none of the change.
// An empty report is ambiguous by nature — every check clean, or nothing
// looked — and the reader will assume the flattering reading unless told
// otherwise, before they read anything else.
func banner(b *strings.Builder, rep *findings.Report) {
	if rep.Coverage.ChangedFiles == 0 {
		fmt.Fprintf(b, "> **Nothing to review.** The working tree matches the base revision.\n\n")
		return
	}
	switch {
	case rep.Coverage.ExaminedFiles == 0:
		if len(rep.Findings) > 0 {
			fmt.Fprintf(b, "> **Redline's panes examined none of this change.** No pane it currently "+
				"ships covers these files. The findings below come from the agent's review.\n\n")
		} else {
			fmt.Fprintf(b, "> **Redline examined none of this change.** No pane it currently ships "+
				"covers these files, so the empty findings list below says nothing about whether "+
				"the change is correct. Review it by hand.\n\n")
		}
	case len(rep.FailedSubstrates()) > 0:
		fmt.Fprintf(b, "> **%d pane(s) applied to this change and did not run.** "+
			"See _What could not be determined_.\n\n", len(rep.FailedSubstrates()))
	}
}

// prLink names the pull request under review, when there is one.
func prLink(ch *change.Set) string {
	if ch == nil || ch.Target == nil || ch.Target.PR == nil {
		return ""
	}
	pr := ch.Target.PR
	label := prLabel(pr.Number)
	if pr.URL != "" {
		label = fmt.Sprintf("[%s](%s)", label, pr.URL)
	}
	if pr.Title != "" {
		label += " " + pr.Title
	}
	return label
}

func prLabel(n int) string {
	if n == 0 {
		return "PR"
	}
	return fmt.Sprintf("PR #%d", n)
}

// coverageSection is the number that stands in for reading the tests. An
// absent profile is stated as absent: "no test executes these lines" and
// "nobody measured" are different claims and only one is the author's problem.
// A change no profile could describe gets no section at all.
func coverageSection(b *strings.Builder, rep *findings.Report) {
	if !rep.Coverage.CoverageApplies() {
		return
	}
	fmt.Fprintf(b, "## Coverage\n\n")
	c := rep.Coverage.Diff
	if c == nil {
		fmt.Fprintf(b, "_No coverage profile was found, so whether these changes are tested is unknown. "+
			"That is not the same as untested — run the suite with `-coverprofile=coverage.out`._\n\n")
		return
	}
	if c.Stale {
		fmt.Fprintf(b, "> **The profile `%s` predates this change**, so its number does not describe "+
			"the code under review. Re-run the suite with `-coverprofile`.\n\n", c.Profile)
	}
	if c.Percent < 0 {
		fmt.Fprintf(b, "No added line is coverable, so there is nothing for a test to execute (`%s`).\n\n", c.Profile)
	} else {
		fmt.Fprintf(b, "**%.0f%%** of the %d coverable line(s) this change adds are executed by a test, "+
			"according to `%s`. %d covered, %d not.\n\n",
			c.Percent, c.Lines, c.Profile, c.Covered, c.Lines-c.Covered)
	}
	if c.TotalPercent >= 0 {
		fmt.Fprintf(b, "Repository-wide, **%.0f%%** of %d coverable line(s) in `%s` are covered "+
			"(%d covered, %d not) — the profile's whole scope, not just what this change added.\n\n",
			c.TotalPercent, c.TotalLines, c.Profile, c.TotalCovered, c.TotalLines-c.TotalCovered)
	}
	for _, gap := range c.Uncovered {
		fmt.Fprintf(b, "- `%s` — %d uncovered added line(s)\n", gap.Path, len(gap.Lines))
	}
	if len(c.Uncovered) > 0 {
		fmt.Fprintln(b)
	}
}

// fileSection is the walkthrough: every changed file, with its finding count.
// Shown even when no pane ran — that is when a reviewer most needs a
// file-by-file account of what moved.
func fileSection(b *strings.Builder, ch *change.Set, rep *findings.Report) {
	if ch == nil || len(ch.Files) == 0 {
		return
	}
	fmt.Fprintf(b, "## Files\n\n")
	for _, row := range fileWalk(ch.Files, rep.Findings) {
		fmt.Fprintf(b, "- `%s` (%s, +%d −%d)", row.Path, row.Status, row.Added, row.Removed)
		if row.Findings > 0 {
			fmt.Fprintf(b, ", %d finding(s)", row.Findings)
		}
		if rep.Agent != nil {
			if s := rep.Agent.Files[row.Path]; s != "" {
				fmt.Fprintf(b, ": %s", s)
			}
		}
		fmt.Fprintln(b)
	}
	fmt.Fprintln(b)
}

// compositionSection is the diff's lines grouped by language and kind — test
// versus behavior versus config versus prose — so a reviewer knows the shape
// of the change before reading a single line of it.
func compositionSection(b *strings.Builder, ch *change.Set) {
	if ch == nil {
		return
	}
	rows := change.Composition(ch.Files)
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(b, "## Lines by language and type\n\n")
	fmt.Fprintf(b, "| Language | Type | Files | + | − |\n|---|---|---:|---:|---:|\n")
	for _, r := range rows {
		fmt.Fprintf(b, "| %s | %s | %d | %d | %d |\n", r.Language, r.Kind, r.Files, r.Added, r.Removed)
	}
	fmt.Fprintln(b)
}

// section1 is the rendered evidence, in the domain of the change.
func section1(b *strings.Builder, renders []pane.Render) {
	fmt.Fprintf(b, "## What changed\n\n")
	if len(renders) == 0 {
		fmt.Fprintf(b, "_No pane examined this change; there is no evidence to show._\n\n")
		return
	}
	for _, r := range renders {
		fmt.Fprintf(b, "### %s\n\n%s\n\n", r.Title, r.Summary)
		if len(r.Lines) > 0 {
			fmt.Fprintf(b, "```\n%s\n```\n\n", strings.Join(r.Lines, "\n"))
		}
	}
}

// section2 is the confirmations: every question the reviewer does not need to
// ask again. Collapsed for density, never discarded.
func section2(b *strings.Builder, rep *findings.Report) {
	fmt.Fprintf(b, "## What was checked and held\n\n")
	if len(rep.Confirmations) == 0 {
		fmt.Fprintf(b, "_Nothing was confirmed — no check reached a clean answer on this change._\n\n")
		return
	}
	fmt.Fprintf(b, "<details>\n<summary>%d confirmation(s)</summary>\n\n", len(rep.Confirmations))
	for _, c := range rep.Confirmations {
		fmt.Fprintf(b, "- **%s** — %s\n", c.Rule, c.Message)
	}
	fmt.Fprintf(b, "\n</details>\n\n")
}

// section3 is what could not be determined, and why. A pane that did not run
// renders here loudly: an empty pane must never be readable as "nothing to
// worry about".
func section3(b *strings.Builder, rep *findings.Report) {
	fmt.Fprintf(b, "## What could not be determined\n\n")
	failed := rep.FailedSubstrates()
	if len(rep.Unknowns) == 0 && len(failed) == 0 {
		if n := len(rep.Coverage.Unexamined); n > 0 {
			fmt.Fprintf(b, "Every pane that applies ran, but %d changed file(s) were not in any pane's scope.\n\n", n)
		} else {
			fmt.Fprintf(b, "Nothing. Every check that applies to these %d file(s) ran and answered.\n\n",
				rep.Coverage.ExaminedFiles)
		}
	}
	for _, s := range failed {
		fmt.Fprintf(b, "- **%s did not run.** %s\n", s.Name, s.Detail)
	}
	for _, u := range rep.Unknowns {
		line := fmt.Sprintf("- **%s** — %s", u.Substrate, u.Message)
		if u.Reason != "" {
			line += " (" + u.Reason + ")"
		}
		fmt.Fprintf(b, "%s\n", line)
	}
	if len(rep.Unknowns) > 0 || len(failed) > 0 {
		fmt.Fprintln(b)
	}
	if n := len(rep.Coverage.Unexamined); n > 0 && rep.Coverage.ExaminedFiles > 0 {
		fmt.Fprintf(b, "<details>\n<summary>%d changed file(s) no pane examined</summary>\n\n", n)
		for _, path := range rep.Coverage.Unexamined {
			fmt.Fprintf(b, "- `%s`\n", path)
		}
		fmt.Fprintf(b, "\n</details>\n\n")
	}
	if n := len(rep.Coverage.Generated); n > 0 {
		// Named, not just counted. Excluding a file a human wrote is the one
		// way suppression can hide a real change, and the reader can only
		// catch that if the list is here.
		fmt.Fprintf(b, "<details>\n<summary>%d generated file(s) excluded from this review</summary>\n\n", n)
		for _, path := range rep.Coverage.Generated {
			fmt.Fprintf(b, "- `%s`\n", path)
		}
		fmt.Fprintf(b, "\n</details>\n\n")
	}
	skipped := 0
	for _, s := range rep.Substrates {
		if s.State == findings.SubstrateSkipped {
			skipped++
		}
	}
	if skipped > 0 {
		fmt.Fprintf(b, "<details>\n<summary>%d pane(s) did not apply to this change</summary>\n\n", skipped)
		for _, s := range rep.Substrates {
			if s.State == findings.SubstrateSkipped {
				fmt.Fprintf(b, "- `%s` — %s\n", s.Name, s.Detail)
			}
		}
		fmt.Fprintf(b, "\n</details>\n\n")
	}
}

// section4 is the findings, ranked.
func section4(b *strings.Builder, rep *findings.Report, evidence map[string]pane.Artifact) {
	fmt.Fprintf(b, "## Findings\n\n")
	if len(rep.Findings) == 0 {
		if rep.Coverage.ExaminedFiles == 0 {
			fmt.Fprintf(b, "None — because nothing was checked. See the note at the top.\n")
		} else {
			fmt.Fprintf(b, "None.\n")
		}
		return
	}
	for _, f := range rep.Findings {
		loc := f.File
		if loc == "" {
			loc = f.Anchor.Key()
		}
		fmt.Fprintf(b, "### %s — `%s`\n\n", strings.ToUpper(string(f.Severity)), f.Rule)
		fmt.Fprintf(b, "%s\n\n", f.Message)
		fmt.Fprintf(b, "- location: `%s`\n", loc)
		if f.Expected != "" {
			fmt.Fprintf(b, "- expected: %s\n", f.Expected)
		}
		if f.Observed != "" {
			fmt.Fprintf(b, "- observed: %s\n", f.Observed)
		}
		if f.Verdict != nil {
			fmt.Fprintf(b, "- agent verdict: %s", f.Verdict.Ruling)
			if f.Verdict.Rationale != "" {
				fmt.Fprintf(b, " — %s", f.Verdict.Rationale)
			}
			fmt.Fprintf(b, "\n")
			if f.Verdict.Fix != "" {
				fmt.Fprintf(b, "- agent fix: %s\n", f.Verdict.Fix)
			}
		}
		fmt.Fprintf(b, "- source: %s\n", f.Source)
		if f.Confidence != "" {
			fmt.Fprintf(b, "- confidence: %s\n", f.Confidence)
		}
		emitRelated(b, rep, f)
		if f.FixCmd != "" {
			fmt.Fprintf(b, "- fix: %s\n", f.FixCmd)
		}
		emitEvidence(b, f, evidence)
		if f.Context != "" {
			fmt.Fprintf(b, "\n> %s\n", f.Context)
		}
		fmt.Fprintln(b)
	}
}

// maxInlineEvidence caps what is inlined into the report. Larger artifacts
// stay on disk under .redline/evidence/ and are referenced by path.
const maxInlineEvidence = 4000

// emitEvidence shows the artifact behind a finding. The claim and the thing it
// rests on belong in the same place; a reviewer who has to go find the
// evidence is back to taking the tool's word for it.
// emitRelated names the findings a correlation was built from, rather than
// repeating what they said. The point of the reference is that the reader can
// see both halves of the connection without the same text appearing twice on
// one page.
func emitRelated(b *strings.Builder, rep *findings.Report, f findings.Finding) {
	if len(f.RelatedFindings) == 0 {
		return
	}
	for _, ref := range f.RelatedFindings {
		prior := rep.FindRef(ref)
		if prior == nil {
			// A reference to a finding this run does not have is dropped.
			// Rendering a broken link would be worse than saying nothing.
			continue
		}
		loc := prior.File
		if loc == "" && prior.Anchor != nil {
			loc = prior.Anchor.Key()
		}
		fmt.Fprintf(b, "- builds on: `%s` (%s · %s)\n", prior.Rule, prior.Substrate, loc)
	}
}

func emitEvidence(b *strings.Builder, f findings.Finding, evidence map[string]pane.Artifact) {
	for _, id := range f.Evidence {
		a, ok := evidence[id]
		if !ok || a.Content == "" {
			continue
		}
		if len(a.Content) > maxInlineEvidence {
			fmt.Fprintf(b, "- evidence: `.redline/evidence/%s` (%d bytes)\n", evidenceFile(id), len(a.Content))
			continue
		}
		fmt.Fprintf(b, "\n```%s\n%s\n```\n", a.Kind, strings.TrimRight(a.Content, "\n"))
	}
}

// evidenceFile turns an observation ID into a filename under .redline/evidence/.
func evidenceFile(id string) string {
	repl := strings.NewReplacer("/", "_", ":", "_", " ", "_", "..", "_")
	name := filepath.Base(repl.Replace(id))
	if name == "" || name == "." {
		return "artifact"
	}
	return name
}

// EvidenceFile is evidenceFile, exported for the writer that persists artifacts.
func EvidenceFile(id string) string { return evidenceFile(id) }

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
