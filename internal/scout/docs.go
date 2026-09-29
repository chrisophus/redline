package scout

import (
	"fmt"
	"os"
	"path"
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
// repository root, and the ones closer to the files being changed.
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
//
// filter, when set, keeps only the paths containing it. It is what makes a
// repository with more documents than the bound navigable: the first listing
// says which directories hold the rest, and a second call asks for one of
// them. Without it the bound is a dead end, because the reason to list
// documents at all is not yet knowing which one to ask for.
func listDocsIn(root, filter string) []docFile {
	all := listDocs(root, 0)
	if filter = strings.TrimSpace(filter); filter == "" {
		return all
	}
	kept := all[:0]
	for _, d := range all {
		if strings.Contains(d.Path, filter) {
			kept = append(kept, d)
		}
	}
	return kept
}

// renderDocs writes the listing, and when it is cut says where the rest are.
//
// Directories with counts rather than "there are more". A model told only that
// it did not see everything has no next move but to page or to guess a word;
// told that 54 of the missing 87 are under docs/adr, it asks for docs/adr.
func renderDocs(all []docFile, max int, filter string) string {
	if len(all) == 0 {
		if filter != "" {
			return "no documents under " + filter
		}
		return "no documents in this repository"
	}
	shown := all
	if max > 0 && len(shown) > max {
		shown = shown[:max]
	}
	var b strings.Builder
	for _, d := range shown {
		fmt.Fprintf(&b, "%s (%d lines)", d.Path, d.Lines)
		if d.Heading != "" {
			fmt.Fprintf(&b, ": %s", d.Heading)
		}
		b.WriteString("\n")
	}
	if len(shown) == len(all) {
		return b.String()
	}
	fmt.Fprintf(&b, "\n(%d of %d shown, sorted by path.", len(shown), len(all))
	if dirs := dirCounts(all[len(shown):]); dirs != "" {
		fmt.Fprintf(&b, " The rest are under %s.", dirs)
	}
	b.WriteString(" Pass path to list one of those.)\n")
	return b.String()
}

// dirCounts names the directories holding a set of documents, biggest first,
// as "docs/adr (54), internal/notes (33)". Bounded, because a remainder spread
// across forty directories is a list nobody reads; what is left over is
// counted into "other".
func dirCounts(docs []docFile) string {
	const maxDirs = 6
	counts := map[string]int{}
	for _, d := range docs {
		dir := path.Dir(d.Path)
		if dir == "." {
			dir = "the repository root"
		}
		counts[dir]++
	}
	dirs := make([]string, 0, len(counts))
	for dir := range counts {
		dirs = append(dirs, dir)
	}
	// Count descending, then path, so the same remainder reads the same twice.
	sort.Slice(dirs, func(i, j int) bool {
		if counts[dirs[i]] != counts[dirs[j]] {
			return counts[dirs[i]] > counts[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})
	var parts []string
	other := 0
	for i, dir := range dirs {
		if i >= maxDirs {
			other += counts[dir]
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", dir, counts[dir]))
	}
	if other > 0 {
		parts = append(parts, fmt.Sprintf("%d elsewhere", other))
	}
	return strings.Join(parts, ", ")
}

// listDocs walks the tree. max of 0 means every document.
func listDocs(root string, max int) []docFile {
	var out []docFile
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
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
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// describe reads a file's size and its first heading. The heading is the
// file's own words, which is what makes a listing worth reading: "What is
// left" says more about docs/plans/roadmap.md than its path does.
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
// through or read again before one can be given. Reading it again is the
// round trip inlining was meant to save, and counting it by eye is how a rule
// gets filed three lines off.
func guidelineBrief(root string, found []docFile, inlineLines, totalLines int) string {
	if len(found) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nThis repository's own rules. Record the parts that bear on this change ")
	b.WriteString("under the guideline role; the reviewer does not see them unless you do.\n")

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
