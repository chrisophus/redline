package lint

import (
	"path/filepath"
	"testing"
)

func TestNormalizeIssueFile_AbsoluteToRepoRelative(t *testing.T) {
	root := filepath.Join("/work", "repo")
	got := normalizeIssueFile(root, filepath.Join(root, "api", "openapi.yaml"), nil)
	if got != "api/openapi.yaml" {
		t.Fatalf("got %q, want api/openapi.yaml", got)
	}
}

func TestNormalizeIssueFile_BundledRootYAMLMapsToScopedSpec(t *testing.T) {
	root := "/Users/me/.redline/worktrees/abc/head"
	got := normalizeIssueFile(root, filepath.Join(root, "api", "root.yaml"), []string{"api/openapi.yaml"})
	if got != "api/openapi.yaml" {
		t.Fatalf("got %q, want api/openapi.yaml", got)
	}
}

func TestNormalizeIssueFile_FingerprintStableAcrossPaths(t *testing.T) {
	issueA := Issue{Tool: "vacuum", File: "/wt/api/root.yaml", Rule: "operation-tag-defined", Message: "tag `notifications` for `GET` operation is not defined as a global tag"}
	issueB := Issue{Tool: "vacuum", File: "api/openapi.yaml", Rule: "operation-tag-defined", Message: "tag `notifications` for `GET` operation is not defined as a global tag"}
	scope := []string{"api/openapi.yaml"}
	normA := normalizeIssues("/wt", []Issue{issueA}, scope)[0]
	normB := normalizeIssues("/wt", []Issue{issueB}, scope)[0]
	if fingerprint(normA) != fingerprint(normB) {
		t.Fatalf("normalized paths should fingerprint equally: %q vs %q", fingerprint(normA), fingerprint(normB))
	}
}

func TestDiffIssuesIgnoresBundledPathMismatch(t *testing.T) {
	scope := []string{"api/openapi.yaml"}
	base := normalizeIssues("/wt", []Issue{{
		Tool: "vacuum", File: "api/openapi.yaml", Line: 100, Rule: "operation-tag-defined",
		Message: "tag `notifications` for `GET` operation is not defined as a global tag",
	}}, scope)
	head := normalizeIssues("/wt", []Issue{{
		Tool: "vacuum", File: "/wt/api/root.yaml", Line: 200, Rule: "operation-tag-defined",
		Message: "tag `notifications` for `GET` operation is not defined as a global tag",
	}}, scope)
	introduced, resolved := diffIssues(base, head)
	if len(introduced) != 0 || resolved != 0 {
		t.Fatalf("same rule on bundled vs source path should not read as introduced: %+v resolved=%d", introduced, resolved)
	}
}
