package post

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
)

// severityCycle is the mix of severities a real review sees: mostly warnings
// and info, errors are rarer.
var severityCycle = [...]findings.Severity{
	findings.SeverityError, findings.SeverityWarning, findings.SeverityWarning,
	findings.SeverityInfo, findings.SeverityInfo,
}

// genReport builds a report with n findings spread over 20 files, four in
// five of them located on a changed line and the rest unlocated (riding in
// the body) — the shape BuildAttest actually walks on a large pull request.
func genReport(n int) *findings.Report {
	rep := &findings.Report{
		Coverage: findings.Coverage{ChangedFiles: 20, ExaminedFiles: 20},
		Substrates: []findings.SubstrateStatus{
			{Name: "migrations", State: findings.SubstrateRan},
			{Name: "openapi", State: findings.SubstrateSkipped, Detail: "no files in scope for this pane"},
			{Name: "lint", State: findings.SubstrateRan},
		},
	}
	for i := range n {
		f := findings.Finding{
			Rule:      fmt.Sprintf("rule-%d", i%12),
			Substrate: "migrations",
			Severity:  severityCycle[i%len(severityCycle)],
			Message:   fmt.Sprintf("finding %d: a realistic diagnostic message describing the issue in some detail", i),
		}
		if i%5 != 0 {
			f.File = fmt.Sprintf("internal/pkg%d/file%d.go", i%20, i%20)
			f.Line = (i % 50) + 1
		}
		rep.Findings = append(rep.Findings, f)
	}
	rep.Finalize()
	return rep
}

// genCommentable builds the commentable-line sets for the same file names
// genReport uses, as CommentableLines would derive from a fetched PR file
// list, so most located findings land as line comments rather than the body.
func genCommentable(nFiles int) map[string]map[int]bool {
	out := make(map[string]map[int]bool, nFiles)
	for i := range nFiles {
		lines := make(map[int]bool, 50)
		for l := 1; l <= 50; l++ {
			lines[l] = true
		}
		out[fmt.Sprintf("internal/pkg%d/file%d.go", i, i)] = lines
	}
	return out
}

// BenchmarkBuildPayload exercises assembling the full review payload — the
// per-finding comment/body split, comment bodies, and the evidence table —
// from a report with 200 findings against a 20-file PR, sizes representative
// of a real large pull request review.
func BenchmarkBuildPayload(b *testing.B) {
	rep := genReport(200)
	tgt := prTarget()
	commentable := genCommentable(20)
	b.ReportAllocs()
	for b.Loop() {
		Build(rep, tgt, "https://ci/report.html", commentable)
	}
}

// genManyHunkPatch builds a single file's patch with nHunks hunks, each
// touching a handful of lines further down the file — the shape of a patch
// GitHub returns for a file with many scattered changes.
func genManyHunkPatch(nHunks int) string {
	var b strings.Builder
	line := 1
	for h := range nHunks {
		start := line
		fmt.Fprintf(&b, "@@ -%d,3 +%d,4 @@ func f%d()\n", start, start, h)
		b.WriteString(" ctx\n")
		b.WriteString("+added one\n")
		b.WriteString("+added two\n")
		b.WriteString("-removed\n")
		b.WriteString(" ctx2\n")
		line = start + 10
	}
	return b.String()
}

// BenchmarkCommentableLines exercises the diff-position mapping in diff.go
// over a many-hunk diff — the shape of a large, heavily-edited file, which a
// review build must map before any finding on it can become a line comment.
func BenchmarkCommentableLines(b *testing.B) {
	patch := genManyHunkPatch(200)
	files := map[string]string{"internal/big/file.go": patch}
	b.ReportAllocs()
	for b.Loop() {
		CommentableLines(files)
	}
}
