package report

import (
	"testing"

	"github.com/ccason/redline/internal/change"
	"github.com/ccason/redline/internal/findings"
)

func TestFileWalkKeepsChangeOrder(t *testing.T) {
	rows := fileWalk(
		[]change.File{
			{Path: "b.go", Status: "modified", Added: 2, Removed: 1},
			{Path: "a.go", Status: "added", Added: 8},
		},
		nil,
	)
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].Path != "b.go" || rows[1].Path != "a.go" {
		t.Fatalf("walk must keep change order: %+v", rows)
	}
}

// The walk is the first list a reviewer reads, so a file carrying a finding
// has to say so there rather than only inside the collapsed drill-in.
func TestFileWalkCountsFindingsPerFile(t *testing.T) {
	rows := fileWalk(
		[]change.File{{Path: "a.go"}, {Path: "b.go"}},
		[]findings.Finding{
			{File: "a.go", Severity: findings.SeverityWarning},
			{File: "a.go", Severity: findings.SeverityError},
			{File: "nowhere.go", Severity: findings.SeverityError},
			{Severity: findings.SeverityError},
		},
	)
	if rows[0].Findings != 2 || rows[0].Severity != string(findings.SeverityError) {
		t.Fatalf("a.go should carry 2 findings worst=error, got %d/%q", rows[0].Findings, rows[0].Severity)
	}
	if rows[1].Findings != 0 || rows[1].Severity != "" {
		t.Fatalf("b.go should carry none, got %d/%q", rows[1].Findings, rows[1].Severity)
	}
}
