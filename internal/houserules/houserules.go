// Package houserules reads the instruction files a repository writes for the
// agents that work on it, and hands them to the review as context.
//
// A review that contradicts the house rules is wrong twice: the finding is
// wrong, and it is evidence the tool did not read what the team wrote down.
// The scout already looks for these files, but the scout calls a model and is
// off by default, so on most runs nothing read them. None of them needs a
// model to find: convention fixes where they live, which makes them free and
// deterministic to resolve, and `redline run` costing nothing is a property
// worth keeping.
//
// Three shapes are read. The first two are GitHub's:
//
//   - .github/copilot-instructions.md, which governs the whole repository.
//   - .github/instructions/*.md, each carrying an `applyTo` frontmatter glob
//     that says which files it governs. A change that touches none of them
//     does not carry the rule, which is the point of the field.
//
// The third is the convention file by name: AGENTS.md, CLAUDE.md,
// CONTRIBUTING.md and the rest, at the repository root and in the directories
// the change touches. These are a convention rather than a specification, and
// the scout has read them since it shipped. But the scout calls a model and is
// off by default, so on a repository whose rules live in CLAUDE.md the review
// never saw them, and field use produced findings dismissed for contradicting
// exactly those rules. A name in a fixed set of places needs no model to find,
// which is what lets every run carry them.
//
// A nested file is scoped by where it sits: internal/foo/AGENTS.md governs
// changes under internal/foo and is not carried for a change that touches
// nothing there. That is the same rule applyTo states explicitly, read from
// the location instead, and it is the convention every agent already follows.
package houserules

import (
	"fmt"
	"os"
	"path"
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

// conventionNames are what a repository calls the file it writes its own rules
// in. Looked for at the root and in the directories the change touches.
//
// A list of names rather than a search: these are a convention, and a
// repository that keeps its rules somewhere else is not served by guessing.
// The scout's own list, because a rule that reaches the review with the scout
// on and vanishes with it off is worse than either.
var conventionNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"CONTRIBUTING.md",
	"CONVENTIONS.md",
	"STYLE.md",
	"STYLEGUIDE.md",
	".cursorrules",
}

// rootOnlyConventions sit at fixed paths and govern the whole repository.
var rootOnlyConventions = []string{
	".github/CONTRIBUTING.md",
	"docs/CONTRIBUTING.md",
}

// maxConventionFiles bounds how many of these one review carries. A monorepo
// where every package has an AGENTS.md would otherwise spend the whole
// guideline budget on a wide change, and the ones nearest the change are the
// ones with something specific to say. What is cut is counted, not hidden.
const maxConventionFiles = 6

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
	files, omitted, err := applicable(root, changed)
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
	if omitted > 0 {
		// A rule that was found and dropped is not the same as one that does
		// not exist, and only the note can tell the reader which this was.
		env.Notes = append(env.Notes, fmt.Sprintf(
			"%d further convention file(s) apply to this change and are not carried; "+
				"the ones nearest the changed files were kept", omitted))
	}
	return env, nil
}

// promptFragment tells the model what a guideline block is. Redline's context
// header names an unranked role and leaves the gloss to the provider that
// invented it, so this is where the words have to be.
const promptFragment = `The context tagged instructions is what this repository
wrote down about itself, read from the files a team keeps its rules in: the
ones GitHub's convention names, and AGENTS.md, CLAUDE.md, CONTRIBUTING.md and
their kin at the root and beside the changed code. It is not code and it is
not part of the change.

Expansions in the guideline role are rules, conventions and decisions the team
recorded. Treat them as binding on the review: a finding that contradicts one
is wrong twice, because it is also evidence the review did not read what the
team wrote. A rule scoped to particular paths is only carried here when this
change touches them, and a block whose scope names a directory was written
about that directory: it is the nearest word on the code it governs, and it
narrows anything the repository-wide blocks say.

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
	// depth is how far the file sits from the repository root. Zero is
	// repository-wide. It orders the block, and it decides what is dropped
	// first when there are more convention files than one review carries: a
	// file beside the changed code has something specific to say and the root
	// one has already been read a hundred times.
	depth int
}

// applicable finds the instruction files that govern the changed paths, and
// reports how many were found and not carried.
func applicable(root string, changed []string) ([]rule, int, error) {
	var out []rule
	if r, ok, err := read(root, rootInstructions, nil); err != nil {
		return nil, 0, err
	} else if ok {
		out = append(out, r)
	}
	conv, omitted, err := conventions(root, changed)
	if err != nil {
		return nil, 0, err
	}
	out = append(out, conv...)
	scoped, err := scopedInstructions(root, changed)
	if err != nil {
		return nil, 0, err
	}
	out = append(out, scoped...)
	// Deterministic order, outermost first: a repository-wide rule is the
	// frame a nested one narrows, so reading it second reads as a correction
	// to something already stated rather than as the statement itself.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].path == rootInstructions) != (out[j].path == rootInstructions) {
			return out[i].path == rootInstructions
		}
		if out[i].depth != out[j].depth {
			return out[i].depth < out[j].depth
		}
		return out[i].path < out[j].path
	})
	return out, omitted, nil
}

// scopedInstructions reads .github/instructions/*.md, keeping the ones whose
// applyTo covers a changed path.
func scopedInstructions(root string, changed []string) ([]rule, error) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(instructionsDir)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", instructionsDir, err)
	}
	var out []rule
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
	return out, nil
}

// conventions finds the rule files a repository writes by name: at the root,
// and in every directory on the path from the root to a changed file.
//
// The walk is what makes a nested file worth reading. A package with its own
// AGENTS.md has said something specific about that package, and it is the
// thing a review of that package is most likely to contradict. The same walk
// is what scopes it: a rule found at internal/foo governs internal/foo, so a
// change that touches nothing there never sees it.
func conventions(root string, changed []string) ([]rule, int, error) {
	dirs := map[string]bool{".": true}
	for _, p := range changed {
		d := path.Dir(normPath(p))
		for d != "." && d != "/" && d != "" {
			dirs[d] = true
			d = path.Dir(d)
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	// Nearest last, so the deepest rule reads as the final word, and stable
	// within a depth so two runs of the same change agree.
	sort.Slice(ordered, func(i, j int) bool {
		di, dj := depthOf(ordered[i]), depthOf(ordered[j])
		if di != dj {
			return di < dj
		}
		return ordered[i] < ordered[j]
	})

	var out []rule
	seen := map[string]bool{}
	add := func(rel string, depth int) error {
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		// changed is nil: location has already decided the scope, so an
		// applyTo in one of these would be re-deciding it against a file
		// whose author never wrote one.
		r, ok, err := read(root, rel, nil)
		if err != nil || !ok {
			return err
		}
		r.depth = depth
		if depth > 0 {
			r.scope = path.Dir(rel) + "/**"
		}
		out = append(out, r)
		return nil
	}
	for _, rel := range rootOnlyConventions {
		if err := add(rel, 0); err != nil {
			return nil, 0, err
		}
	}
	for _, d := range ordered {
		depth := depthOf(d)
		for _, name := range conventionNames {
			rel := name
			if d != "." {
				rel = d + "/" + name
			}
			if err := add(rel, depth); err != nil {
				return nil, 0, err
			}
		}
	}
	if len(out) <= maxConventionFiles {
		return out, 0, nil
	}
	// Keep the deepest, which are the ones nearest the change. The root file
	// is the one most likely to be general advice the review does not need
	// spelled out, and it is also the one a reader would think to look at.
	sort.SliceStable(out, func(i, j int) bool { return out[i].depth > out[j].depth })
	kept := out[:maxConventionFiles]
	return kept, len(out) - maxConventionFiles, nil
}

func depthOf(dir string) int {
	if dir == "." || dir == "" {
		return 0
	}
	return strings.Count(dir, "/") + 1
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
