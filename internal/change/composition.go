package change

import (
	"path/filepath"
	"sort"
	"strings"
)

// LinesRow is one (language, kind) cell of the composition table: how many
// files of this language and kind changed, and how many lines moved.
type LinesRow struct {
	Language string `json:"language"`
	Kind     string `json:"kind"`
	Files    int    `json:"files"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
}

// Kind values. Orthogonal to Language and to Areas: Areas says where a file
// lives in the report's drill-in (sql/api/ui/tests/code), Kind says what
// role it plays in the change (behavior, its test, its config, its prose).
const (
	KindTest   = "test"
	KindConfig = "config"
	KindDocs   = "docs"
	KindSource = "source"
)

// LinesGroup is a composition cell together with the files behind it, so the
// drill-in can present the language/kind breakdown as a browsable list rather
// than only a summary count.
type LinesGroup struct {
	Language string
	Kind     string
	Added    int
	Removed  int
	Files    []File
}

// CompositionGroups groups a change's files by language and kind, the dominant
// part of the change first, each group carrying the files it counts. Generated
// files never reach here: run.go filters them before change.Build sees them.
func CompositionGroups(files []File) []LinesGroup {
	type key struct{ lang, kind string }
	byKey := map[key]*LinesGroup{}
	var order []key
	for _, f := range files {
		lang := f.Language
		if lang == "" {
			lang = "other"
		}
		k := key{lang, kind(f.Path)}
		g, ok := byKey[k]
		if !ok {
			g = &LinesGroup{Language: lang, Kind: k.kind}
			byKey[k] = g
			order = append(order, k)
		}
		g.Added += f.Added
		g.Removed += f.Removed
		g.Files = append(g.Files, f)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := byKey[order[i]], byKey[order[j]]
		// The dominant part of the change leads: most added lines first.
		if a.Added != b.Added {
			return a.Added > b.Added
		}
		if a.Language != b.Language {
			return a.Language < b.Language
		}
		return a.Kind < b.Kind
	})
	groups := make([]LinesGroup, len(order))
	for i, k := range order {
		groups[i] = *byKey[k]
	}
	return groups
}

// Composition is CompositionGroups reduced to per-cell counts, for the markdown
// table and the JSON packet.
func Composition(files []File) []LinesRow {
	groups := CompositionGroups(files)
	rows := make([]LinesRow, len(groups))
	for i, g := range groups {
		rows[i] = LinesRow{Language: g.Language, Kind: g.Kind, Files: len(g.Files), Added: g.Added, Removed: g.Removed}
	}
	return rows
}

// IsTest reports whether a path is test material: a test file, a fixture, or
// anything under a test directory. The patterns are conventions rather than a
// parse, and they hold across languages, which is what lets Redline decide
// test-vs-not without a toolchain for the language in hand.
//
// It is exported because the boundary is used outside the composition table:
// the review producer holds test code back from the request, and it must draw
// the line in the same place the report's own counts do.
func IsTest(path string) bool {
	lower := strings.ToLower(path)
	base := filepath.Base(lower)
	return strings.HasSuffix(lower, "_test.go") || strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		strings.Contains(lower, "/tests/") || strings.HasPrefix(lower, "tests/") ||
		strings.Contains(lower, "/test/") || strings.HasPrefix(lower, "test/") ||
		strings.Contains(lower, "/testdata/") || strings.HasPrefix(lower, "testdata/")
}

// kind classifies what role a file plays in the change. Test wins over
// config or docs when a path could read as either (a Markdown file inside a
// testdata directory is still docs; the boundary Redline actually cares
// about is test-vs-not, checked first).
func kind(path string) string {
	lower := strings.ToLower(path)
	base := filepath.Base(lower)
	ext := filepath.Ext(lower)

	if IsTest(path) {
		return KindTest
	}
	if ext == ".md" || ext == ".rst" || ext == ".adoc" ||
		strings.HasPrefix(base, "readme") || strings.HasPrefix(base, "changelog") ||
		strings.Contains(lower, "/docs/") {
		return KindDocs
	}
	switch base {
	case "makefile", "dockerfile", "go.mod", "go.sum",
		"package.json", "package-lock.json", "go.work", "go.work.sum":
		return KindConfig
	}
	switch ext {
	case ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf", ".editorconfig":
		return KindConfig
	}
	if strings.HasPrefix(base, ".") {
		// A root dotfile with no other extension match, e.g. .gitignore,
		// .dockerignore: configuration by convention, not by suffix.
		return KindConfig
	}
	return KindSource
}
