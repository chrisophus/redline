package packet

import (
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
)

func TestParseReviewToleratesFence(t *testing.T) {
	raw := "Here is the review:\n```json\n{\"summary\":\"ok\",\"findings\":[{\"file\":\"a.go\",\"line\":3,\"rule\":\"x\",\"message\":\"bug\"}]}\n```\n"
	rev, err := ParseReview(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Summary != "ok" || len(rev.Findings) != 1 || rev.Findings[0].File != "a.go" {
		t.Fatalf("unexpected parse: %+v", rev)
	}
}

func TestApplyDedupesAndSorts(t *testing.T) {
	rep := findings.Report{
		Findings: []findings.Finding{{
			File: "b.go", Rule: "det", Substrate: "redline/sql",
			Category: findings.CategorySchema, Severity: findings.SeverityInfo,
			Message: "observed", Source: findings.SourceDeterministic,
		}},
	}
	rev := &Review{
		Findings: []Judgment{
			{File: "a.go", Rule: "x", Message: "agent bug", Severity: findings.SeverityError},
			{File: "a.go", Rule: "x", Message: "agent bug", Severity: findings.SeverityError},
		},
		Unknowns: []string{"could not tell"},
	}
	Apply(&rep, rev)
	Apply(&rep, rev)
	if len(rep.Findings) != 2 {
		t.Fatalf("expected det + one agent finding, got %d", len(rep.Findings))
	}
	if rep.Findings[0].Source != findings.SourceLLM || rep.Findings[0].Severity != findings.SeverityError {
		t.Fatalf("error-severity agent finding should sort first: %+v", rep.Findings[0])
	}
	n := 0
	for _, u := range rep.Unknowns {
		if u.Message == "could not tell" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unknowns should dedupe, got %d copies", n)
	}
}

func TestAreas(t *testing.T) {
	cases := map[string][]string{
		"migrations/0001_init.up.sql": {"sql"},
		"api/openapi.yaml":            {"api"},
		"web/src/App.tsx":             {"ui"},
		"pkg/foo_test.go":             {"tests"},
		"cmd/redline/main.go":         {"code"},
	}
	for path, want := range cases {
		got := Areas(path)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v want %v", path, got, want)
		}
	}
}

func TestToFindingsStampsLLM(t *testing.T) {
	fs := (&Review{Findings: []Judgment{{File: "a.go", Rule: "r", Message: "m"}}}).ToFindings()
	if len(fs) != 1 || fs[0].Source != findings.SourceLLM || fs[0].Substrate != Substrate {
		t.Fatalf("agent findings must be llm-sourced: %+v", fs)
	}
}
