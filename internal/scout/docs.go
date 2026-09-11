package scout

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// guidelineNames are the files a repository puts its own rules in. A review
// that contradicts the house style is noise twice over: the finding is wrong,
// and it teaches the reader that the tool has not read what they wrote down.
//
// The list is names rather than a search because these files are a
// convention: a repository that keeps its rules somewhere else will have that
// place turn up under docs instead, which is what listDocs is for.
var guidelineNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"CONTRIBUTING.md",
	"CONVENTIONS.md",
	"STYLE.md",
	"STYLEGUIDE.md",
	".cursorrules",
}

// rootOnlyGuidelines are looked for at the repository root and nowhere else.
var rootOnlyGuidelines = []string{
	".github/CONTRIBUTING.md",
	"docs/CONTRIBUTING.md",
}

// docFile is one guideline or document, with enough about it to decide
// whether to read it.
type docFile struct {
	Path    string
	Lines   int
	Heading string
}

// guidelines finds the rules that govern this change: the ones at the
// repository root, and the ones sitting closer to the files being changed.
//
// The nearest-first walk is the convention agents already follow, and it is
// the one that matters for a large repository: a package with its own
// AGENTS.md has said something specific about that package, and a reviewer
// that only ever reads the root file will not know it.
func guidelines(root string, changed []string) []docFile {
	seen := map[string]bool{}
	var out []docFile

	add := func(rel string) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		if f, ok := describe(root, rel); ok {
			out = append(out, f)
		}
	}
	for _, name := range guidelineNames {
		add(name)
	}
	for _, rel := range rootOnlyGuidelines {
		add(rel)
	}
	// Every directory between a changed file and the root may carry rules of
	// its own.
	for _, path := range changed {
		dir := filepath.Dir(filepath.FromSlash(normPath(path)))
		for dir != "." && dir != string(filepath.Separator) && dir != "" {
			for _, name := range guidelineNames {
				add(filepath.ToSlash(filepath.Join(dir, name)))
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	sort.Slice(out, func(i, j int) bool {
		// Deepest first: the rules nearest the change are the specific ones,
		// and they are what a reader with one turn to spend should see.
		di, dj := strings.Count(out[i].Path, "/"), strings.Count(out[j].Path, "/")
		if di != dj {
			return di > dj
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// docExtensions are what listDocs will offer. Prose only: the scout is
// looking for a design note or a decision record, and a repository's own
// source is reachable through every other tool it has.
var docExtensions = map[string]bool{
	".md":       true,
	".markdown": true,
	".rst":      true,
	".adoc":     true,
	".txt":      true,
}

// listDocs is every document in the repository, so the scout can find the
// design note or decision record that explains what a change is for. Bounded,
// and sorted so the same repository lists the same way twice.
func listDocs(root string, max int) []docFile {
	var out []docFile
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "graphify-out", ".redline", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !docExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if f, ok := describe(root, filepath.ToSlash(rel)); ok {
			out = append(out, f)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// describe reads a file's size and its first heading. The heading is the
// file's own words, which is what makes a listing worth reading: "Report
// roadmap" says more about docs/plans/report-roadmap.md than its path does.
func describe(root, rel string) (docFile, bool) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	st, err := os.Stat(full)
	if err != nil || st.IsDir() || st.Size() == 0 || st.Size() > 1<<20 {
		return docFile{}, false
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return docFile{}, false
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	return docFile{Path: rel, Lines: len(lines), Heading: firstHeading(lines)}, true
}

func firstHeading(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if h := strings.TrimLeft(trimmed, "#"); h != trimmed {
			return strings.TrimSpace(h)
		}
		// A file with no markdown heading still has a first line worth
		// showing: a .cursorrules opens with its own rule.
		return truncate(trimmed, 80)
	}
	return ""
}

// guidelineBrief renders the repository's rules for the scout's opening turn.
//
// Short files go in whole rather than being listed. The scout would otherwise
// spend its first turn reading them, and a turn costs more than the couple of
// hundred tokens an AGENTS.md takes: this is the one piece of context that is
// relevant to every change, so paying a round trip for it every run is the
// one saving worth taking here.
//
// They are shown with line numbers, in the shape read_lines uses. A record
// needs a line range, and a file shown without numbers has to be counted
// through or read again before one can be given; the second is the round trip
// inlining was meant to save, and the first is how a rule gets filed three
// lines off.
func guidelineBrief(root string, found []docFile, inlineLines, totalLines int) string {
	if len(found) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nThis repository's own rules. They govern what a good review of it says, ")
	b.WriteString("so read them before you decide what matters, and record the parts that bear on this change ")
	b.WriteString("under the guideline role. The reviewer does not see them unless you do.\n")

	budget := totalLines
	for _, f := range found {
		if f.Lines <= inlineLines && budget-f.Lines >= 0 {
			body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
			if err == nil {
				budget -= f.Lines
				fmt.Fprintf(&b, "\n--- %s (%d lines, shown in full with line numbers) ---\n%s\n", f.Path, f.Lines, numbered(string(body)))
				continue
			}
		}
		fmt.Fprintf(&b, "\n--- %s (%d lines, read it if it bears on this change)", f.Path, f.Lines)
		if f.Heading != "" {
			fmt.Fprintf(&b, ": %s", f.Heading)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// numbered renders a file the way read_lines does, one line per row with its
// 1-based number in front, so a range can be recorded straight off it.
func numbered(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, l)
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
