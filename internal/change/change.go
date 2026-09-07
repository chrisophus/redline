// Package change describes the change under review: every file with its diff,
// classified for the report's sections, with machine output already removed.
//
// This is internal structure, not a machine interface. The packet that used to
// carry this shape to a reviewing agent is gone; an agent that wants the diff
// runs git, and the evidence it should not re-derive is in findings.json. The
// report still needs the diffs to render its walkthrough and drill-ins, and
// that is what this package supplies.
package change

import (
	"path/filepath"
	"strings"

	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/target"
)

// maxDiffBytes caps a single file's diff. A file with a 40,000-line diff would
// crowd out every hand-written change on the report.
const maxDiffBytes = 60000

// Set is the change: the target, the commits, and every changed file.
type Set struct {
	Target  *target.Target `json:"target"`
	BaseSHA string         `json:"baseSHA"`

	Commits []gitx.Commit `json:"commits,omitempty"`
	Files   []File        `json:"files"`

	// UITouched is true when at least one changed file is classified as UI.
	// It decides how loud the absence of captures should be on the report: a
	// change that moves the interface with nothing captured is a gap, one that
	// touches no UI is not.
	UITouched bool `json:"uiTouched"`
}

// File is one changed file with its diff.
type File struct {
	Path     string `json:"path"`
	Status   string `json:"status"` // added | modified | deleted
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Diff     string `json:"diff,omitempty"`
	Language string `json:"language,omitempty"`
	// Areas classifies the file for the report's drill-in sections.
	Areas []string `json:"areas,omitempty"`
}

// Build assembles the change from an already-resolved target.
func Build(repo *gitx.Repo, tgt *target.Target, baseSHA string, changed []string) *Set {
	s := &Set{Target: tgt, BaseSHA: baseSHA}
	if commits, err := repo.Log(baseSHA, headRev(tgt)); err == nil {
		s.Commits = commits
	}
	stats := map[string]gitx.DiffStat{}
	if st, err := repo.Stat(baseSHA, tgt.Head); err == nil {
		for _, d := range st {
			stats[d.Path] = d
		}
	}
	baseBlobs, _ := repo.Blobs(baseSHA)
	workBlobs, _ := repo.WorktreeBlobs()
	for _, path := range changed {
		f := File{
			Path:     path,
			Added:    stats[path].Added,
			Removed:  stats[path].Removed,
			Language: language(path),
			Areas:    Areas(path),
		}
		diff := repo.DiffPath(baseSHA, path)
		if len(diff) > maxDiffBytes {
			diff = diff[:maxDiffBytes] + "\n... diff truncated; read the file directly\n"
		}
		f.Diff = diff
		f.Status = fileStatus(path, baseBlobs, workBlobs)
		if f.Status == "" {
			f.Status = status(diff)
		}
		s.Files = append(s.Files, f)
	}
	s.UITouched = touchesUI(s.Files)
	return s
}

func touchesUI(files []File) bool {
	for _, f := range files {
		for _, a := range f.Areas {
			if a == "ui" {
				return true
			}
		}
	}
	return false
}

func headRev(t *target.Target) string {
	if t.Head == "" {
		return "HEAD"
	}
	return t.Head
}

func status(diff string) string {
	switch {
	case strings.Contains(diff, "\nnew file mode "):
		return "added"
	case strings.Contains(diff, "\ndeleted file mode "):
		return "deleted"
	default:
		return "modified"
	}
}

// fileStatus is presence in the base tree vs the worktree, not a parse of
// unified-diff headers. An empty untracked diff used to read as "modified".
func fileStatus(path string, base, work map[string]string) string {
	if base == nil || work == nil {
		return ""
	}
	_, inBase := base[path]
	_, inWork := work[path]
	switch {
	case !inBase && inWork:
		return "added"
	case inBase && !inWork:
		return "deleted"
	default:
		return "modified"
	}
}

// Areas classifies a file into the report's drill-in sections. A file can
// belong to several: an OpenAPI spec under a migrations directory is both.
func Areas(path string) []string {
	var areas []string
	add := func(a string) {
		for _, existing := range areas {
			if existing == a {
				return
			}
		}
		areas = append(areas, a)
	}
	lower := strings.ToLower(path)
	base := filepath.Base(lower)
	ext := filepath.Ext(lower)

	switch {
	case strings.HasSuffix(lower, ".sql"), strings.Contains(lower, "/migrations/"), strings.HasPrefix(lower, "migrations/"):
		add("sql")
	}
	switch {
	case strings.Contains(base, "openapi"), strings.Contains(base, "swagger"),
		strings.Contains(lower, "/api/") && (ext == ".yaml" || ext == ".yml" || ext == ".json"),
		strings.HasSuffix(lower, ".proto"):
		add("api")
	}
	switch ext {
	case ".tsx", ".jsx", ".vue", ".svelte", ".css", ".scss", ".html", ".htm":
		add("ui")
	case ".ts", ".js":
		if strings.Contains(lower, "/web/") || strings.Contains(lower, "/ui/") ||
			strings.Contains(lower, "/frontend/") || strings.Contains(lower, "/components/") {
			add("ui")
		}
	case ".tmpl":
		if strings.Contains(lower, "html") {
			add("ui")
		}
	}
	if strings.HasSuffix(lower, "_test.go") || strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") || strings.Contains(lower, "/tests/") {
		add("tests")
	}
	if len(areas) == 0 {
		add("code")
	}
	return areas
}

func language(path string) string {
	switch filepath.Ext(strings.ToLower(path)) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js", ".jsx":
		return "javascript"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	case ".css", ".scss":
		return "css"
	default:
		return ""
	}
}
