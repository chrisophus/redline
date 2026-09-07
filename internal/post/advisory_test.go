package post

import (
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

func gateProfile() *Profile {
	return &Profile{
		ReviewMarker:  "m:v1",
		FindingMarker: "f:v1",
		Blocking:      []findings.Severity{findings.SeverityError, findings.SeverityWarning},
	}
}

// Panes gate, reviewers advise. A reviewer's finding is nondeterministic and
// costs money to reproduce, so a merge queue must never turn on one.
func TestAReviewersFindingNeverFailsTheGate(t *testing.T) {
	rep := &findings.Report{Findings: []findings.Finding{{
		Rule: "agent-comment", Substrate: "redline/review", File: "a.go", Line: 1,
		Category: findings.CategoryReview, Severity: findings.SeverityError,
		Message: "this looks wrong to me", Source: findings.SourceLLM,
	}}}
	if got := GateVerdict(rep, gateProfile()); got != "pass" {
		t.Fatalf("GateVerdict = %q, want pass: an advisory finding must not block a merge", got)
	}
}

func TestACorrelationFindingNeverFailsTheGate(t *testing.T) {
	rep := &findings.Report{Findings: []findings.Finding{{
		Rule: "correlation", Substrate: "redline/review", File: "a.go", Line: 1,
		Category: findings.CategoryCorrelation, Severity: findings.SeverityError,
		Message: "two producers disagree", Source: findings.SourceLLM,
	}}}
	if got := GateVerdict(rep, gateProfile()); got != "pass" {
		t.Fatalf("GateVerdict = %q, want pass", got)
	}
}

func TestAPaneFindingStillFailsTheGate(t *testing.T) {
	rep := &findings.Report{Findings: []findings.Finding{{
		Rule: "migration-modified-after-merge", Substrate: "redline/sql", File: "m.sql",
		Category: findings.CategorySchema, Severity: findings.SeverityError,
		Message: "edited after merge", Source: findings.SourceDeterministic,
	}}}
	if got := GateVerdict(rep, gateProfile()); got != "fail" {
		t.Fatalf("GateVerdict = %q, want fail: a measurement still gates", got)
	}
}

func TestAReviewersFindingGetsNoBlockingMarker(t *testing.T) {
	llm := findings.Finding{Severity: findings.SeverityError, Source: findings.SourceLLM}
	if m := findingAttestMarker(gateProfile(), llm); m != "" {
		t.Fatalf("a reviewer's finding must open no blocking thread, got %q", m)
	}
	pane := findings.Finding{Severity: findings.SeverityError, Source: findings.SourceDeterministic}
	if m := findingAttestMarker(gateProfile(), pane); m == "" {
		t.Fatal("a pane's error must still stamp the gate marker")
	}
}
