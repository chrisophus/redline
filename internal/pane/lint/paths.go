package lint

import (
	"path/filepath"
	"strings"
)

// normalizeIssues rewrites issue file paths to stable repo-relative form so
// base and head fingerprints match even when a tool reports absolute paths or
// vacuum's bundled spec artifact (root.yaml).
func normalizeIssues(repoRoot string, issues []Issue, scope []string) []Issue {
	if len(issues) == 0 {
		return issues
	}
	out := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		issue.File = normalizeIssueFile(repoRoot, issue.File, scope)
		out = append(out, issue)
	}
	return out
}

func normalizeIssueFile(repoRoot, file string, scope []string) string {
	file = filepath.ToSlash(strings.TrimSpace(file))
	if file == "" {
		return file
	}
	if rel := repoRelativePath(repoRoot, file); rel != "" {
		file = rel
	}
	file = strings.TrimPrefix(file, "./")
	if isBundledSpecPath(file) {
		if spec := primarySpecScope(scope); spec != "" {
			return spec
		}
	}
	return file
}

func repoRelativePath(repoRoot, file string) string {
	if repoRoot == "" {
		return ""
	}
	repoRoot = filepath.Clean(repoRoot)
	clean := filepath.Clean(file)
	if rel, err := filepath.Rel(repoRoot, clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return ""
}

func isBundledSpecPath(file string) bool {
	base := strings.ToLower(filepath.Base(file))
	return base == "root.yaml" || base == "root.yml" || base == "root.json"
}

func primarySpecScope(scope []string) string {
	var specs []string
	for _, p := range scope {
		p = filepath.ToSlash(p)
		base := strings.ToLower(filepath.Base(p))
		if strings.Contains(base, "openapi") || strings.Contains(base, "swagger") {
			specs = append(specs, p)
		}
	}
	if len(specs) == 1 {
		return specs[0]
	}
	return ""
}
