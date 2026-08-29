package packet

import (
	"path/filepath"
	"strings"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/instructions"
	"github.com/ccason/redline/internal/target"
)

// maxDiffBytes caps a single file's diff in the packet. A generated file with
// a 40,000-line diff would crowd out every hand-written change in the review.
const maxDiffBytes = 60000

// Build assembles the packet from an already-resolved target and report.
func Build(repo *gitx.Repo, tgt *target.Target, baseSHA string, changed []string, det []findings.Finding) *Packet {
	p := &Packet{
		Version:       Version,
		Target:        tgt,
		BaseSHA:       baseSHA,
		Deterministic: det,
		Guidance:      DefaultGuidance(),
	}
	if p.Deterministic == nil {
		p.Deterministic = []findings.Finding{}
	}
	if commits, err := repo.Log(baseSHA, headRev(tgt)); err == nil {
		p.Commits = commits
	}
	stats := map[string]gitx.DiffStat{}
	if s, err := repo.Stat(baseSHA, tgt.Head); err == nil {
		p.Stats = s
		for _, st := range s {
			stats[st.Path] = st
		}
	}
	baseBlobs, _ := repo.Blobs(baseSHA)
	workBlobs, _ := repo.WorktreeBlobs()
	for _, path := range changed {
		fc := FileChange{
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
		fc.Diff = diff
		fc.Status = fileStatus(path, baseBlobs, workBlobs)
		if fc.Status == "" {
			fc.Status = status(diff)
		}
		p.Files = append(p.Files, fc)
	}
	all := instructions.Discover(tgt.Dir)
	p.Instructions = instructions.For(all, changed)
	return p
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
	case ".tsx", ".jsx", ".vue", ".svelte", ".css", ".scss":
		add("ui")
	case ".ts", ".js":
		if strings.Contains(lower, "/web/") || strings.Contains(lower, "/ui/") ||
			strings.Contains(lower, "/frontend/") || strings.Contains(lower, "/components/") {
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
