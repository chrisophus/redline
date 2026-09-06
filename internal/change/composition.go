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

// Composition groups a change's files by language and kind, so a reviewer
// can see how much of the diff is test versus behavior versus config before
// reading a single line of it. Generated files never reach here: run.go
// filters them out of the change before change.Build ever sees them, and a
// composition table is not the exception the way that exclusion list is.
func Composition(files []File) []LinesRow {
	type key struct{ lang, kind string }
	byKey := map[key]*LinesRow{}
	var order []key
	for _, f := range files {
		lang := f.Language
		if lang == "" {
			lang = "other"
		}
		k := key{lang, kind(f.Path)}
		row, ok := byKey[k]
		if !ok {
			row = &LinesRow{Language: lang, Kind: k.kind}
			byKey[k] = row
			order = append(order, k)
		}
		row.Files++
		row.Added += f.Added
		row.Removed += f.Removed
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
	rows := make([]LinesRow, len(order))
	for i, k := range order {
		rows[i] = *byKey[k]
	}
	return rows
}

// kind classifies what role a file plays in the change. Test wins over
// config or docs when a path could read as either (a Markdown file inside a
// testdata directory is still docs; the boundary Redline actually cares
// about is test-vs-not, checked first).
func kind(path string) string {
	lower := strings.ToLower(path)
	base := filepath.Base(lower)
	ext := filepath.Ext(lower)

	if strings.HasSuffix(lower, "_test.go") || strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		strings.Contains(lower, "/tests/") || strings.HasPrefix(lower, "tests/") ||
		strings.Contains(lower, "/test/") || strings.HasPrefix(lower, "test/") ||
		strings.Contains(lower, "/testdata/") || strings.HasPrefix(lower, "testdata/") {
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
