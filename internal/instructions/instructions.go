// Package instructions discovers the repository's own review guidance and
// hands it to the agent doing the LLM pass.
//
// A review that ignores the house rules is worse than no review: it spends the
// reviewer's attention arguing about things the team already decided. Copilot
// reads .github/copilot-instructions.md and .github/instructions/*.instructions.md;
// the same files are the right input here, alongside the agent-native
// equivalents a repo may carry instead.
package instructions

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File is one discovered instruction file.
type File struct {
	Path string `json:"path"`
	// ApplyTo is the glob from an .instructions.md frontmatter `applyTo` key.
	// Empty means the file applies to the whole repository.
	ApplyTo string `json:"applyTo,omitempty"`
	Content string `json:"content"`
	// Format names the convention the file came from, so the agent can weigh
	// a Copilot instruction differently from a Cursor rule if it needs to.
	Format string `json:"format"`
}

// Applies reports whether this file governs the given path. A file with no
// applyTo glob governs everything.
func (f File) Applies(path string) bool {
	if f.ApplyTo == "" {
		return true
	}
	for _, pattern := range strings.Split(f.ApplyTo, ",") {
		if matchGlob(strings.TrimSpace(pattern), path) {
			return true
		}
	}
	return false
}

// sources are the conventions searched, in precedence order. Copilot's two
// locations come first because they are what the user asked to honour; the
// rest are the same instruction in a different dialect and are included so a
// repo that has picked one is not treated as having none.
var sources = []struct {
	glob   string
	format string
}{
	{".github/copilot-instructions.md", "copilot"},
	{".github/instructions/*.instructions.md", "copilot"},
	{"AGENTS.md", "agents"},
	{"CLAUDE.md", "claude"},
	{".cursor/rules/*.mdc", "cursor"},
	{".cursorrules", "cursor"},
	{"CONTRIBUTING.md", "contributing"},
}

// Discover reads every instruction file present in the repository root.
func Discover(root string) []File {
	var out []File
	for _, src := range sources {
		matches, err := filepath.Glob(filepath.Join(root, src.glob))
		if err != nil {
			continue
		}
		sort.Strings(matches)
		for _, m := range matches {
			body, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(root, m)
			if err != nil {
				rel = m
			}
			applyTo, content := splitFrontmatter(string(body))
			if strings.TrimSpace(content) == "" {
				continue
			}
			out = append(out, File{Path: rel, ApplyTo: applyTo, Content: content, Format: src.format})
		}
	}
	return out
}

// For returns the instruction files governing at least one of the given paths.
func For(files []File, paths []string) []File {
	var out []File
	for _, f := range files {
		if f.ApplyTo == "" {
			out = append(out, f)
			continue
		}
		for _, p := range paths {
			if f.Applies(p) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// splitFrontmatter pulls the applyTo glob out of YAML frontmatter and returns
// the body. Only applyTo is interpreted; everything else is left in place for
// the agent to read.
func splitFrontmatter(body string) (applyTo, content string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(body, "---\n") {
		return "", body
	}
	end := strings.Index(body[4:], "\n---")
	if end < 0 {
		return "", body
	}
	front := body[4 : 4+end]
	// end indexes the newline before the closing "---"; skip past that line.
	rest := body[4+end+1:]
	if i := strings.Index(rest, "\n"); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = ""
	}
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "applyTo" {
			continue
		}
		applyTo = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return applyTo, strings.TrimPrefix(rest, "\n")
}

// matchGlob matches a path against a glob supporting ** for any number of
// path segments, which filepath.Match does not.
func matchGlob(pattern, path string) bool {
	if pattern == "" || pattern == "**" || pattern == "**/*" {
		return true
	}
	if !strings.Contains(pattern, "**") {
		ok, err := filepath.Match(pattern, path)
		if err == nil && ok {
			return true
		}
		// A bare directory prefix governs everything under it.
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "/")+"/")
	}
	head, tail, _ := strings.Cut(pattern, "**")
	head = strings.TrimSuffix(head, "/")
	tail = strings.TrimPrefix(tail, "/")
	if head != "" && !strings.HasPrefix(path, head+"/") && path != head {
		return false
	}
	if tail == "" {
		return true
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(path, head), "/")
	for {
		if ok, _ := filepath.Match(tail, rest); ok {
			return true
		}
		_, next, found := strings.Cut(rest, "/")
		if !found {
			return false
		}
		rest = next
	}
}
