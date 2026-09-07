package harness

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccason/redline/internal/cover"
)

func TestArtifactStaleIntegration(t *testing.T) {
	dir := t.TempDir()
	cov := filepath.Join(dir, "coverage.out")
	goFile := filepath.Join(dir, "a.go")
	if err := os.WriteFile(cov, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(cov, old, old); err != nil {
		t.Fatal(err)
	}
	if !cover.ArtifactStale(dir, "coverage.out", []string{"a.go"}) {
		t.Fatal("expected stale")
	}
}
