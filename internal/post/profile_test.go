package post

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

func TestLoadProfileDefaultsBlockErrorAndWarning(t *testing.T) {
	path := writeProfile(t, "review_marker: mct-agent-review:v1\nfinding_marker: mct-agent-finding:v1\n")
	p, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AuthorOnly || !p.RequireHead || !p.FailClosedPane {
		t.Fatalf("strict defaults: %+v", p)
	}
	if !p.Blocks(findings.SeverityError) || !p.Blocks(findings.SeverityWarning) {
		t.Fatal("error and warning must block")
	}
	if p.Blocks(findings.SeverityInfo) {
		t.Fatal("info must not block unless listed")
	}
}

func TestLoadProfileRequiresMarkers(t *testing.T) {
	path := writeProfile(t, "author_only: false\n")
	if _, err := LoadProfile(path); err == nil {
		t.Fatal("missing markers must fail")
	}
}

func TestGateVerdictWarningIsFail(t *testing.T) {
	p := &Profile{
		ReviewMarker:  "mct-agent-review:v1",
		FindingMarker: "mct-agent-finding:v1",
		Blocking:      []findings.Severity{findings.SeverityError, findings.SeverityWarning},
	}
	rep := &findings.Report{Findings: []findings.Finding{{
		Severity: findings.SeverityWarning, Message: "edited migration",
	}}}
	if got := GateVerdict(rep, p); got != "fail" {
		t.Fatalf("warning should fail, got %q", got)
	}
}

func TestGateVerdictInfoAloneIsPass(t *testing.T) {
	p := &Profile{
		Blocking: []findings.Severity{findings.SeverityError, findings.SeverityWarning},
	}
	rep := &findings.Report{Findings: []findings.Finding{{
		Severity: findings.SeverityInfo, Message: "coverage unknown",
	}}}
	if got := GateVerdict(rep, p); got != "pass" {
		t.Fatalf("info should pass, got %q", got)
	}
}

func TestGateVerdictFailedPaneIsFail(t *testing.T) {
	p := &Profile{FailClosedPane: true, Blocking: []findings.Severity{findings.SeverityError}}
	rep := &findings.Report{Substrates: []findings.SubstrateStatus{{
		Name: "migrations", State: findings.SubstrateFailed,
	}}}
	if got := GateVerdict(rep, p); got != "fail" {
		t.Fatalf("a pane that applied and did not run should fail, got %q", got)
	}
}

func TestAttestedVerdictReadsLastForHead(t *testing.T) {
	fail := "<!-- mct-agent-review:v1 verdict=fail head=deadbeef -->"
	pass := "<!-- mct-agent-review:v1 verdict=pass head=deadbeef -->"
	other := "<!-- mct-agent-review:v1 verdict=fail head=cafef00d -->"
	got := AttestedVerdict([]string{fail, other, pass}, "mct-agent-review:v1", "deadbeef")
	if got != "pass" {
		t.Fatalf("got %q", got)
	}
}

func writeProfile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "redline-review.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
