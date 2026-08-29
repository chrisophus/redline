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
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
	"github.com/ccason/redline/internal/pane"
)

//go:embed assets/report.html.tmpl
var assets embed.FS

// HTMLInput is everything the page renders.
type HTMLInput struct {
	Report   *findings.Report
	Packet   *packet.Packet
	Review   *packet.Review
	Renders  []pane.Render
	Evidence map[string]pane.Artifact
	// Screenshots are route captures. Until the UI pane ships they come from
	// the reviewing agent's walk (ingest `screenshots`). The section still
	// renders empty as "did not run" when none were supplied.
	Screenshots []Screenshot
}

// Screenshot is one route capture. Before is optional: an agent walk of the
// current tree often has only After. Data URIs must be template.URL or
// html/template will replace them with #ZgotmplZ.
type Screenshot struct {
	Route   string
	Caption string
	Before  template.URL
	After   template.URL
	// BeforeFile and File are the copies under evidence/ui, relative to the
	// evidence directory. Set even when the image is inlined, so the report
	// still names where the capture lives.
	BeforeFile string
	File       string
}

// view is the flattened shape the template consumes.
type view struct {
	Title    string
	Subtitle string
	Summary  string
	URL      string
	Author   string
	Coverage findings.Coverage
	Banner   string
	Counts   map[string]int

	// Orientation: why this change exists. Every field is optional because the
	// screen opens pre-push, where there is no pull request and often no
	// ticket, as well as on an open one.
	Ticket *packet.IntentTicket
	PR     *prView
	Fit    *packet.IntentFit

	// Surfaces always has three entries, in the order a reviewer checks them.
	// A surface the agent said nothing about renders as unreported rather than
	// as unchanged.
	Surfaces []surfaceView

	API    []packet.Highlight
	Schema []packet.Highlight
	Files  []fileWalkRow

	// Judged and Observed are the same findings split by who is accountable
	// for them. A skim of what the reviewers found is a different act from
	// reading what Redline can prove.
	Judged   []reviewerGroup
	Observed []findingView

	Areas       []areaView
	TestFiles   int
	Confirms    []findings.Confirmation
	Unknowns    []findings.Unknown
	Dark        []findings.SubstrateStatus
	Skipped     []findings.SubstrateStatus
	Threads     []packet.Thread
	Screenshots []Screenshot
	AgentWalk   bool
	// UITouched is whether the change moves the interface at all. It decides
	// how loud the absence of captures should be: no captures on a change that
	// touches no UI is unremarkable, and on one that does it is a gap.
	UITouched bool
	Commits   int
	HasReview bool
	// Identity keys browser-local comments to this review, not to the
	// report.html path — every run overwrites the same file.
	Identity string
}

// prView is the pull request as the screen shows it, from whichever source
// knew about it.
type prView struct {
	Number int
	Title  string
	URL    string
}

// surfaceView is one contract-surface tile. Stated separates "the agent looked
// and nothing moved" from "nobody said" — the second must never read as a pass.
type surfaceView struct {
	Label  string
	Line   string
	Moved  bool
	Stated bool
}

// reviewerGroup is one reviewer's findings, kept under its own name.
//
// Two reviewers are not reconciled into one list. Redline cannot tell whether
// two differently worded sentences describe the same defect without guessing,
// and a wrong guess deletes a finding silently. Grouping puts both accounts in
// front of the reviewer, which is what skimming for a flavour of what each one
// found actually needs.
type reviewerGroup struct {
	Reviewer string
	Findings []findingView
}

type findingView struct {
	findings.Finding
	Evidence template.HTML
	IsLLM    bool
}

type areaView struct {
	Key   string
	Label string
	Files []fileView
	Count int
}

type fileView struct {
	Path    string
	Status  string
	Added   int
	Removed int
	Summary string
	Diff    template.HTML
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
		Title:       "Redline",
		Coverage:    rep.Coverage,
		Confirms:    rep.Confirmations,
		Unknowns:    rep.Unknowns,
		Dark:        rep.DarkSubstrates(),
		Screenshots: in.Screenshots,
		AgentWalk:   len(in.Screenshots) > 0,
		Counts:      map[string]int{},
	}
	for _, s := range rep.Substrates {
		if s.State == findings.SubstrateSkipped {
			v.Skipped = append(v.Skipped, s)
		}
	}
	head := "worktree"
	if p := in.Packet; p != nil {
		v.Commits = len(p.Commits)
		v.Threads = p.Threads
		v.UITouched = p.UITouched
		if p.Target != nil {
			v.Subtitle = p.Target.Describe()
			if p.Target.Head != "" {
				head = p.Target.Head
			}
			if p.Target.PR != nil {
				v.URL = p.Target.PR.URL
				v.Author = p.Target.PR.Author
			}
		}
	}
	v.Identity = reviewIdentity(rep.BaseSHA, head, in.Packet)
	if v.Subtitle == "" {
		v.Subtitle = fmt.Sprintf("base %s", short(rep.BaseSHA))
	}
	var notes []packet.FileNote
	var surfaces *packet.Surfaces
	if r := in.Review; r != nil {
		v.HasReview = true
		v.Summary = r.Summary
		v.API = r.APIChanges
		v.Schema = r.SchemaChanges
		notes = r.Files
		surfaces = r.Surfaces
		if r.Intent != nil {
			v.Ticket = r.Intent.Ticket
			v.Fit = r.Intent.Fit
			if p := r.Intent.PR; p != nil {
				v.PR = &prView{Number: p.Number, Title: p.Title, URL: p.URL}
			}
		}
	}
	// When the target is a pull request, Redline fetched it and that is the
	// pull request under review. The agent's copy is hearsay about the same
	// thing, and rendering it instead would contradict the subtitle two lines
	// above. It is used only when Redline was not pointed at a pull request at
	// all — the pre-push case where the agent knows one exists.
	if in.Packet != nil && in.Packet.Target != nil && in.Packet.Target.PR != nil {
		pr := in.Packet.Target.PR
		v.PR = &prView{Number: pr.Number, Title: pr.Title, URL: pr.URL}
	}
	v.Surfaces = surfaceViews(surfaces)
	if v.Summary == "" {
		v.Summary = "No agent summary. Redline emits evidence; the plain-language account of the change comes from the agent driving it — run `redline review` and pipe the result to `redline ingest`."
	}
	v.Banner = bannerText(rep)

	var judged []findingView
	for _, f := range rep.Findings {
		v.Counts[string(f.Severity)]++
		fv := findingView{Finding: f, IsLLM: f.Source == findings.SourceLLM}
		for _, id := range f.Evidence {
			if a, ok := in.Evidence[id]; ok && a.Content != "" && len(a.Content) < maxInlineEvidence {
				fv.Evidence = template.HTML(highlightDiff(a.Content))
			}
		}
		if fv.IsLLM {
			judged = append(judged, fv)
		} else {
			v.Observed = append(v.Observed, fv)
		}
	}
	v.Judged = groupByReviewer(judged)

	if in.Packet != nil {
		v.Files = fileWalk(in.Packet.Files, notes, rep.Findings)
		byPath := map[string]string{}
		for _, row := range v.Files {
			byPath[row.Path] = row.Summary
		}
		byArea := map[string][]fileView{}
		for _, f := range in.Packet.Files {
			fv := fileView{Path: f.Path, Status: f.Status, Added: f.Added, Removed: f.Removed,
				Summary: byPath[f.Path],
				Diff:    template.HTML(highlightDiffFor(f.Path, f.Diff))}
			for _, a := range f.Areas {
				byArea[a] = append(byArea[a], fv)
			}
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
	return v
}

// groupByReviewer splits judged findings by who reported them, preserving the
// order each reviewer's findings arrived in. The driving agent's own judgments
// carry no reviewer name and are grouped last, after the tools that were run
// deliberately.
func groupByReviewer(judged []findingView) []reviewerGroup {
	var order []string
	byName := map[string][]findingView{}
	for _, f := range judged {
		if _, seen := byName[f.Reviewer]; !seen {
			order = append(order, f.Reviewer)
		}
		byName[f.Reviewer] = append(byName[f.Reviewer], f)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if (order[i] == "") != (order[j] == "") {
			return order[j] == ""
		}
		return order[i] < order[j]
	})
	out := make([]reviewerGroup, 0, len(order))
	for _, name := range order {
		label := name
		if label == "" {
			label = "the driving agent"
		}
		out = append(out, reviewerGroup{Reviewer: label, Findings: byName[name]})
	}
	return out
}

// surfaceViews returns the three surfaces in the order a reviewer checks them,
// always all three. A missing surface is rendered as unreported: the whole
// point of the strip is that a surface nobody spoke about looks different from
// one that was checked and had not moved.
func surfaceViews(s *packet.Surfaces) []surfaceView {
	labels := []struct {
		label string
		get   func(*packet.Surfaces) *packet.Surface
	}{
		{"Interface", func(x *packet.Surfaces) *packet.Surface { return x.Interface }},
		{"API", func(x *packet.Surfaces) *packet.Surface { return x.API }},
		{"Schema", func(x *packet.Surfaces) *packet.Surface { return x.Schema }},
	}
	out := make([]surfaceView, 0, len(labels))
	for _, l := range labels {
		sv := surfaceView{Label: l.label}
		if s != nil {
			if got := l.get(s); got != nil && (got.Line != "" || got.Moved) {
				sv.Line = got.Line
				sv.Moved = got.Moved
				sv.Stated = true
			}
		}
		if !sv.Stated {
			sv.Line = "Not reported. Nobody said whether this surface moved."
		}
		out = append(out, sv)
	}
	return out
}

// bannerText is the same honesty check the markdown report makes. An empty
// report is ambiguous by nature and the reader will take the flattering
// reading unless told otherwise.
func bannerText(rep *findings.Report) string {
	switch {
	case rep.Coverage.ChangedFiles == 0:
		return "Nothing to review — the target matches the base revision."
	case rep.Coverage.ExaminedFiles == 0:
		// A reviewer that ran changes what the empty pane coverage means, and
		// the old wording — "an empty findings list says nothing" — is a plain
		// contradiction when findings from that reviewer are on the page. Say
		// what did look, and what that costs: a reviewer's reading is not
		// reproducible, and it confirms nothing it did not examine.
		if who := reviewersRan(rep); who != "" {
			return fmt.Sprintf("No pane Redline ships covers these files, so nothing below is reproducible evidence — it is %s's reading of the change. Findings are worth what that reviewer is worth; silence is not a pass.", who)
		}
		return "Redline examined none of this change. No pane it currently ships covers these files, so an empty findings list says nothing about whether the change is correct."
	case len(rep.DarkSubstrates()) > 0:
		return fmt.Sprintf("%d pane(s) applied to this change and did not run. That part of the change is unreviewed.", len(rep.DarkSubstrates()))
	}
	return ""
}

// reviewersRan names the external reviewers that produced findings this run,
// for the banner. Panes and reviewers are both substrates; only the reviewers
// carry the "reviewer:" prefix that withReviewer stamps on them.
func reviewersRan(rep *findings.Report) string {
	var names []string
	for _, s := range rep.Substrates {
		if s.State == findings.SubstrateRan && strings.HasPrefix(s.Name, "reviewer:") {
			names = append(names, strings.TrimPrefix(s.Name, "reviewer:"))
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
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
			fmt.Fprintf(&b, `<span class="%s">%s</span>`+"\n", class, escaped)
			continue
		}
		fmt.Fprintf(&b, `<span class="%s" data-file="%s" data-line="%d" data-side="%s">%s</span>`+"\n",
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
// of the packet files so two dirty trees against the same base do not share
// comments.
func reviewIdentity(baseSHA, head string, p *packet.Packet) string {
	id := short(baseSHA) + ":" + short(head)
	if head != "worktree" || p == nil {
		return id
	}
	h := sha256.New()
	for _, f := range p.Files {
		_, _ = h.Write([]byte(f.Path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(f.Diff))
		_, _ = h.Write([]byte{0})
	}
	return id + ":" + hex.EncodeToString(h.Sum(nil)[:8])
}
