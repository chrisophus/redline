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
	prev.Intent = &Intent{
		Ticket: &IntentTicket{ID: "REL-24", Title: "Serve the report over HTTP"},
		Fit:    &IntentFit{Thing: "Yes — the ticket asked for loopback serving.", Way: "Yes, though the port scan is undocumented."},
	}
	prev.Surfaces = &Surfaces{
		Interface: &Surface{Line: "Report page gained a comment bar.", Moved: true},
		API:       &Surface{Line: "No spec in this change.", Moved: false},
	}
	next := &Review{Findings: []Judgment{{File: "a.go", Message: "boom"}}}

	got := Merge(prev, next)
	if got.Summary != prev.Summary {
		t.Fatalf("summary lost: %q", got.Summary)
	}
	if got.Intent == nil || got.Intent.Ticket == nil || got.Intent.Ticket.ID != "REL-24" {
		t.Fatalf("intent lost: %+v", got.Intent)
	}
	if got.Intent.Fit == nil || got.Intent.Fit.Thing == "" {
		t.Fatalf("fit lost: %+v", got.Intent)
	}
	if got.Surfaces == nil || got.Surfaces.Interface == nil || !got.Surfaces.Interface.Moved {
		t.Fatalf("surfaces lost: %+v", got.Surfaces)
	}
	if got.Surfaces.API == nil || got.Surfaces.API.Line == "" {
		t.Fatal("idle surface copy lost — an absent line reads as a pass")
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

func TestParseReviewOrientation(t *testing.T) {
	raw := `{
	  "summary": "Serve the report over loopback.",
	  "intent": {
	    "ticket": {"id": "REL-24", "title": "Serve the report", "url": "https://example.test/REL-24"},
	    "pr": {"number": 1, "title": "serve", "url": "https://example.test/pr/1"},
	    "fit": {"thing": "Yes.", "way": "Mostly — the retry is undocumented."}
	  },
	  "surfaces": {
	    "interface": {"line": "Comment bar added.", "moved": true},
	    "api": {"line": "No spec in this change.", "moved": false},
	    "schema": {"line": "No migrations touched.", "moved": false}
	  }
	}`
	rev, err := ParseReview(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Intent.Ticket.ID != "REL-24" || rev.Intent.Ticket.URL == "" {
		t.Fatalf("ticket: %+v", rev.Intent.Ticket)
	}
	if rev.Intent.PR.Number != 1 {
		t.Fatalf("pr: %+v", rev.Intent.PR)
	}
	if rev.Intent.Fit.Thing != "Yes." || rev.Intent.Fit.Way == "" {
		t.Fatalf("fit: %+v", rev.Intent.Fit)
	}
	if !rev.Surfaces.Interface.Moved {
		t.Fatal("interface should read as moved")
	}
	// An idle surface keeps its sentence; that sentence is the difference
	// between "looked, nothing moved" and silence.
	if rev.Surfaces.API.Moved || rev.Surfaces.API.Line == "" {
		t.Fatalf("api surface: %+v", rev.Surfaces.API)
	}
}

// Pre-push there is no pull request and often no ticket. That is the common
// case, not an error.
func TestParseReviewWithoutOrientationLinks(t *testing.T) {
	for _, raw := range []string{
		`{"summary":"just a summary"}`,
		`{"summary":"s","intent":{}}`,
		`{"summary":"s","intent":{"fit":{"thing":"Yes."}}}`,
	} {
		rev, err := ParseReview(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if rev.Summary == "" {
			t.Fatalf("%s: summary lost", raw)
		}
	}
}

func TestParseReviewRejectsMalformedOrientation(t *testing.T) {
	for _, raw := range []string{
		`{"summary":"s","intent":"REL-24"}`,
		`{"summary":"s","surfaces":{"interface":{"moved":"yes"}}}`,
	} {
		if _, err := ParseReview(strings.NewReader(raw)); err == nil {
			t.Fatalf("%s: expected a parse error", raw)
		} else if !strings.Contains(err.Error(), "review is not valid JSON") {
			t.Fatalf("%s: unhelpful error %v", raw, err)
		}
	}
}

// An empty object is not a value. A follow-up ingest that sends `intent: {}`
// must not blank the orientation the screen leads with.
func TestMergeKeepsPreviousWhenOrientationIsEmpty(t *testing.T) {
	prev := &Review{
		Intent:   &Intent{Ticket: &IntentTicket{ID: "REL-24"}},
		Surfaces: &Surfaces{Schema: &Surface{Line: "One migration added.", Moved: true}},
	}
	next := &Review{Intent: &Intent{}, Surfaces: &Surfaces{Interface: &Surface{}}}

	got := Merge(prev, next)
	if got.Intent == nil || got.Intent.Ticket == nil || got.Intent.Ticket.ID != "REL-24" {
		t.Fatalf("intent blanked by an empty object: %+v", got.Intent)
	}
	if got.Surfaces == nil || got.Surfaces.Schema == nil || !got.Surfaces.Schema.Moved {
		t.Fatalf("surfaces blanked by an empty object: %+v", got.Surfaces)
	}
}

func TestMergeReplacesOrientationWholesale(t *testing.T) {
	prev := &Review{Intent: &Intent{
		Ticket: &IntentTicket{ID: "REL-24"},
		Fit:    &IntentFit{Thing: "Yes."},
	}}
	next := &Review{Intent: &Intent{PR: &IntentPR{Number: 9}}}

	got := Merge(prev, next)
	if got.Intent.PR == nil || got.Intent.PR.Number != 9 {
		t.Fatalf("new intent should win: %+v", got.Intent)
	}
	if got.Intent.Ticket != nil || got.Intent.Fit != nil {
		t.Fatal("intent replaces wholesale; a stale ticket must not outlive it")
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
