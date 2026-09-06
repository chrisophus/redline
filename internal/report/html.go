// HTML rendering. One self-contained file, no server, no network: the design
// cut the web app, and this is what replaces it. If it turns out a static file
// cannot shift a reviewer into review mode, an app can be built later over the
// same JSON — but that is a decision to make from evidence, not in advance.
package report

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ccason/redline/internal/change"
	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/pane"
)

//go:embed assets/report.html.tmpl
var assets embed.FS

// HTMLInput is everything the page renders.
type HTMLInput struct {
	Report   *findings.Report
	Change   *change.Set
	Renders  []pane.Render
	Evidence map[string]pane.Artifact
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
	DiffFiles   []fileView
	Composition []change.LinesRow
	Observed    []findingView

	Areas     []areaView
	TestFiles int
	TestPaths []string
	Confirms  []findings.Confirmation
	Unknowns  []findings.Unknown
	Dark      []findings.SubstrateStatus
	Skipped   []findings.SubstrateStatus
	// UITouched is whether the change moves the interface at all. It decides
	// how loud the absence of captures should be: no captures on a change that
	// touches no UI is unremarkable, and on one that does it is a gap.
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

type findingView struct {
	findings.Finding
	Evidence template.HTML
	// Snippet is the offending source lines, shown behind a disclosure so a
	// reviewer can see the code a finding points at without leaving the page.
	Snippet template.HTML
}

type areaView struct {
	Key   string
	Label string
	Files []fileView
	Count int
}

type fileView struct {
	Path     string
	Status   string
	Added    int
	Removed  int
	Findings int
	Severity string
	Diff     template.HTML
}

// areaLabels names the drill-in sections, in the order they are shown.
//
// Tests are deliberately absent. A reviewer does not read test bodies; they
// read a coverage number and expect the tests to have been run. Rendering the
// diff of every table-driven case buries the change that needed testing. The
// files still appear in the walkthrough and are counted, so the reviewer knows
// tests moved — they just do not have to scroll past them.
var areaLabels = []struct{ Key, Label string }{
	{"sql", "Schema & migrations"},
	{"api", "API contract"},
	{"ui", "Interface"},
	{"code", "Code"},
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
		Dark:     rep.DarkSubstrates(),
		Counts:   map[string]int{},
	}
	for _, s := range rep.Substrates {
		if s.State == findings.SubstrateSkipped {
			v.Skipped = append(v.Skipped, s)
		}
	}
	head := "worktree"
	if ch := in.Change; ch != nil {
		v.Commits = len(ch.Commits)
		v.UITouched = ch.UITouched
		if ch.Target != nil {
			v.Subtitle = ch.Target.Describe()
			if ch.Target.Head != "" {
				head = ch.Target.Head
			}
			if ch.Target.PR != nil {
				v.URL = ch.Target.PR.URL
				v.Author = ch.Target.PR.Author
			}
		}
	}
	v.Identity = reviewIdentity(rep.BaseSHA, head, in.Change)
	if v.Subtitle == "" {
		v.Subtitle = fmt.Sprintf("base %s", short(rep.BaseSHA))
	}
	v.Banner = bannerText(rep)

	headDir := ""
	if in.Change != nil && in.Change.Target != nil {
		headDir = in.Change.Target.Dir
	}
	for _, f := range rep.Findings {
		v.Counts[string(f.Severity)]++
		fv := findingView{Finding: f}
		for _, id := range f.Evidence {
			if a, ok := in.Evidence[id]; ok && a.Content != "" && len(a.Content) < maxInlineEvidence {
				fv.Evidence = template.HTML(highlightDiff(a.Content))
			}
		}
		fv.Snippet = snippet(headDir, f.File, f.Line)
		v.Observed = append(v.Observed, fv)
	}

	if in.Change != nil {
		v.Composition = change.Composition(in.Change.Files)
		count, worst := fileFindingCounts(rep.Findings)
		rendered := map[string]bool{}
		for _, al := range areaLabels {
			rendered[al.Key] = true
		}
		byArea := map[string][]fileView{}
		seen := map[string]bool{}
		for _, f := range in.Change.Files {
			fv := fileView{Path: f.Path, Status: f.Status, Added: f.Added, Removed: f.Removed,
				Findings: count[f.Path], Severity: string(worst[f.Path]),
				Diff: template.HTML(highlightDiffFor(f.Path, f.Diff))}
			inArea := false
			for _, a := range f.Areas {
				byArea[a] = append(byArea[a], fv)
				if rendered[a] {
					inArea = true
				}
			}
			if inArea && !seen[f.Path] {
				seen[f.Path] = true
				v.DiffFiles = append(v.DiffFiles, fv)
			}
		}
		for _, tf := range byArea["tests"] {
			v.TestPaths = append(v.TestPaths, tf.Path)
		}
		v.TestFiles = len(byArea["tests"])
		for _, al := range areaLabels {
			files := byArea[al.Key]
			if len(files) == 0 {
				continue
			}
			v.Areas = append(v.Areas, areaView{Key: al.Key, Label: al.Label, Files: files, Count: len(files)})
		}
	}
	v.Nav = navFor(v)
	return v
}

// navFor lists the sections this page will render, in page order. It must stay
// in step with the template: TestNavMatchesTheSectionsOnThePage fails if a
// section gains or loses a heading without its entry moving too.
func navFor(v view) []navLink {
	// A UI that moved with nothing captured is a gap; a change with no UI files
	// is not.
	uiGap := v.UITouched
	coverageGap := v.Coverage.Diff == nil || v.Coverage.Diff.Stale

	nav := []navLink{
		{ID: "interface", Label: "What it looks like", Warn: uiGap},
		{ID: "coverage", Label: "Coverage", Warn: coverageGap},
	}
	if len(v.Areas) > 0 {
		nav = append(nav, navLink{ID: "drill", Label: "Drill in", Count: len(v.Areas)})
	}
	if len(v.Composition) > 0 {
		nav = append(nav, navLink{ID: "composition", Label: "Lines by language and type", Count: len(v.Composition)})
	}
	if len(v.Renders) > 0 {
		nav = append(nav, navLink{ID: "changed", Label: "What changed", Count: len(v.Renders)})
	}
	nav = append(nav, navLink{ID: "observed", Label: "What Redline observed", Count: len(v.Observed)})
	nav = append(nav,
		navLink{ID: "unknowns", Label: "Undetermined", Count: len(v.Unknowns) + len(v.Dark), Warn: len(v.Unknowns)+len(v.Dark) > 0},
		navLink{ID: "confirms", Label: "Checked and held", Count: len(v.Confirms)},
	)
	return nav
}

// snippet returns the offending lines around a finding, read from the head
// tree, as a numbered code block with the finding's own line marked. Best
// effort: a file it cannot read, or a line past the file's end (a stale
// tool result), yields no snippet rather than an error — the finding still
// renders, just without the code behind it.
func snippet(headDir, file string, line int) template.HTML {
	if file == "" || line <= 0 {
		return ""
	}
	path := file
	if headDir != "" {
		path = filepath.Join(headDir, file)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line > len(lines) {
		return ""
	}
	const ctx = 3
	from, to := line-ctx, line+ctx
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	var b strings.Builder
	for n := from; n <= to; n++ {
		cls := "src-line"
		if n == line {
			cls = "src-line hit"
		}
		fmt.Fprintf(&b, `<span class="%s"><span class="ln">%d</span>%s</span>`,
			cls, n, template.HTMLEscapeString(lines[n-1]))
	}
	return template.HTML(b.String())
}

// bannerText is the same honesty check the markdown report makes. An empty
// report is ambiguous by nature and the reader will take the flattering
// reading unless told otherwise.
func bannerText(rep *findings.Report) string {
	switch {
	case rep.Coverage.ChangedFiles == 0:
		return "Nothing to review — the target matches the base revision."
	case rep.Coverage.ExaminedFiles == 0:
		return "Redline examined none of this change. No pane it currently ships covers these files, so an empty findings list says nothing about whether the change is correct."
	case len(rep.DarkSubstrates()) > 0:
		return fmt.Sprintf("%d pane(s) applied to this change and did not run. That part of the change is unreviewed.", len(rep.DarkSubstrates()))
	}
	return ""
}

// highlightDiff marks up a unified diff. Deliberately hand-rolled: a syntax
// highlighting library would be a dependency and a CDN fetch, and the page
// must work with no network.
func highlightDiff(diff string) string { return highlightDiffFor("", diff) }

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
func highlightDiffFor(path, diff string) string {
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
		fmt.Fprintf(&b, `<span class="%s" data-file="%s" data-line="%d" data-side="%s">%s</span>`,
			class, template.HTMLEscapeString(path), src, side, escaped)
	}
	return b.String()
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

// reviewIdentity keys browser comments to this change. Branch and PR reviews
// are identified by base+head SHA. Working-tree reviews include a fingerprint
// of the changed files so two dirty trees against the same base do not share
// comments.
func reviewIdentity(baseSHA, head string, ch *change.Set) string {
	id := short(baseSHA) + ":" + short(head)
	if head != "worktree" || ch == nil {
		return id
	}
	h := sha256.New()
	for _, f := range ch.Files {
		_, _ = h.Write([]byte(f.Path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(f.Diff))
		_, _ = h.Write([]byte{0})
	}
	return id + ":" + hex.EncodeToString(h.Sum(nil)[:8])
}
