package change_test

import (
	"fmt"
	"testing"

	"github.com/chrisophus/redline/internal/change"
)

// realisticChangedPaths builds a slice of plausible repo paths mixing
// generated markers, test files, vendored dependencies, and hand-written
// source — the shape of the file list Build hands to Generated and Areas on
// every invocation, over the whole diff.
//
// The 20-slot distribution is 55% hand-written, 15% test files, 15%
// generated output, 10% vendor, 5% lockfiles/checksums.
func realisticChangedPaths(n int) []string {
	handWritten := []func(i int) string{
		func(i int) string { return fmt.Sprintf("internal/service%d/handler.go", i) },
		func(i int) string { return fmt.Sprintf("internal/service%d/model.go", i) },
		func(i int) string { return fmt.Sprintf("cmd/redline/cmd%d.go", i) },
		func(i int) string { return fmt.Sprintf("web/src/components/Widget%d.tsx", i) },
		func(i int) string { return fmt.Sprintf("migrations/%06d_add_table.up.sql", i) },
		func(i int) string { return fmt.Sprintf("docs/design%d.md", i) },
	}
	testFiles := []func(i int) string{
		func(i int) string { return fmt.Sprintf("internal/service%d/handler_test.go", i) },
		func(i int) string { return fmt.Sprintf("web/src/components/Widget%d.spec.ts", i) },
	}
	generated := []func(i int) string{
		func(i int) string { return fmt.Sprintf("internal/api/oas_schemas_gen_%d.go", i) },
		func(i int) string { return fmt.Sprintf("internal/pb/message_%d.pb.go", i) },
		func(i int) string { return fmt.Sprintf("internal/gen/zz_generated_%d.go", i) },
	}
	vendor := []func(i int) string{
		func(i int) string { return fmt.Sprintf("vendor/github.com/example/pkg%d/x.go", i) },
	}
	lock := []func(i int) string{
		func(i int) string { return "go.sum" },
		func(i int) string { return "pnpm-lock.yaml" },
	}

	slots := make([]func(i int) string, 0, 20)
	slots = append(slots, handWritten[0], handWritten[1], handWritten[2], handWritten[0], handWritten[3],
		handWritten[1], handWritten[4], handWritten[2], handWritten[5], handWritten[0], handWritten[3])
	slots = append(slots, testFiles[0], testFiles[1], testFiles[0])
	slots = append(slots, generated[0], generated[1], generated[2])
	slots = append(slots, vendor[0], vendor[0])
	slots = append(slots, lock[0])

	paths := make([]string, n)
	for i := range n {
		paths[i] = slots[i%len(slots)](i)
	}
	return paths
}

// BenchmarkGenerated exercises the classification every invocation runs over
// the whole diff before any pane sees a file.
func BenchmarkGenerated(b *testing.B) {
	paths := realisticChangedPaths(500)
	b.ReportAllocs()
	for b.Loop() {
		kept, generated := change.Generated("", paths, nil)
		if len(kept)+len(generated) != len(paths) {
			b.Fatal("Generated lost or duplicated paths")
		}
	}
}

// BenchmarkAreas exercises the per-file drill-in classification every
// invocation runs over the whole diff.
func BenchmarkAreas(b *testing.B) {
	paths := realisticChangedPaths(500)
	b.ReportAllocs()
	for b.Loop() {
		var total int
		for _, p := range paths {
			total += len(change.Areas(p))
		}
		if total == 0 {
			b.Fatal("Areas classified nothing")
		}
	}
}
