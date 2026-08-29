package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppliesGlob(t *testing.T) {
	f := File{ApplyTo: "internal/**/*.go"}
	if !f.Applies("internal/run/run.go") {
		t.Fatal("expected match")
	}
	if f.Applies("cmd/redline/main.go") {
		t.Fatal("cmd should not match internal/**")
	}
	all := File{}
	if !all.Applies("anything") {
		t.Fatal("empty applyTo governs everything")
	}
}

func TestDiscoverNestedCursorRulesAndCap(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, ".cursor", "rules", "go")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\napplyTo: \"**/*.go\"\n---\n\nUse tabs.\n"
	if err := os.WriteFile(filepath.Join(nested, "go.mdc"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", maxInstructionBytes+50)
	if err := os.WriteFile(filepath.Join(root, "CONTRIBUTING.md"), []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Discover(root)
	var sawNested, sawCap bool
	for _, f := range got {
		if f.Path == ".cursor/rules/go/go.mdc" {
			sawNested = true
			if f.ApplyTo != "**/*.go" {
				t.Fatalf("applyTo: %q", f.ApplyTo)
			}
			if !f.Applies("pkg/a.go") {
				t.Fatal("nested rule should apply to go files")
			}
		}
		if f.Path == "CONTRIBUTING.md" {
			sawCap = true
			if len(f.Content) > maxInstructionBytes+80 {
				t.Fatalf("content not capped: %d", len(f.Content))
			}
			if !strings.Contains(f.Content, "truncated") {
				t.Fatal("expected truncation marker")
			}
		}
	}
	if !sawNested {
		t.Fatalf("nested cursor rule not discovered: %+v", got)
	}
	if !sawCap {
		t.Fatal("CONTRIBUTING.md not discovered")
	}
}

func TestForFiltersByApplyTo(t *testing.T) {
	files := []File{
		{Path: "all.md", Content: "repo"},
		{Path: "go.md", ApplyTo: "**/*.go", Content: "go only"},
		{Path: "sql.md", ApplyTo: "**/*.sql", Content: "sql only"},
	}
	got := For(files, []string{"pkg/a.go"})
	if len(got) != 2 {
		t.Fatalf("expected all.md + go.md, got %+v", got)
	}
}
