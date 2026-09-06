package findings_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/cover"
	"github.com/ccason/redline/internal/findings"
)

func TestFinalizeDefaultsSeverityFromCategory(t *testing.T) {
	cases := []struct {
		category findings.Category
		want     findings.Severity
	}{
		{findings.CategorySchema, findings.SeverityError},
		{findings.CategoryContract, findings.SeverityError},
		{findings.CategoryCover, findings.SeverityWarning},
		{findings.CategoryUI, findings.SeverityWarning},
		{findings.CategoryLint, findings.SeverityWarning},
	}
	for _, c := range cases {
		rep := findings.Report{Findings: []findings.Finding{{Category: c.category}}}
		rep.Finalize()
		if got := rep.Findings[0].Severity; got != c.want {
			t.Errorf("%s: got severity %q, want %q", c.category, got, c.want)
		}
	}
}

func TestFinalizeKeepsExplicitSeverity(t *testing.T) {
	rep := findings.Report{Findings: []findings.Finding{
		{Category: findings.CategorySchema, Severity: findings.SeverityInfo},
	}}
	rep.Finalize()
	if rep.Findings[0].Severity != findings.SeverityInfo {
		t.Fatalf("Finalize must not override an explicit severity, got %q", rep.Findings[0].Severity)
	}
}

func TestFinalizeStampsSourceNewAndFingerprint(t *testing.T) {
	rep := findings.Report{Findings: []findings.Finding{
		{Category: findings.CategoryLint, Rule: "r", Message: "m", File: "a.go"},
	}}
	rep.Finalize()
	got := rep.Findings[0]
	if got.Source != findings.SourceDeterministic {
		t.Errorf("Source should default to deterministic, got %q", got.Source)
	}
	if !got.New {
		t.Error("every Redline pane is diff-based; New must always be true")
	}
	want := findings.Fingerprint(findings.Finding{File: "a.go", Rule: "r", Message: "m"})
	if got.Fingerprint != want {
		t.Errorf("stamped fingerprint %q does not match Fingerprint(f) %q", got.Fingerprint, want)
	}
}

// NewCount is the number a reader trusts to know how many findings of each
// severity landed, without counting the list themselves.
func TestFinalizeNewCountAggregation(t *testing.T) {
	rep := findings.Report{Findings: []findings.Finding{
		{Category: findings.CategorySchema},                              // -> error
		{Category: findings.CategorySchema},                              // -> error
		{Category: findings.CategoryUI, Severity: findings.SeverityInfo}, // explicit
		{Category: findings.CategoryLint},                                // -> warning
	}}
	rep.Finalize()
	want := map[findings.Severity]int{
		findings.SeverityError:   2,
		findings.SeverityInfo:    1,
		findings.SeverityWarning: 1,
	}
	if len(rep.NewCount) != len(want) {
		t.Fatalf("NewCount = %+v, want %+v", rep.NewCount, want)
	}
	for sev, n := range want {
		if rep.NewCount[sev] != n {
			t.Errorf("NewCount[%s] = %d, want %d", sev, rep.NewCount[sev], n)
		}
	}
}

func TestFinalizeNilFindingsAndSubstratesMarshalAsEmptyArrays(t *testing.T) {
	rep := findings.Report{}
	rep.Finalize()
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"findings":null`) || strings.Contains(string(b), `"substrates":null`) {
		t.Fatalf("Finalize must leave findings/substrates JSON-encodable as [], not null: %s", b)
	}
}

func TestDarkSubstratesSelectsFailedOnly(t *testing.T) {
	rep := findings.Report{Substrates: []findings.SubstrateStatus{
		{Name: "a", State: findings.SubstrateRan},
		{Name: "b", State: findings.SubstrateSkipped},
		{Name: "c", State: findings.SubstrateFailed, Detail: "panic"},
	}}
	dark := rep.DarkSubstrates()
	if len(dark) != 1 || dark[0].Name != "c" {
		t.Fatalf("DarkSubstrates should return only the failed pane, got %+v", dark)
	}
}

// The skill that emits findings.json and the merge gate that reads it back
// depend on these exact field names; a rename here is a silent wire-format
// break neither side would compile-catch.
func TestReportJSONFieldNamesMatchTheWireContract(t *testing.T) {
	rep := findings.Report{
		Substrates:    []findings.SubstrateStatus{{Name: "s", State: findings.SubstrateRan}},
		Confirmations: []findings.Confirmation{{Substrate: "s", Rule: "r", Message: "ok"}},
		Unknowns:      []findings.Unknown{{Substrate: "s", Message: "could not tell"}},
		Coverage:      findings.Coverage{Diff: &cover.Result{Profile: "coverage.out", Lines: 10, Covered: 5, Percent: 50}},
	}
	rep.Finalize()
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, field := range []string{`"unknowns"`, `"confirmations"`, `"substrates"`, `"diffCoverage"`} {
		if !strings.Contains(body, field) {
			t.Errorf("Report JSON must carry field %s; the skill/merge gate reads it by name: %s", field, body)
		}
	}
}
