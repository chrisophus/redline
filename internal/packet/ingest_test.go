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

func TestParseReviewIntentAndSurfaces(t *testing.T) {
	raw := `{
		"summary":"A reviewer can open the report in Cursor and see what each file does.",
		"intent":{
			"pr":{"number":1,"title":"Serve the report over loopback","url":"https://github.com/chrisophus/redline/pull/1"},
			"ticket":{"id":"REL-24","title":"Serve the report over HTTP","url":"https://example.invalid/REL-24"}
		},
		"surfaces":{
			"interface":{"line":"The HTML report is the product surface.","moved":true},
			"api":{"line":"Did not move. No spec or handler in the packet.","moved":false},
			"schema":{"line":"Did not move. Migrations pane skipped.","moved":false}
		},
		"apiChanges":[{"title":"unchanged list — still valid"}]
	}`
	rev, err := ParseReview(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Summary != "A reviewer can open the report in Cursor and see what each file does." {
		t.Fatalf("summary is the headline: %q", rev.Summary)
	}
	if rev.Intent == nil || rev.Intent.PR == nil || rev.Intent.Ticket == nil {
		t.Fatalf("intent links lost: %+v", rev.Intent)
	}
	if rev.Intent.PR.Number != 1 || rev.Intent.PR.Title != "Serve the report over loopback" {
		t.Fatalf("pr: %+v", rev.Intent.PR)
	}
	if rev.Intent.Ticket.ID != "REL-24" || rev.Intent.Ticket.URL != "https://example.invalid/REL-24" {
		t.Fatalf("ticket: %+v", rev.Intent.Ticket)
	}
	if rev.Surfaces == nil || rev.Surfaces.Interface == nil || rev.Surfaces.API == nil || rev.Surfaces.Schema == nil {
		t.Fatalf("surfaces lost: %+v", rev.Surfaces)
	}
	if !rev.Surfaces.Interface.Moved || rev.Surfaces.Interface.Line != "The HTML report is the product surface." {
		t.Fatalf("interface: %+v", rev.Surfaces.Interface)
	}
	if rev.Surfaces.API.Moved || rev.Surfaces.API.Line == "" {
		t.Fatalf("a quiet idle surface keeps its line and moved false: %+v", rev.Surfaces.API)
	}
	if rev.Surfaces.Schema.Moved {
		t.Fatalf("schema moved false preserved: %+v", rev.Surfaces.Schema)
	}
	if len(rev.APIChanges) != 1 {
		t.Fatalf("apiChanges still parse beside surfaces: %+v", rev.APIChanges)
	}
}

func TestParseReviewRejectsInvalidIntentType(t *testing.T) {
	raw := `{"summary":"x","intent":"PR #1"}`
	_, err := ParseReview(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "review is not valid JSON") {
		t.Fatalf("intent as a string must fail closed, got err=%v", err)
	}
}

func TestParseReviewRejectsInvalidMovedType(t *testing.T) {
	raw := `{"summary":"x","surfaces":{"interface":{"moved":"yes"}}}`
	_, err := ParseReview(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "review is not valid JSON") {
		t.Fatalf("moved as a string must fail closed, got err=%v", err)
	}
}

func TestParseReviewIgnoresUnknownGoal(t *testing.T) {
	raw := `{"summary":"x","goal":"must be ignored"}`
	rev, err := ParseReview(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Summary != "x" {
		t.Fatalf("summary stays the headline; unknown keys are ignored: %+v", rev)
	}
}

func TestParseReviewAllowsOmittedIntentLinks(t *testing.T) {
	// Summary only: no intent object at all.
	rev, err := ParseReview(strings.NewReader(`{"summary":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Intent != nil {
		t.Fatalf("no intent object means nil intent: %+v", rev.Intent)
	}
	// An intent object carrying neither pr nor ticket is not an error.
	rev, err = ParseReview(strings.NewReader(`{"summary":"x","intent":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Intent == nil {
		t.Fatal("an empty intent object still parses")
	}
	if rev.Intent.PR != nil || rev.Intent.Ticket != nil {
		t.Fatalf("missing links stay nil, not an error: %+v", rev.Intent)
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
		Intent:        &Intent{PR: &IntentPR{Number: 1, Title: "the PR"}, Ticket: &IntentTicket{ID: "REL-24"}},
		Surfaces:      &Surfaces{Interface: &Surface{Line: "moved the surface", Moved: true}},
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
	if got.Intent == nil || got.Intent.PR == nil || got.Intent.PR.Number != 1 || got.Intent.Ticket == nil {
		t.Fatalf("intent lost: %+v", got.Intent)
	}
	if got.Surfaces == nil || got.Surfaces.Interface == nil || !got.Surfaces.Interface.Moved {
		t.Fatalf("surfaces lost: %+v", got.Surfaces)
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

// A comment-loop agent may send explicit empty intent/surfaces objects rather
// than omitting the keys. An empty object carries no briefing content, so it
// keeps the previous value the same way an omitted key does.
func TestMergeKeepsEmptyIntentAndSurfaces(t *testing.T) {
	prev := &Review{
		Summary:  "What this change does.",
		Intent:   &Intent{PR: &IntentPR{Number: 1}, Ticket: &IntentTicket{ID: "REL-24"}},
		Surfaces: &Surfaces{Interface: &Surface{Line: "moved", Moved: true}},
	}
	next := &Review{
		Intent:   &Intent{},
		Surfaces: &Surfaces{},
		Findings: []Judgment{{File: "a.go", Message: "boom"}},
	}

	got := Merge(prev, next)
	if got.Intent == nil || got.Intent.PR == nil || got.Intent.PR.Number != 1 || got.Intent.Ticket == nil {
		t.Fatalf("empty intent object must keep previous: %+v", got.Intent)
	}
	if got.Surfaces == nil || got.Surfaces.Interface == nil || !got.Surfaces.Interface.Moved {
		t.Fatalf("empty surfaces object must keep previous: %+v", got.Surfaces)
	}
}

func TestMergePrefersTheNewReviewWhereItSpeaks(t *testing.T) {
	prev := &Review{
		Summary:  "old",
		Files:    []FileNote{{Path: "a.go", Summary: "old"}},
		Intent:   &Intent{PR: &IntentPR{Number: 1}, Ticket: &IntentTicket{ID: "REL-24"}},
		Surfaces: &Surfaces{Interface: &Surface{Line: "old line", Moved: true}},
	}
	next := &Review{
		Summary:  "new",
		Files:    []FileNote{{Path: "b.go", Summary: "new"}},
		Intent:   &Intent{PR: &IntentPR{Number: 2}},
		Surfaces: &Surfaces{API: &Surface{Line: "new line", Moved: true}},
	}

	got := Merge(prev, next)
	if got.Summary != "new" {
		t.Fatalf("got %q", got.Summary)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "b.go" {
		t.Fatalf("got %+v", got.Files)
	}
	// Whole-object replace, like Files: next's intent wins entirely, so the
	// previous ticket is dropped rather than deep-merged.
	if got.Intent == nil || got.Intent.PR == nil || got.Intent.PR.Number != 2 {
		t.Fatalf("intent should be next's: %+v", got.Intent)
	}
	if got.Intent.Ticket != nil {
		t.Fatalf("whole-object replace drops prev ticket: %+v", got.Intent.Ticket)
	}
	if got.Surfaces == nil || got.Surfaces.API == nil || got.Surfaces.API.Line != "new line" {
		t.Fatalf("surfaces should be next's: %+v", got.Surfaces)
	}
	if got.Surfaces.Interface != nil {
		t.Fatalf("whole-object replace drops prev interface surface: %+v", got.Surfaces.Interface)
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
