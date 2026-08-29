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

func TestParseReviewFiles(t *testing.T) {
	raw := `{"summary":"loopback serve","files":[{"path":"internal/report/serve.go","summary":"Serves the report over HTTP."}]}`
	rev, err := ParseReview(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Files) != 1 || rev.Files[0].Path != "internal/report/serve.go" {
		t.Fatalf("%+v", rev.Files)
	}
	if rev.Files[0].Summary != "Serves the report over HTTP." {
		t.Fatalf("summary: %q", rev.Files[0].Summary)
	}
}

func TestParseReviewScreenshots(t *testing.T) {
	raw := `{"summary":"ui","screenshots":[{"route":"/x","path":"/tmp/a.png","caption":"form"}]}`
	rev, err := ParseReview(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Screenshots) != 1 || rev.Screenshots[0].Route != "/x" || rev.Screenshots[0].Caption != "form" {
		t.Fatalf("%+v", rev.Screenshots)
	}
}

func TestAreas(t *testing.T) {
	cases := map[string][]string{
		"migrations/0001_init.up.sql":             {"sql"},
		"api/openapi.yaml":                        {"api"},
		"web/src/App.tsx":                         {"ui"},
		"internal/report/assets/report.html.tmpl": {"ui"},
		"pkg/foo_test.go":                         {"tests"},
		"cmd/redline/main.go":                     {"code"},
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

// The reviewer-comments loop invites a second ingest carrying only the
// findings that changed. The summary and walkthrough live nowhere but the
// review, so omitting them must not blank the sections the report leads with.
func TestMergeKeepsOmittedNarrative(t *testing.T) {
	prev := &Review{
		Summary:       "What this change does.",
		Files:         []FileNote{{Path: "a.go", Summary: "does a"}},
		APIChanges:    []Highlight{{Title: "moved"}},
		SchemaChanges: []Highlight{{Title: "added column"}},
		Screenshots:   []Shot{{Route: "/x", Path: "/tmp/x.png"}},
	}
	next := &Review{Findings: []Judgment{{File: "a.go", Message: "boom"}}}

	got := Merge(prev, next)
	if got.Summary != prev.Summary {
		t.Fatalf("summary lost: %q", got.Summary)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "a.go" {
		t.Fatalf("walkthrough lost: %+v", got.Files)
	}
	if len(got.APIChanges) != 1 || len(got.SchemaChanges) != 1 {
		t.Fatal("contract highlights lost")
	}
	if len(got.Screenshots) != 1 {
		t.Fatal("screenshots lost")
	}
	if len(got.Findings) != 1 {
		t.Fatalf("incoming findings dropped: %+v", got.Findings)
	}
}

func TestMergePrefersTheNewReviewWhereItSpeaks(t *testing.T) {
	prev := &Review{Summary: "old", Files: []FileNote{{Path: "a.go", Summary: "old"}}}
	next := &Review{Summary: "new", Files: []FileNote{{Path: "b.go", Summary: "new"}}}

	got := Merge(prev, next)
	if got.Summary != "new" {
		t.Fatalf("got %q", got.Summary)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "b.go" {
		t.Fatalf("got %+v", got.Files)
	}
}

func TestMergeHandlesNils(t *testing.T) {
	if got := Merge(nil, nil); got != nil {
		t.Fatal("expected nil")
	}
	only := &Review{Summary: "x"}
	if got := Merge(nil, only); got != only {
		t.Fatal("expected the new review")
	}
	if got := Merge(only, nil); got != only {
		t.Fatal("expected the previous review")
	}
}
