// Package report renders a Report for a human. Four sections, in this order:
// what changed, what was checked and held, what could not be determined, and
// the findings. Most review tools ship only the fourth; section 3 is the one
// most easily skipped and the most costly to omit.
package report

import (
	"fmt"
	"strings"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/pane"
)

// Markdown renders the report. Rung 1's visual output is markdown; report.html
// arrives with the panes whose evidence is actually visual.
func Markdown(rep *findings.Report, renders []pane.Render, evidence map[string]pane.Artifact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Redline\n\n")
	fmt.Fprintf(&b, "Base `%s` (`%s`) — %d file(s) changed, %d examined.\n\n",
		rep.BaseRef, short(rep.BaseSHA), rep.Coverage.ChangedFiles, rep.Coverage.ExaminedFiles)
	fmt.Fprintf(&b, "%d finding(s).\n\n", len(rep.Findings))
	banner(&b, rep)

	section1(&b, renders)
	section2(&b, rep)
	section3(&b, rep)
	section4(&b, rep, evidence)
	return b.String()
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
		fmt.Fprintf(b, "> **Redline examined none of this change.** No pane it currently ships "+
			"covers these files, so the empty findings list below says nothing about whether "+
			"the change is correct. Review it by hand.\n\n")
	case len(rep.DarkSubstrates()) > 0:
		fmt.Fprintf(b, "> **%d pane(s) applied to this change and did not run.** "+
			"See _What could not be determined_.\n\n", len(rep.DarkSubstrates()))
	}
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
	dark := rep.DarkSubstrates()
	if len(rep.Unknowns) == 0 && len(dark) == 0 {
		fmt.Fprintf(b, "Nothing. Every check that applies to these %d file(s) ran and answered.\n\n",
			rep.Coverage.ExaminedFiles)
	}
	for _, s := range dark {
		fmt.Fprintf(b, "- **%s did not run.** %s\n", s.Name, s.Detail)
	}
	for _, u := range rep.Unknowns {
		line := fmt.Sprintf("- **%s** — %s", u.Substrate, u.Message)
		if u.Reason != "" {
			line += " (" + u.Reason + ")"
		}
		fmt.Fprintf(b, "%s\n", line)
	}
	if len(rep.Unknowns) > 0 || len(dark) > 0 {
		fmt.Fprintln(b)
	}
	if n := len(rep.Coverage.Unexamined); n > 0 && rep.Coverage.ExaminedFiles > 0 {
		fmt.Fprintf(b, "<details>\n<summary>%d changed file(s) no pane examined</summary>\n\n", n)
		for _, path := range rep.Coverage.Unexamined {
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
		fmt.Fprintf(b, "- source: %s\n", f.Source)
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
	repl := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return repl.Replace(id)
}

// EvidenceFile is evidenceFile, exported for the writer that persists artifacts.
func EvidenceFile(id string) string { return evidenceFile(id) }

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
