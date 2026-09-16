package post

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/target"
)

func TestLoadProfileDefaultsBlockErrorAndWarning(t *testing.T) {
	path := writeProfile(t, "review_marker: example-agent-review:v1\nfinding_marker: example-agent-finding:v1\n")
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
		ReviewMarker:  "example-agent-review:v1",
		FindingMarker: "example-agent-finding:v1",
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
	fail := "<!-- example-agent-review:v1 verdict=fail head=deadbeef -->"
	pass := "<!-- example-agent-review:v1 verdict=pass head=deadbeef -->"
	other := "<!-- example-agent-review:v1 verdict=fail head=cafef00d -->"
	got := AttestedVerdict([]string{fail, other, pass}, "example-agent-review:v1", "deadbeef")
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

// Stale renders its notice with the body, so it survives Unposted rendering the
// body again. Only a profile that requires the head withholds the verdict.
func TestAStaleReviewKeepsItsNoticeAndWithholdsOnlyWhenRequired(t *testing.T) {
	rep := &findings.Report{Findings: []findings.Finding{{
		File: "a.go", Line: 3, Rule: "r", Substrate: "migrations",
		Severity: findings.SeverityError, Message: "m",
	}}}
	rep.Finalize()
	tgt := &target.Target{Kind: target.KindPR, Head: "deadbeef"}
	for _, tc := range []struct {
		name        string
		requireHead bool
		wantMarker  bool
	}{
		{"require_head", true, false},
		{"head not required", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prof := &Profile{ReviewMarker: "gate:v1", FindingMarker: "finding:v1",
				Blocking: []findings.Severity{findings.SeverityError}, RequireHead: tc.requireHead}
			p := BuildAttest(rep, tgt, "", nil, prof, nil).Stale("cafef00d").Unposted(map[string]bool{})
			if !strings.Contains(p.Body, "no longer the head") {
				t.Errorf("the stale notice was lost when the body was rendered again:\n%s", p.Body)
			}
			if got := strings.Contains(p.Body, "gate:v1 verdict=fail head=deadbeef"); got != tc.wantMarker {
				t.Errorf("verdict marker present = %v, want %v:\n%s", got, tc.wantMarker, p.Body)
			}
			if p.Attested() != tc.wantMarker {
				t.Errorf("Attested() = %v, want %v", p.Attested(), tc.wantMarker)
			}
		})
	}
}
