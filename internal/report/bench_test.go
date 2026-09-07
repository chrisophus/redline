package report

import (
	"fmt"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/findings"
)

// realisticFiles builds changed files with real unified diffs across the
// report's drill-in areas — the shape of a real multi-package change.
func realisticFiles(n int) []change.File {
	areaCycle := [][]string{{"code"}, {"tests"}, {"ui"}, {"api"}, {"sql"}}
	files := make([]change.File, 0, n)
	for i := range n {
		path := fmt.Sprintf("pkg%d/file%d.go", i, i)
		oldLine := i*3 + 1
		diff := "diff --git a/" + path + " b/" + path + "\n" +
			"--- a/" + path + "\n" +
			"+++ b/" + path + "\n" +
			fmt.Sprintf("@@ -%d,4 +%d,5 @@ func handler%d() {\n", oldLine, oldLine, i) +
			" \tctx := context.Background()\n" +
			fmt.Sprintf("-\told := compute(%d)\n", i) +
			fmt.Sprintf("+\tnew := compute(%d)\n", i) +
			"+\tlog.Printf(\"computed %d\", new)\n" +
			" \treturn nil\n"
		files = append(files, change.File{
			Path:    path,
			Status:  "modified",
			Added:   3,
			Removed: 1,
			Diff:    diff,
			Areas:   areaCycle[i%len(areaCycle)],
		})
	}
	return files
}

// realisticReport builds a finalized report with findings spread across
// severities, categories, and the given files.
func realisticReport(nFindings int, files []change.File) *findings.Report {
	rules := []string{"migration-modified-after-merge", "endpoint-removed", "diff-coverage-below-threshold", "component-prop-removed"}
	cats := []findings.Category{findings.CategorySchema, findings.CategoryContract, findings.CategoryCover, findings.CategoryUI}
	fs := make([]findings.Finding, 0, nFindings)
	for i := range nFindings {
		fs = append(fs, findings.Finding{
			File:      files[i%len(files)].Path,
			Line:      i%40 + 1,
			Rule:      rules[i%len(rules)],
			Substrate: fmt.Sprintf("substrate-%d", i%5),
			Category:  cats[i%len(cats)],
			Message:   fmt.Sprintf("finding %d: behavior changed without a matching guard", i),
		})
	}
	rep := &findings.Report{
		BaseSHA:  "0123456789abcdef0123456789abcdef01234567",
		Findings: fs,
		Coverage: findings.Coverage{
			ChangedFiles:  len(files),
			ExaminedFiles: len(files),
			Diff:          &cover.Result{Profile: "coverage.out", Lines: 400, Covered: 300, Percent: 75},
		},
		Confirmations: []findings.Confirmation{{Substrate: "s", Rule: "r", Message: "no drift detected"}},
	}
	rep.Finalize()
	return rep
}

// BenchmarkMarkdown renders the markdown report for a realistic change: 200
// findings across ~150 files with diffs.
func BenchmarkMarkdown(b *testing.B) {
	files := realisticFiles(150)
	ch := &change.Set{Files: files, UITouched: true}
	rep := realisticReport(200, files)
	b.ReportAllocs()
	for b.Loop() {
		out := Markdown(rep, nil, nil, ch)
		if len(out) == 0 {
			b.Fatal("empty markdown report")
		}
	}
}

// BenchmarkHTML renders the HTML report for the same realistic change. HTML
// render is the biggest single unit of work Redline does per run: it
// highlights every diff line for every file, not just the ones with
// findings.
func BenchmarkHTML(b *testing.B) {
	files := realisticFiles(150)
	ch := &change.Set{Files: files, UITouched: true}
	rep := realisticReport(200, files)
	in := HTMLInput{Report: rep, Change: ch}
	b.ReportAllocs()
	for b.Loop() {
		out, err := HTML(in)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("empty html report")
		}
	}
}
