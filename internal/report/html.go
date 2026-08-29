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
	"os/exec"
	"runtime"
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
	// Screenshots are before/after captures from the UI pane, keyed by route.
	// Empty until rung 6 lands; the section renders as "did not run" rather
	// than disappearing, so its absence is visible.
	Screenshots []Screenshot
}

// Screenshot is one before/after route capture.
type Screenshot struct {
	Route  string
	Before string // data URI
	After  string // data URI
}

// view is the flattened shape the template consumes.
type view struct {
	Title       string
	Subtitle    string
	Summary     string
	URL         string
	Author      string
	Coverage    findings.Coverage
	Banner      string
	Counts      map[string]int
	API         []packet.Highlight
	Schema      []packet.Highlight
	Findings    []findingView
	Areas       []areaView
	Confirms    []findings.Confirmation
	Unknowns    []findings.Unknown
	Dark        []findings.SubstrateStatus
	Skipped     []findings.SubstrateStatus
	Threads     []packet.Thread
	Screenshots []Screenshot
	Commits     int
	HasReview   bool
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
	Diff    template.HTML
}

// areaLabels names the drill-in sections, in the order they are shown.
var areaLabels = []struct{ Key, Label string }{
	{"sql", "Schema & migrations"},
	{"api", "API contract"},
	{"ui", "Interface"},
	{"code", "Code"},
	{"tests", "Tests"},
}

// HTML renders the report page.
func HTML(in HTMLInput) (string, error) {
	tmpl, err := template.New("report.html.tmpl").Funcs(template.FuncMap{
		"lower": func(v any) string { return strings.ToLower(fmt.Sprint(v)) },
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
		Counts:      map[string]int{},
	}
	for _, s := range rep.Substrates {
		if s.State == findings.SubstrateSkipped {
			v.Skipped = append(v.Skipped, s)
		}
	}
	if p := in.Packet; p != nil {
		v.Commits = len(p.Commits)
		v.Threads = p.Threads
		if p.Target != nil {
			v.Subtitle = p.Target.Describe()
			if p.Target.PR != nil {
				v.URL = p.Target.PR.URL
				v.Author = p.Target.PR.Author
			}
		}
	}
	if v.Subtitle == "" {
		v.Subtitle = fmt.Sprintf("base %s", short(rep.BaseSHA))
	}
	if r := in.Review; r != nil {
		v.HasReview = true
		v.Summary = r.Summary
		v.API = r.APIChanges
		v.Schema = r.SchemaChanges
	}
	if v.Summary == "" {
		v.Summary = "No agent summary. Redline emits evidence; the plain-language account of the change comes from the agent driving it — run `redline review` and pipe the result to `redline ingest`."
	}
	v.Banner = bannerText(rep)

	for _, f := range rep.Findings {
		v.Counts[string(f.Severity)]++
		fv := findingView{Finding: f, IsLLM: f.Source == findings.SourceLLM}
		for _, id := range f.Evidence {
			if a, ok := in.Evidence[id]; ok && a.Content != "" && len(a.Content) < maxInlineEvidence {
				fv.Evidence = template.HTML(highlightDiff(a.Content))
			}
		}
		v.Findings = append(v.Findings, fv)
	}

	if in.Packet != nil {
		byArea := map[string][]fileView{}
		for _, f := range in.Packet.Files {
			fv := fileView{Path: f.Path, Status: f.Status, Added: f.Added, Removed: f.Removed,
				Diff: template.HTML(highlightDiffFor(f.Path, f.Diff))}
			for _, a := range f.Areas {
				byArea[a] = append(byArea[a], fv)
			}
		}
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

// bannerText is the same honesty check the markdown report makes. An empty
// report is ambiguous by nature and the reader will take the flattering
// reading unless told otherwise.
func bannerText(rep *findings.Report) string {
	switch {
	case rep.Coverage.ChangedFiles == 0:
		return "Nothing to review — the target matches its base revision."
	case rep.Coverage.ExaminedFiles == 0:
		return "Redline examined none of this change. No pane it currently ships covers these files, so an empty findings list says nothing about whether the change is correct."
	case len(rep.DarkSubstrates()) > 0:
		return fmt.Sprintf("%d pane(s) applied to this change and did not run. Their part of the change is unreviewed.", len(rep.DarkSubstrates()))
	}
	return ""
}

// highlightDiff marks up a unified diff. Deliberately hand-rolled: a syntax
// highlighting library would be a dependency and a CDN fetch, and the page
// must work with no network.
func highlightDiff(diff string) string { return highlightDiffFor("", diff) }

// highlightDiffFor marks up a diff and, when a path is given, makes each line
// addressable so a reviewer can attach a comment to it — prequel's core
// affordance, and the thing that turns reading a diff into reviewing one.
func highlightDiffFor(path, diff string) string {
	if diff == "" {
		return ""
	}
	var b strings.Builder
	lineNo := 0
	for _, line := range strings.Split(diff, "\n") {
		lineNo++
		class := "ctx"
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"),
			strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
			class = "meta"
		case strings.HasPrefix(line, "@@"):
			class = "hunk"
		case strings.HasPrefix(line, "+"):
			class = "add"
		case strings.HasPrefix(line, "-"):
			class = "del"
		}
		if path == "" || class == "meta" {
			fmt.Fprintf(&b, `<span class="%s">%s</span>`+"\n", class, template.HTMLEscapeString(line))
			continue
		}
		fmt.Fprintf(&b, `<span class="%s" data-file="%s" data-line="%d">%s</span>`+"\n",
			class, template.HTMLEscapeString(path), lineNo, template.HTMLEscapeString(line))
	}
	return b.String()
}

// Open shows the report in the user's browser. A review nobody opens is a
// review that did not happen.
func Open(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Start()
}
