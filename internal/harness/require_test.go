package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisophus/redline/internal/harness"
)

func TestRequireMissingProfile(t *testing.T) {
	cfg := &harness.Config{
		Profiles: []harness.Profile{{
			ID: "go", Path: "coverage.out", When: "stale",
			Produce: harness.ProduceConfig{Command: "true"},
			Scope:   []string{"**/*.go"},
		}},
	}
	err := harness.Require(harness.Roots{Observe: t.TempDir()}, []string{"a.go"}, cfg, harness.RequireOpts{})
	if err == nil {
		t.Fatal("expected missing artifact error")
	}
}

func TestRequireSkipsWhenScopeMisses(t *testing.T) {
	dir := t.TempDir()
	cfg := &harness.Config{
		Profiles: []harness.Profile{{
			ID: "go", Path: "coverage.out", When: "stale",
			Produce: harness.ProduceConfig{Command: "true"},
			Scope:   []string{"ui/**"},
		}},
	}
	if err := harness.Require(harness.Roots{Observe: dir}, []string{"internal/a.go"}, cfg, harness.RequireOpts{}); err != nil {
		t.Fatalf("scope miss should not require profile: %v", err)
	}
}

func TestRequireSkipsCoverageWhenAsked(t *testing.T) {
	cfg := &harness.Config{
		Profiles: []harness.Profile{{
			ID: "go", Path: "coverage.out", When: "stale",
			Produce: harness.ProduceConfig{Command: "true"},
			Scope:   []string{"**/*.go"},
		}},
	}
	if err := harness.Require(harness.Roots{Observe: t.TempDir()}, []string{"a.go"}, cfg, harness.RequireOpts{SkipCoverage: true}); err != nil {
		t.Fatalf("expected coverage skip: %v", err)
	}
}

func TestRequireIgnoresCallerCoverageOnDetachedWorktree(t *testing.T) {
	observe := t.TempDir()
	caller := t.TempDir()
	if err := os.WriteFile(filepath.Join(caller, "coverage.out"), []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &harness.Config{
		Profiles: []harness.Profile{{
			ID: "go", Path: "coverage.out", When: "stale",
			Produce: harness.ProduceConfig{Command: "true"},
			Scope:   []string{"**/*.go"},
		}},
	}
	err := harness.Require(harness.Roots{Observe: observe, Caller: caller}, []string{"a.go"}, cfg, harness.RequireOpts{})
	if err == nil {
		t.Fatal("caller coverage must not satisfy a detached worktree review")
	}
}

func TestRequireOnlyMutationProfileLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".redline.yml"), []byte(`harness:
  profiles:
    - id: mutation
      path: mutants.json
      when: stale
      scope: ["**/*.go"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := harness.Load(dir)
	if err != nil || cfg == nil || len(cfg.Profiles) != 1 {
		t.Fatalf("cfg=%v err=%v", cfg, err)
	}
	if err := harness.Require(harness.Roots{Observe: dir}, []string{"a.go"}, cfg, harness.RequireOpts{}); err == nil {
		t.Fatal("expected missing mutants.json")
	}
}
