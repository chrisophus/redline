package harness

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNeedsProduce(t *testing.T) {
	dir := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	profile := Profile{
		ID:      "go",
		Path:    "coverage.out",
		When:    "stale",
		Produce: ProduceConfig{Command: "true"},
	}

	if !needsProduce(dir, profile, []string{"a.go"}) {
		t.Fatal("missing artifact should need produce")
	}

	write("coverage.out", "mode: set\n")
	write("a.go", "package a\n")
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "coverage.out"), old, old); err != nil {
		t.Fatal(err)
	}

	if !needsProduce(dir, profile, []string{"a.go"}) {
		t.Fatal("artifact older than changed file should need produce")
	}

	profile.When = "missing"
	if needsProduce(dir, profile, []string{"a.go"}) {
		t.Fatal("when missing should skip existing artifact")
	}

	profile.Scope = []string{"ui/**"}
	if needsProduce(dir, Profile{ID: "ui", Path: "ui/cov.json", When: "always", Produce: profile.Produce, Scope: profile.Scope}, []string{"internal/a.go"}) {
		t.Fatal("scope mismatch should skip produce")
	}
}

func TestPrepareRunsProduce(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "write.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ok > out.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Profiles: []Profile{{
			ID:      "out",
			Path:    "out.txt",
			When:    "missing",
			Produce: ProduceConfig{Command: "sh", Args: []string{"write.sh"}},
		}},
	}
	produced, err := Prepare(dir, dir, []string{"a.go"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(produced) != 1 || produced[0] != "out" {
		t.Fatalf("produced: %v", produced)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err != nil {
		t.Fatal("artifact not written")
	}
}

func TestLoadValidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".redline.yml"), []byte(`harness:
  profiles:
    - path: out.txt
      produce: {command: true}
      when: bogus
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("expected validation error for when")
	}
}

func TestLoadWorktreeOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".redline.yml"), []byte(`harness:
  worktree:
    - path: ui/dist/index.html
      produce: {command: make, args: [stub-ui]}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil || cfg == nil || len(cfg.Worktree) != 1 {
		t.Fatalf("cfg=%v err=%v", cfg, err)
	}
	if cfg.Worktree[0].When != "missing" {
		t.Fatalf("default when: %q", cfg.Worktree[0].When)
	}
}

func TestPrepareWorktreeMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Worktree: []Profile{{
			ID:      "ui",
			Path:    "ui/dist/index.html",
			When:    "missing",
			Produce: ProduceConfig{Command: "mkdir", Args: []string{"-p", "ui/dist"}},
		}},
	}
	produced, err := PrepareWorktree(dir, dir, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(produced) != 1 {
		t.Fatalf("produced: %v", produced)
	}
}

func TestEnvFromConfigRoot(t *testing.T) {
	config := t.TempDir()
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "env.sh"), []byte("export MARKER=ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "write.sh"), []byte("#!/bin/sh\necho \"$MARKER\" > out.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Env: EnvConfig{From: "env.sh"},
		Profiles: []Profile{{
			ID:      "out",
			Path:    "out.txt",
			When:    "missing",
			Produce: ProduceConfig{Command: "sh", Args: []string{"write.sh"}},
		}},
	}
	produced, err := Prepare(worktree, config, []string{"a.go"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(produced) != 1 {
		t.Fatalf("produced: %v", produced)
	}
	data, err := os.ReadFile(filepath.Join(worktree, "out.txt"))
	if err != nil || string(data) != "ok\n" {
		t.Fatalf("out.txt: %q err=%v", data, err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil || cfg != nil {
		t.Fatalf("missing config: cfg=%v err=%v", cfg, err)
	}
}

// A produce step's own output goes to stderr with everything else the run
// says. Stdout is the report, or with --format json the report as JSON, and
// a test transcript in front of it is what broke piping that JSON to jq.
func TestProduceOutputStaysOffStdout(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Profiles: []Profile{{
			ID:      "out",
			Path:    "out.txt",
			When:    "missing",
			Produce: ProduceConfig{Command: "sh", Args: []string{"-c", "echo produced-on-stdout; echo produced-on-stderr >&2; echo ok > out.txt"}},
		}},
	}
	stdout, stderr := capture(t, func() {
		if _, err := Prepare(dir, dir, []string{"a.go"}, cfg); err != nil {
			t.Error(err)
		}
	})
	if strings.Contains(stdout, "produced-on-stdout") {
		t.Errorf("the produce step's stdout reached the report's stdout: %q", stdout)
	}
	for _, want := range []string{"produced-on-stdout", "produced-on-stderr"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr must carry the produce step's %s, got %q", want, stderr)
		}
	}
}

// capture runs fn with os.Stdout and os.Stderr replaced and returns what
// each received.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	read := func(f **os.File) func() string {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := *f
		*f = w
		done := make(chan string)
		go func() {
			b, _ := io.ReadAll(r)
			done <- string(b)
		}()
		return func() string {
			_ = w.Close()
			*f = orig
			return <-done
		}
	}
	outDone := read(&os.Stdout)
	errDone := read(&os.Stderr)
	fn()
	return outDone(), errDone()
}
