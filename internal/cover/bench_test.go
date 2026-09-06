package cover

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// genProfile builds a synthetic Go coverage profile with nFiles files, each
// contributing blocksPerFile statement blocks. Large enough to resemble a
// real repository-wide profile, which parseProfile re-scans on every run.
func genProfile(nFiles, blocksPerFile int) string {
	var b strings.Builder
	b.WriteString("mode: set\n")
	for f := range nFiles {
		name := fmt.Sprintf("github.com/x/y/internal/pkg%d/file%d.go", f, f)
		line := 1
		for i := range blocksPerFile {
			start, end := line, line+2
			count := (i + f) % 3 // mix of covered and uncovered blocks
			fmt.Fprintf(&b, "%s:%d.1,%d.2 2 %d\n", name, start, end, count)
			line = end + 3
		}
	}
	return b.String()
}

// genLargeDiff builds a unified diff touching nFiles files with addedPerFile
// added lines each, interleaved with a context and a removed line the way a
// real multi-file pull request diff looks.
func genLargeDiff(nFiles, addedPerFile int) string {
	var b strings.Builder
	for f := range nFiles {
		name := fmt.Sprintf("internal/pkg%d/file%d.go", f, f)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", name, name)
		b.WriteString("index 1111111..2222222 100644\n")
		fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", name, name)
		fmt.Fprintf(&b, "@@ -10,2 +10,%d @@ func F%d() {\n", addedPerFile+1, f)
		b.WriteString(" \tctx()\n")
		for i := range addedPerFile {
			fmt.Fprintf(&b, "+\tline%d()\n", i)
		}
		b.WriteString("-\told()\n")
	}
	return b.String()
}

// BenchmarkParseProfile exercises the coverage-profile scanner on a profile
// large enough to resemble a real repository's combined profile: diff
// coverage re-parses this file from disk on every redline run.
func BenchmarkParseProfile(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "coverage.out")
	if err := os.WriteFile(path, []byte(genProfile(200, 25)), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseProfile(path); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAddedLines exercises the unified-diff scanner on a diff touching
// 200 files with 5000 total added lines — the shape of a large pull request,
// which Compute walks on every redline run to intersect against the coverage
// profile.
func BenchmarkAddedLines(b *testing.B) {
	diff := genLargeDiff(200, 25)
	b.ReportAllocs()
	for b.Loop() {
		AddedLines(diff)
	}
}
