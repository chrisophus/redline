// Package houserules reads the instruction files a repository writes for the
// agents that work on it, and hands them to the review as context.
//
// A review that contradicts the house rules is wrong twice: the finding is
// wrong, and it is evidence the tool did not read what the team wrote down.
// The scout already looks for these files, but the scout calls a model and is
// off by default, so on most runs nothing read them. These particular files do
// not need a model to find: GitHub's convention fixes their location, which
// makes them free and deterministic to resolve, and `redline run` costing
// nothing is a property worth keeping.
//
// Two shapes are read, both GitHub's:
//
//   - .github/copilot-instructions.md, which governs the whole repository.
//   - .github/instructions/*.md, each carrying an `applyTo` frontmatter glob
//     that says which files it governs. A change that touches none of them
//     does not carry the rule, which is the point of the field.
package houserules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/globmatch"
)

// RoleGuideline is a rule the repository wrote down about itself.
//
// It ships as a role Redline does not rank, for the reason the graph adapter's
// `neighbor` does: the contract already keeps an unknown role, ranks it after
// the ones Redline knows, and reports it, so the measurement decides whether
// it belongs in the vocabulary rather than the decision being made by writing
// it down first.
const RoleGuideline = envelope.Role("guideline")

// ProviderName is what the report and the prompt call this context.
const ProviderName = "instructions"

// rootInstructions governs the whole repository and carries no applyTo.
const rootInstructions = ".github/copilot-instructions.md"

// instructionsDir holds the path-scoped instruction files.
const instructionsDir = ".github/instructions"

// priority keeps a rule from being the first thing dropped when the budget
// binds. Guideline is unranked, so it sorts against other unranked roles on
// priority alone, and a graph adjacency would otherwise win on record order. A
// twenty-line rule the reviewer would otherwise contradict is worth more than
// the last adjacent file. The number matches the scout's own floor.
const priority = 80

// maxLines bounds one rule file. Instructions are prose a team wrote for a
// reader, not generated output, so this is generous: truncating a rule
// halfway is worse than not carrying it, because half a rule still reads as
// the whole one. A file over the bound is carried up to it and says so.
const maxLines = 400

// Resolve reads the rules that govern this change. A repository with no
// instruction files returns a nil envelope and no error: nothing to say is a
// legitimate answer, and it is not the same as a failure to look.
func Resolve(root string, changed []string) (*envelope.Envelope, error) {
	files, err := applicable(root, changed)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	env := &envelope.Envelope{
		SchemaVersion:  envelope.SchemaVersion,
		Provider:       envelope.Provider{Name: ProviderName, Version: "builtin"},
		PromptFragment: promptFragment,
	}
	for _, f := range files {
		x := envelope.Expansion{
			Role:      RoleGuideline,
			Priority:  priority,
			Symbol:    f.symbol,
			File:      f.path,
			StartLine: 1,
			EndLine:   f.lines,
			Content:   f.content,
			Details:   map[string]string{"scope": f.scope},
		}
		if f.truncated {
			x.Details["span"] = "truncated"
		}
		env.Expansions = append(env.Expansions, x)
	}
	return env, nil
}

// promptFragment tells the model what a guideline block is. Redline's context
// header names an unranked role and leaves the gloss to the provider that
// invented it, so this is where the words have to be.
const promptFragment = `The context tagged instructions is what this repository
wrote down about itself, read from the files GitHub's convention puts them in.
It is not code and it is not part of the change.

Expansions in the guideline role are rules, conventions and decisions the team
recorded. Treat them as binding on the review: a finding that contradicts one
is wrong twice, because it is also evidence the review did not read what the
team wrote. A rule scoped to particular paths is only carried here when this
change touches them.

They are not a checklist to audit the change against, and a change that
follows them deserves no comment saying so.
`

// rule is one instruction file that governs this change.
type rule struct {
	path      string
	symbol    string
	scope     string
	content   string
	lines     int
	truncated bool
}

// applicable finds the instruction files that govern the changed paths.
func applicable(root string, changed []string) ([]rule, error) {
	var out []rule
	if r, ok, err := read(root, rootInstructions, nil); err != nil {
		return nil, err
	} else if ok {
		out = append(out, r)
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(instructionsDir)))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("reading %s: %w", instructionsDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		rel := instructionsDir + "/" + e.Name()
		r, ok, err := read(root, rel, changed)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r)
		}
	}
	// Deterministic order, and the repository-wide file first: it is the one
	// that always applies, so it reads as the frame for the scoped ones.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].path == rootInstructions) != (out[j].path == rootInstructions) {
			return out[i].path == rootInstructions
		}
		return out[i].path < out[j].path
	})
	return out, nil
}

// read loads one instruction file and decides whether it governs the change.
// changed being nil means the file is repository-wide and applies regardless.
func read(root, rel string, changed []string) (rule, bool, error) {
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return rule{}, false, nil
		}
		return rule{}, false, fmt.Errorf("reading %s: %w", rel, err)
	}
	front, text := splitFrontmatter(string(body))
	if strings.TrimSpace(text) == "" {
		// An empty rule file is not a rule. Carrying it would spend budget
		// to tell the model nothing and make the report claim a rule was
		// read.
		return rule{}, false, nil
	}
	globs := applyTo(front)
	scope := "repository"
	if changed != nil && len(globs) > 0 {
		if !matchesAny(globs, changed) {
			return rule{}, false, nil
		}
		scope = strings.Join(globs, ", ")
	}
	content, lines, truncated := clamp(text)
	return rule{
		path:      rel,
		symbol:    describe(front, text, rel),
		scope:     scope,
		content:   content,
		lines:     lines,
		truncated: truncated,
	}, true, nil
}

// matchesAny reports whether any changed path falls under any of the globs.
func matchesAny(globs, changed []string) bool {
	for _, path := range changed {
		if globmatch.MatchesAny(globs, normPath(path)) {
			return true
		}
	}
	return false
}

func normPath(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(p), "./")
}

// splitFrontmatter separates a leading YAML block from the prose. The prose is
// what the model reads; the frontmatter is metadata about when to read it.
func splitFrontmatter(body string) (front, text string) {
	s := strings.TrimLeft(body, "\ufeff")
	if !strings.HasPrefix(s, "---") {
		return "", s
	}
	rest := strings.TrimPrefix(s, "---")
	rest = strings.TrimPrefix(rest, "\r")
	if !strings.HasPrefix(rest, "\n") {
		// `---something` is a horizontal rule or prose, not a delimiter.
		return "", s
	}
	rest = rest[1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		// An unterminated block is prose that happens to start with a rule.
		return "", s
	}
	front = rest[:end]
	after := rest[end+len("\n---"):]
	if i := strings.Index(after, "\n"); i >= 0 {
		after = after[i+1:]
	} else {
		after = ""
	}
	return front, after
}

// applyTo reads the frontmatter's path globs. Absent or empty means the file
// governs everything, which is how GitHub reads it: a rule with no scope is a
// rule for the repository.
func applyTo(front string) []string {
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "applyTo") {
			continue
		}
		var globs []string
		for _, g := range strings.Split(unquote(strings.TrimSpace(value)), ",") {
			if g = strings.TrimSpace(unquote(strings.TrimSpace(g))); g != "" && g != "**" && g != "**/*" {
				globs = append(globs, g)
			}
		}
		return globs
	}
	return nil
}

func unquote(s string) string {
	for _, q := range []string{`"`, `'`} {
		if len(s) >= 2 && strings.HasPrefix(s, q) && strings.HasSuffix(s, q) {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// describe names the block for the reader: the frontmatter's description if
// the author wrote one, else the first heading, else the path.
func describe(front, text, rel string) string {
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "description") {
			if d := unquote(strings.TrimSpace(value)); d != "" {
				return d
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if h := strings.TrimSpace(line); strings.HasPrefix(h, "#") {
			if h = strings.TrimSpace(strings.TrimLeft(h, "#")); h != "" {
				return h
			}
		}
	}
	return rel
}

// clamp bounds one file's content and reports what it kept.
func clamp(text string) (content string, lines int, truncated bool) {
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) <= maxLines {
		return strings.Join(all, "\n"), len(all), false
	}
	return strings.Join(all[:maxLines], "\n"), maxLines, true
}
