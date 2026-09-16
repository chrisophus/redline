package change

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadExclude(t *testing.T) {
	dir := writeConfig(t, ".redline.yml", `harness:
  profiles:
    - path: coverage.out
      produce:
        command: make
        args: [cover]
exclude:
  - "internal/oas/"
  - "**/*.gen.ts"
`)
	got, err := LoadExclude(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "internal/oas/" || got[1] != "**/*.gen.ts" {
		t.Fatalf("exclude = %v, want both patterns in order", got)
	}
}

// The other spelling of the file, and the cases where there is nothing to read.
// A repository with no config is the common one and is not an error.
func TestLoadExcludeAbsentIsNotAnError(t *testing.T) {
	yaml := writeConfig(t, ".redline.yaml", "exclude: [\"gen/**\"]\n")
	got, err := LoadExclude(yaml)
	if err != nil || len(got) != 1 || got[0] != "gen/**" {
		t.Fatalf("exclude = %v, err = %v", got, err)
	}
	for _, dir := range []string{t.TempDir(), ""} {
		got, err := LoadExclude(dir)
		if err != nil || got != nil {
			t.Errorf("LoadExclude(%q) = %v, %v; want nil, nil", dir, got, err)
		}
	}
	// A harness-only config has no exclude key, and that is not an error
	// either: harness.Load and this one read the same file for different keys.
	harnessOnly := writeConfig(t, ".redline.yml", "harness:\n  worktree:\n    - path: dist\n      produce:\n        command: make\n")
	if got, err := LoadExclude(harnessOnly); err != nil || got != nil {
		t.Errorf("harness-only config = %v, %v; want nil, nil", got, err)
	}
}

// Malformed YAML stops the run. Read as an empty list it would review the tree
// the config meant to trim, and nothing would say why.
func TestLoadExcludeRejectsMalformedYAML(t *testing.T) {
	dir := writeConfig(t, ".redline.yml", "exclude: [unclosed\n")
	if _, err := LoadExclude(dir); err == nil {
		t.Fatal("malformed yaml returned no error")
	}
}
