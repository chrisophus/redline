package findings_test

import (
	"fmt"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// realisticFindings builds findings across every severity/category the
// panes emit, with a mix of file:line locations and pane-relative anchors —
// the shape of a real merged report before Finalize runs.
func realisticFindings(n int) []findings.Finding {
	categories := []findings.Category{
		findings.CategorySchema, findings.CategoryContract, findings.CategoryCover,
		findings.CategoryUI, findings.CategoryLint,
	}
	rules := []string{
		"migration-modified-after-merge", "endpoint-removed", "column-dropped",
		"diff-coverage-below-threshold", "component-prop-removed", "suppression-added",
	}
	out := make([]findings.Finding, 0, n)
	for i := range n {
		f := findings.Finding{
			Rule:      rules[i%len(rules)],
			Substrate: fmt.Sprintf("substrate-%d", i%7),
			Category:  categories[i%len(categories)],
			Message:   fmt.Sprintf("column %d dropped from table orders without a migration guard on line %d", i, i*3+1),
		}
		switch i % 3 {
		case 0:
			f.File = fmt.Sprintf("internal/service%d/handler.go", i%50)
			f.Line = i%400 + 1
		case 1:
			f.Anchor = &findings.Anchor{Kind: "migration", ID: fmt.Sprintf("%06d_add_table", i)}
		default:
			f.File = fmt.Sprintf("web/src/components/Widget%d.tsx", i%30)
			f.Line = i%200 + 1
		}
		// Four fifths arrive with an explicit severity, as a real pane would
		// set for most categories; the rest are left blank so Finalize's
		// DefaultSeverity path is exercised too.
		if i%5 != 0 {
			switch i % 3 {
			case 0:
				f.Severity = findings.SeverityError
			case 1:
				f.Severity = findings.SeverityWarning
			default:
				f.Severity = findings.SeverityInfo
			}
		}
		out = append(out, f)
	}
	return out
}

// BenchmarkReportFinalize measures the pass every finding takes on every
// run: default-severity fill, source stamping, and fingerprinting.
func BenchmarkReportFinalize(b *testing.B) {
	rep := findings.Report{Findings: realisticFindings(300)}
	b.ReportAllocs()
	for b.Loop() {
		rep.Finalize()
		if len(rep.NewCount) == 0 {
			b.Fatal("Finalize produced no severity counts")
		}
	}
}

// BenchmarkFingerprint measures the join-key computation in isolation, over
// realistic finding content (file paths, anchors, and messages with digits
// that NormalizeMessage must collapse).
func BenchmarkFingerprint(b *testing.B) {
	fs := realisticFindings(300)
	b.ReportAllocs()
	for b.Loop() {
		var total int
		for _, f := range fs {
			total += len(findings.Fingerprint(f))
		}
		if total == 0 {
			b.Fatal("Fingerprint produced empty output")
		}
	}
}
