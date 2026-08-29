package instructions_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ccason/redline/internal/instructions"
)

func write(t *testing.T, root, path, body string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoversCopilotConventions(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/copilot-instructions.md", "Always use citext for email.\n")
	write(t, root, ".github/instructions/go.instructions.md",
		"---\napplyTo: \"**/*.go\"\n---\nWrap errors with %w.\n")

	files := instructions.Discover(root)
	if len(files) != 2 {
		t.Fatalf("expected both Copilot locations, got %d: %+v", len(files), files)
	}
	repo, scoped := files[0], files[1]
	if repo.ApplyTo != "" || repo.Format != "copilot" {
		t.Fatalf("repo-wide file: %+v", repo)
	}
	if scoped.ApplyTo != "**/*.go" {
		t.Fatalf("expected applyTo glob, got %q", scoped.ApplyTo)
	}
	if got := scoped.Content; got != "Wrap errors with %w.\n" {
		t.Fatalf("frontmatter should be stripped from the body, got %q", got)
	}
}

func TestApplyToScoping(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/*.go", "internal/run/run.go", true},
		{"**/*.go", "web/app.tsx", false},
		{"web/**", "web/src/app.tsx", true},
		{"web/**", "internal/run.go", false},
		{"**/*.sql,**/*.go", "migrations/1_a.up.sql", true},
		{"", "anything", true},
		{"*.md", "README.md", true},
	}
	for _, c := range cases {
		f := instructions.File{ApplyTo: c.pattern}
		if got := f.Applies(c.path); got != c.want {
			t.Errorf("applyTo %q vs %q: got %v want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// A scoped instruction file must not reach the packet when the change does not
// touch anything it governs — the review budget is attention, not tokens.
func TestForFiltersByChangedPaths(t *testing.T) {
	files := []instructions.File{
		{Path: "a.md", ApplyTo: "**/*.go"},
		{Path: "b.md", ApplyTo: "web/**"},
		{Path: "c.md"},
	}
	got := instructions.For(files, []string{"internal/run.go"})
	if len(got) != 2 || got[0].Path != "a.md" || got[1].Path != "c.md" {
		t.Fatalf("expected the Go rule and the repo-wide rule, got %+v", got)
	}
}
