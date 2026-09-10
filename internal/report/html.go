// HTML rendering. One self-contained file, no server, no network: the design
// cut the web app, and this is what replaces it. If it turns out a static file
// cannot shift a reviewer into review mode, an app can be built later over the
// same JSON — but that is a decision to make from evidence, not in advance.
package report

import (
	"embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/mutation"
	"github.com/chrisophus/redline/internal/pane"
)

//go:embed assets/report.html.tmpl
var assets embed.FS

// HTMLInput is everything the page renders.
type HTMLInput struct {
	Report   *findings.Report
	Change   *change.Set
	Renders  []pane.Render
	Evidence map[string]pane.Artifact
	// LineCoverage overlays which changed lines a test ran, per file, keyed by
	// new-file line. Nil when no coverage profile was found.
	LineCoverage map[string]map[int]bool
}

// view is the flattened shape the template consumes.
type view struct {
	Title    string
	Subtitle string
	URL      string
	Author   string
	Coverage findings.Coverage
	Banner   string
	Counts   map[string]int

	// Overview is the agent's summary of the change, from review.json. Empty
	// when the agent wrote none; the Review section and its nav entry render
	// only when it is present.
	Overview string

	// Mutation is the diff-scoped gomutants result, when a report was on disk.
	// Nil leaves the Mutation section and its nav entry off the page.
	Mutation *mutation.Result

	// Renders is what changed, in the domain where it lives — a pane's own
	// account (e.g. the lint delta's introduced/resolved count, or a
	// suppression's added-directive list), not a finding. Markdown has
	// rendered this since section1 existed; HTML dropped it on the floor
	// until a report with a real lint delta and no visible "Lint" anywhere
	// on the page exposed the gap.
	Renders []pane.Render

	// DiffFiles is the deduped set of changed files that appear in a drill-in
	// area, each with its diff. Rendered once into a hidden store the drawer
	// moves from, so a diff exists in exactly one place in the DOM.
	DiffFiles []fileView
	Observed  []findingView

	// LowConfidence is the agent findings that said so themselves. They are
	// folded away rather than dropped: the review prompt promises an
	// uncertain finding costs the reader nothing, and a reviewer who is
	// charged full price for hedging stops hedging.
	LowConfidence []findingView

	Groups   []drillGroup
	Confirms []findings.Confirmation
	Unknowns []findings.Unknown
	Failed   []findings.SubstrateStatus
	Skipped  []findings.SubstrateStatus
	// UITouched is whether the change moves the interface at all. A change
	// that touches no UI gets no interface section; on one that does, the
	// absence of captures is a gap and is said so.
	UITouched bool
	// Nav is the sidebar: one entry per section actually rendered, in the order
	// they appear. Built here rather than scanned out of the DOM so the page
	// has a map before any script runs.
	Nav []navLink

	Commits int
	// Identity keys browser-local comments to this review, not to the
	// report.html path — every run overwrites the same file.
	Identity string
}

// navLink is one sidebar entry.
//
// Count is shown when a section has a number worth knowing before you scroll to
// it. Warn marks a section that is a gap rather than a result — no captures of a
// UI that moved, no coverage profile, something undetermined — so the sidebar
// answers "what is missing here" without reading the page.
type navLink struct {
	ID    string
	Label string
	Count int
	Warn  bool
}

// findingView is one finding as a card renders it. Heading and Body exist
// because an agent comment arrives with its entire remark in Message: a
// one-line deterministic message is a title, a paragraph is not, and the two
// have to share this template.
type findingView struct {
	findings.Finding
	Evidence template.HTML
	// Related are the priors a correlation names, resolved against this run.
	// A reference to a finding this run does not have is dropped here, so the
	// card never shows a broken link.
	Related []relatedRef
}

// relatedRef is a prior finding a correlation builds on: enough to recognise
// and find it, and deliberately not its message. The point of a reference is
// that the reader sees both halves of the connection without the same text
// appearing twice on one page.
type relatedRef struct {
	Rule     string
	Location string
	ID       string
}

// headingMax is how long a message may be before the card splits it — about
// one line of h3 at the report's width. Past that the heading sets as a block
// of bold prose: on two real reports, 10 of 24 headings ran over 200
// characters and the longest was 1023, eleven lines deep, which is the end of
// the ten-second orientation the briefing exists for. A message this short
// renders exactly as it always has.
const headingMax = 120

// Heading is the card's title: the whole message when it is short enough to
// be one, otherwise its first sentence or its first line, whichever ends
// sooner.
func (f findingView) Heading() string {
	head, _ := splitMessage(f.Message)
	return head
}

// Body is what the heading left behind, for the slot a deterministic card
// fills with evidence rows. Empty on a short message, so that card keeps its
// single-line title and nothing else moves.
func (f findingView) Body() template.HTML {
	_, rest := splitMessage(f.Message)
	if rest == "" {
		return ""
	}
	return template.HTML(codeSpans(template.HTMLEscapeString(rest)))
}

// drillGroup is one language/kind cell of the drill-in: the files of that
// language and role, each openable in the drawer, with the line totals the
// composition table used to show on its own.
type drillGroup struct {
	Label   string
	Count   int
	Added   int
	Removed int
	Files   []fileView
}

type fileView struct {
	Path     string
	Status   string
	Summary  string
	Added    int
	Removed  int
	Findings int
	Severity string
	Diff     template.HTML
	Survived []int // new-side lines with a mutant a test ran but did not catch
}

// HTML renders the report page.
func HTML(in HTMLInput) (string, error) {
	tmpl, err := template.New("report.html.tmpl").Funcs(template.FuncMap{
		"lower": func(v any) string { return strings.ToLower(fmt.Sprint(v)) },
		"sub":   func(a, b int) int { return a - b },
	}).ParseFS(assets, "assets/report.html.tmpl")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, buildView(in)); err != nil {
		return "", err
	}
	return b.String(), nil
}

func buildView(in HTMLInput) view {
	rep := in.Report
	v := view{
		Title:    "Redline",
		Coverage: rep.Coverage,
		Renders:  in.Renders,
		Confirms: rep.Confirmations,
		Unknowns: rep.Unknowns,
		Failed:   rep.FailedSubstrates(),
		Counts:   map[string]int{},
	}
	for _, s := range rep.Substrates {
		if s.State == findings.SubstrateSkipped {
			v.Skipped = append(v.Skipped, s)
		}
	}
	if ch := in.Change; ch != nil {
		v.Commits = len(ch.Commits)
		v.UITouched = ch.UITouched
		if ch.Target != nil {
			v.Subtitle = ch.Target.Describe()
			if ch.Target.PR != nil {
				v.URL = ch.Target.PR.URL
				v.Author = ch.Target.PR.Author
			}
		}
	}
	v.Identity = change.ReviewIdentity(rep.BaseSHA, in.Change)
	if v.Subtitle == "" {
		v.Subtitle = fmt.Sprintf("base %s", short(rep.BaseSHA))
	}
	v.Banner = bannerText(rep)
	if rep.Agent != nil {
		v.Overview = rep.Agent.Overview
	}
	v.Mutation = rep.Mutation

	for _, f := range rep.Findings {
		v.Counts[string(f.Severity)]++
		fv := findingView{Finding: f}
		var ev strings.Builder
		for _, id := range f.Evidence {
			if a, ok := in.Evidence[id]; ok && a.Content != "" && len(a.Content) < maxInlineEvidence {
				ev.WriteString(highlightDiff(a.Content))
			}
		}
		fv.Evidence = template.HTML(ev.String())
		fv.Related = relatedRefs(rep, f)
		if f.Confidence == findings.ConfidenceLow {
			v.LowConfidence = append(v.LowConfidence, fv)
			continue
		}
		v.Observed = append(v.Observed, fv)
	}

	if in.Change != nil {
		count, worst := fileFindingCounts(rep.Findings)
		findingLines := map[string][]int{}
		for _, f := range rep.Findings {
			if f.File != "" && f.Line > 0 {
				findingLines[f.File] = append(findingLines[f.File], f.Line)
			}
		}
		headDir := ""
		if in.Change.Target != nil {
			headDir = in.Change.Target.Dir
		}
		survived := map[string][]int{}
		if rep.Mutation != nil {
			for _, fs := range rep.Mutation.Survived {
				for _, m := range fs.Mutants {
					survived[fs.Path] = append(survived[fs.Path], m.Line)
				}
			}
		}
		seen := map[string]bool{}
		for _, g := range change.CompositionGroups(in.Change.Files) {
			dg := drillGroup{Label: g.Language + " " + g.Kind, Count: len(g.Files), Added: g.Added, Removed: g.Removed}
			for _, f := range g.Files {
				diff := expandForFindings(headDir, f.Path, f.Diff, findingLines[f.Path])
				fv := fileView{Path: f.Path, Status: f.Status, Added: f.Added, Removed: f.Removed,
					Findings: count[f.Path], Severity: string(worst[f.Path]),
					Diff: template.HTML(highlightDiffFor(f.Path, diff, in.LineCoverage[f.Path]))}
				if rep.Agent != nil {
					fv.Summary = rep.Agent.Files[f.Path]
				}
				fv.Survived = survived[f.Path]
				dg.Files = append(dg.Files, fv)
				if !seen[f.Path] {
					seen[f.Path] = true
					v.DiffFiles = append(v.DiffFiles, fv)
				}
			}
			v.Groups = append(v.Groups, dg)
		}
	}
	v.Nav = navFor(v)
	return v
}

// splitMessage cuts a long message into a heading and the rest. Two
// candidates and the shorter wins: the first sentence, and the first line —
// a remark that opens with a line of its own has already said where its
// title ends.
func splitMessage(msg string) (head, rest string) {
	msg = strings.TrimSpace(msg)
	if len(msg) <= headingMax {
		return msg, ""
	}
	cut := len(msg)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		cut = i
	}
	if i := firstSentenceEnd(msg); i > 0 && i < cut {
		cut = i
	}
	return strings.TrimSpace(msg[:cut]), strings.TrimSpace(msg[cut:])
}

// sentenceEnd is a full stop that ends a sentence: terminal punctuation, any
// closing quote or bracket, then whitespace or the end of the text.
var sentenceEnd = regexp.MustCompile(`[.!?]["')\]]*(?:\s|$)`)

// minHeading is the shortest heading a sentence cut may produce. The same
// pattern matches the dot in "e.g." and "cf.", which agent prose is full of,
// and a four-character heading is worse than no split at all — so a candidate
// that short is read as an abbreviation and the scan carries on.
const minHeading = 32

// firstSentenceEnd returns the offset just past the first sentence-ending
// punctuation, or -1 when the message has none worth cutting at.
func firstSentenceEnd(msg string) int {
	for _, m := range sentenceEnd.FindAllStringIndex(msg, -1) {
		// The match swallows the space after the stop; the heading keeps the
		// punctuation itself.
		end := len(strings.TrimRight(msg[:m[1]], " \t\r\n\f\v"))
		if end >= minHeading {
			return end
		}
	}
	return -1
}

// inlineCode is a `code` span in agent prose, which quotes identifiers
// constantly and until now showed the backticks literally.
var inlineCode = regexp.MustCompile("`([^`\n]+)`")

// codeSpans turns `x` into <code>x</code>. It must run on already-escaped
// text: this is the one place the card introduces markup of its own, so what
// it wraps has to be inert before it arrives or the body becomes an injection
// point.
func codeSpans(escaped string) string {
	return inlineCode.ReplaceAllString(escaped, "<code>$1</code>")
}

// relatedRefs resolves the priors a correlation names. The markdown report has
// rendered these since the field existed and the HTML report showed nothing,
// so the connection a correlation exists to draw was visible on one of the two
// reports only.
func relatedRefs(rep *findings.Report, f findings.Finding) []relatedRef {
	var out []relatedRef
	for _, ref := range f.RelatedFindings {
		prior := rep.FindRef(ref)
		if prior == nil {
			// A reference to a finding this run does not have is dropped.
			// Rendering a broken link would be worse than saying nothing.
			continue
		}
		loc := prior.File
		if loc == "" {
			loc = prior.Anchor.Key()
		}
		if prior.Line > 0 {
			loc += ":" + strconv.Itoa(prior.Line)
		}
		out = append(out, relatedRef{Rule: prior.Rule, Location: loc, ID: prior.ID})
	}
	return out
}

// navFor lists the sections this page will render, in page order. It must stay
// in step with the template: TestNavMatchesTheSectionsOnThePage fails if a
// section gains or loses a heading without its entry moving too.
func navFor(v view) []navLink {
	// A UI that moved with nothing captured is a gap; a change with no UI files
	// has no interface section. Coverage is the same: a change with no
	// coverable file has no coverage section, and one with a coverable file
	// and no profile is a gap.
	coverageGap := v.Coverage.Diff == nil || v.Coverage.Diff.Stale

	nav := []navLink{}
	if v.Overview != "" {
		nav = append(nav, navLink{ID: "review", Label: "Review"})
	}
	if v.UITouched {
		nav = append(nav, navLink{ID: "interface", Label: "What it looks like", Warn: true})
	}
	if v.Coverage.CoverageApplies() {
		nav = append(nav, navLink{ID: "coverage", Label: "Coverage", Warn: coverageGap})
	}
	if v.Mutation != nil {
		// A run whose mutants failed on the runner is worth the same mark as
		// one with survivors: both mean the added lines are not known to be
		// asserted.
		nav = append(nav, navLink{ID: "mutation", Label: "Mutation", Count: v.Mutation.Lived,
			Warn: v.Mutation.Lived > 0 || v.Mutation.Infra > 0})
	}
	if len(v.Groups) > 0 {
		nav = append(nav, navLink{ID: "drill", Label: "Drill in", Count: len(v.Groups)})
	}
	if len(v.Renders) > 0 {
		nav = append(nav, navLink{ID: "changed", Label: "What changed", Count: len(v.Renders)})
	}
	// The count is every finding in the section, folded ones included: the
	// fold takes a low-confidence finding out of the list, not out of the
	// tally, so the sidebar still says how much is there.
	nav = append(nav, navLink{ID: "observed", Label: "What Redline observed", Count: len(v.Observed) + len(v.LowConfidence)})
	nav = append(nav,
		navLink{ID: "unknowns", Label: "Undetermined", Count: len(v.Unknowns) + len(v.Failed), Warn: len(v.Unknowns)+len(v.Failed) > 0},
		navLink{ID: "confirms", Label: "Checked and held", Count: len(v.Confirms)},
	)
	return nav
}

// bannerText is the same honesty check the markdown report makes. An empty
// report is ambiguous by nature and the reader will take the flattering
// reading unless told otherwise.
func bannerText(rep *findings.Report) string {
	switch {
	case rep.Coverage.ChangedFiles == 0:
		return "Nothing to review — the target matches the base revision."
	case rep.Coverage.ExaminedFiles == 0:
		if len(rep.Findings) > 0 {
			return "Redline's panes examined none of this change — no pane it currently ships covers these files. The findings below come from the agent's review."
		}
		return "Redline examined none of this change. No pane it currently ships covers these files, so an empty findings list says nothing about whether the change is correct."
	case len(rep.FailedSubstrates()) > 0:
		return fmt.Sprintf("%d pane(s) applied to this change and did not run. That part of the change is unreviewed.", len(rep.FailedSubstrates()))
	}
	return ""
}

// highlightDiff marks up a unified diff. Deliberately hand-rolled: a syntax
// highlighting library would be a dependency and a CDN fetch, and the page
// must work with no network.
func highlightDiff(diff string) string { return highlightDiffFor("", diff, nil) }

// hunkHeader captures the old and new starting line numbers of a unified-diff
// hunk. Counts are optional (`@@ -1 +1 @@`).
var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// highlightDiffFor marks up a diff and, when a path is given, makes each line
// addressable so a reviewer can attach a comment to it — prequel's core
// affordance, and the thing that turns reading a diff into reviewing one.
//
// data-line is the source line in the file, parsed from hunk headers: new-file
// line for added and context lines, old-file line for deletions. A comment
// copied for the agent therefore names path.go:48, not "row 17 of the dump".
func highlightDiffFor(path, diff string, cov map[int]bool) string {
	if diff == "" {
		return ""
	}
	var b strings.Builder
	var cur diffCursor
	for _, line := range strings.Split(diff, "\n") {
		class, side, src := cur.classify(line)
		escaped := template.HTMLEscapeString(line)
		if path == "" || class == "meta" || class == "hunk" || src == 0 {
			fmt.Fprintf(&b, `<span class="%s">%s</span>`, class, escaped)
			continue
		}
		fmt.Fprintf(&b, `<span class="%s" data-file="%s" data-line="%d" data-side="%s"%s>%s</span>`,
			class, template.HTMLEscapeString(path), src, side, coverAttr(side, src, cov), escaped)
	}
	return b.String()
}

// coverAttr is the coverage stripe for one diff line. Only head (new) lines
// carry it — coverage is a fact about the head file: hit ran, miss did not, and
// a line the profile does not mention is not coverable and gets nothing.
func coverAttr(side string, src int, cov map[int]bool) string {
	if side != "new" || cov == nil {
		return ""
	}
	covered, ok := cov[src]
	if !ok {
		return ""
	}
	if covered {
		return ` data-cov="hit"`
	}
	return ` data-cov="miss"`
}

// expandForFindings appends a small context window around each finding line the
// file's diff does not already show, read from the head tree. A lint finding
// often sits on an unchanged line the default 3-line diff context never reaches;
// without this it cannot be located or highlighted in the drawer. Best effort:
// a file it cannot read, or a finding already in the diff, adds nothing.
func expandForFindings(headDir, path, diff string, lines []int) string {
	if len(lines) == 0 {
		return diff
	}
	covered := coveredNewLines(diff)
	var want []int
	for _, ln := range lines {
		if ln > 0 && !covered[ln] {
			want = append(want, ln)
		}
	}
	if len(want) == 0 {
		return diff
	}
	src := headFileLines(headDir, path)
	if len(src) == 0 {
		return diff
	}
	const ctx = 3
	var b strings.Builder
	b.WriteString(diff)
	if diff != "" && !strings.HasSuffix(diff, "\n") {
		b.WriteByte('\n')
	}
	for _, w := range mergeWindows(want, len(src), ctx) {
		n := w.to - w.from + 1
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", w.from, n, w.from, n)
		for i := w.from; i <= w.to; i++ {
			b.WriteString(" ")
			b.WriteString(src[i-1])
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// coveredNewLines is the set of new-file line numbers the diff already shows.
func coveredNewLines(diff string) map[int]bool {
	covered := map[int]bool{}
	var cur diffCursor
	for _, line := range strings.Split(diff, "\n") {
		_, side, src := cur.classify(line)
		if src > 0 && side == "new" {
			covered[src] = true
		}
	}
	return covered
}

type lineWindow struct{ from, to int }

// mergeWindows turns finding lines into context windows, merging ones that touch
// so a cluster of findings reads as one block rather than repeating lines.
func mergeWindows(lines []int, max, ctx int) []lineWindow {
	sort.Ints(lines)
	var ws []lineWindow
	for _, ln := range lines {
		if ln < 1 || ln > max {
			continue
		}
		from, to := ln-ctx, ln+ctx
		if from < 1 {
			from = 1
		}
		if to > max {
			to = max
		}
		if n := len(ws); n > 0 && from <= ws[n-1].to+1 {
			if to > ws[n-1].to {
				ws[n-1].to = to
			}
			continue
		}
		ws = append(ws, lineWindow{from, to})
	}
	return ws
}

// headFileLines reads path from the head tree as lines. Empty when unreadable.
func headFileLines(headDir, path string) []string {
	if path == "" {
		return nil
	}
	full := path
	if headDir != "" {
		full = filepath.Join(headDir, path)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// diffCursor walks a unified diff, tracking old and new file line numbers.
// inHunk separates the header region of a file — where "--- a/x" is a header
// — from its body, where a line starting "---" is deleted content.
type diffCursor struct {
	old, new int
	inHunk   bool
}

func (c *diffCursor) classify(line string) (class, side string, src int) {
	// Header detection must not swallow content. Inside a hunk, "---port N"
	// is a deleted line whose text begins with "--", not a file header, and
	// treating it as one would leave the old-side cursor behind and shift
	// every following line number in that hunk. File headers only appear
	// before the first @@ of a file, and git always writes them with a
	// trailing space; both conditions are required here.
	if !c.inHunk {
		switch {
		case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "new file"), strings.HasPrefix(line, "deleted file"),
			strings.HasPrefix(line, "old file"), strings.HasPrefix(line, "similarity "),
			strings.HasPrefix(line, "rename "):
			return "meta", "", 0
		}
	} else if strings.HasPrefix(line, "diff ") {
		// The next file in a multi-file diff.
		c.inHunk = false
		return "meta", "", 0
	}
	// "\ No newline at end of file" belongs to neither side.
	if strings.HasPrefix(line, `\`) {
		return "meta", "", 0
	}
	if m := hunkHeader.FindStringSubmatch(line); m != nil {
		c.old, _ = strconv.Atoi(m[1])
		c.new, _ = strconv.Atoi(m[2])
		c.inHunk = true
		return "hunk", "", 0
	}
	switch {
	case strings.HasPrefix(line, "+"):
		n := c.new
		c.new++
		return "add", "new", n
	case strings.HasPrefix(line, "-"):
		n := c.old
		c.old++
		return "del", "old", n
	default:
		n := c.new
		c.old++
		c.new++
		return "ctx", "new", n
	}
}
