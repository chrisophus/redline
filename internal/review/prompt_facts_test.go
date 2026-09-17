package review

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// The lint sentence says which linters ran, and only when some did. A reviewer
// told a linter ran on a change nothing linted sets aside what nothing checked.
func TestTheFindingsSayWhichLintersRan(t *testing.T) {
	ran := []findings.ToolStatus{
		{Name: "golangci-lint", Status: "ran"},
		{Name: "eslint", Status: "degraded", Detail: "base run failed"},
		{Name: "golangci-lint", Status: "ran"},
	}
	for _, tc := range []struct {
		name   string
		report *findings.Report
	}{
		{"with findings", &findings.Report{Tools: ran, Findings: []findings.Finding{{ID: "f1", File: "a.go", Message: "unused"}}}},
		{"without findings", &findings.Report{Tools: ran}},
	} {
		got := Input{Report: tc.report}.priorsSection()
		if !strings.Contains(got, "Linters ran over this change: golangci-lint, eslint.") {
			t.Errorf("%s: the section does not name each linter that ran, once:\n%s", tc.name, got)
		}
	}

	for _, report := range []*findings.Report{
		{},
		{Findings: []findings.Finding{{ID: "f1", File: "a.go", Message: "unused"}}},
	} {
		if got := (Input{Report: report}).priorsSection(); strings.Contains(got, "Linters ran") {
			t.Errorf("no linter ran, but the section says one did:\n%s", got)
		}
	}
}

// The one-shot system block is sent beside the tool catalogue on every call,
// and a call asked to think is told to answer by calling a tool, so the block
// must not say there are none. Nor may it say a linter ran regardless, or that
// what it was shown is everything.
func TestTheSystemBlockClaimsNoFactItCannotKnow(t *testing.T) {
	res, err := Assemble(exploreInput(), Options{Model: "claude-sonnet-5", API: APIAnthropic})
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range []string{"no tools", "One already ran"} {
		if strings.Contains(res.System, claim) {
			t.Errorf("the system block still says %q", claim)
		}
	}
	// Nor that the material below is all there is: a pass can read held-back
	// context, and the scout's answers arrive later.
	if strings.Contains(res.System, "Everything you get to see is below.") {
		t.Error("the system block still tells the pass its material is complete")
	}
}
