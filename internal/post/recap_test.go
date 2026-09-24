package post

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
)

func recapPayload(t *testing.T) Payload {
	t.Helper()
	return Payload{
		CommitID: "cccccccccccccccccccccccccccccccccccccccc",
		rep: &findings.Report{
			Agent: &findings.AgentReview{
				Overview: "This change batches the recompute.",
				Files:    map[string]string{"a.go": "first file", "b.go": "second file"},
				Recap:    "The transaction boundary moved inside the loop.",
			},
		},
		files: []change.File{
			{Path: "a.go", Language: "go", Added: 12, Removed: 3},
			{Path: "b.go", Language: "go", Added: 4, Removed: 1},
		},
		profile: &Profile{BodyStyle: BodyWalkthrough},
	}
}

// The recap replaces the overview, and the per-file list under it keeps only
// the files that moved since the last review. Repeating the whole walkthrough
// would leave the body longer than the one it exists to shorten.
func TestTheRecapReplacesTheWalkthrough(t *testing.T) {
	p := recapPayload(t)
	before := p.renderBody()
	for _, want := range []string{"What this change does", "batches the recompute", "first file"} {
		if !strings.Contains(before, want) {
			t.Fatalf("the ordinary body is missing %q, so this test is not comparing what it thinks", want)
		}
	}
	after := p.WithRecap("The transaction boundary moved inside the loop.", "abc1234def5678", []string{"b.go"}).Body
	if !strings.Contains(after, "### Since the last review (`abc1234def56`)") {
		t.Error("the recap body does not say what it is")
	}
	if !strings.Contains(after, "transaction boundary moved") {
		t.Error("the recap paragraph is missing")
	}
	for _, gone := range []string{"What this change does", "batches the recompute", "first file"} {
		if strings.Contains(after, gone) {
			t.Errorf("the recap body still repeats %q", gone)
		}
	}
	if !strings.Contains(after, "Files changed since `abc1234def56`") || !strings.Contains(after, "second file") {
		t.Errorf("the recap body should list the file that moved since the last review:\n%s", after)
	}
	if !strings.Contains(after, "abc1234def56") {
		t.Error("the recap body does not name the commit it is measured against")
	}

	// A session from before the moved files were stored cannot say which
	// files the recap covers, so it lists none rather than all of them.
	none := p.WithRecap("The transaction boundary moved inside the loop.", "abc1234def5678", nil).Body
	if strings.Contains(none, "first file") || strings.Contains(none, "second file") {
		t.Errorf("with no moved files known the recap body should list no files:\n%s", none)
	}
}

// A paragraph with no commit beside it cannot be read: "since the last
// review" means nothing without saying which one. Given either half alone the
// body is left as it was.
func TestARecapWithoutItsCommitChangesNothing(t *testing.T) {
	p := recapPayload(t)
	want := p.renderBody()
	for _, tc := range []struct{ recap, since string }{
		{"", "abc1234"},
		{"something changed", ""},
		{"   ", "abc1234"},
	} {
		if got := p.WithRecap(tc.recap, tc.since, []string{"a.go"}).renderBody(); got != want {
			t.Errorf("WithRecap(%q, %q) changed the body", tc.recap, tc.since)
		}
	}
}

// The commit a previous review ran against is in the marker that review
// already carries, so --recap does not have to be told it.
func TestTheHeadsOfEarlierReviewsComeOffTheirMarkers(t *testing.T) {
	first := marker(reviewMarkerPrefix + "aaaaaaaaaaaa")
	second := marker(reviewMarkerPrefix + "bbbbbbbbbbbb")
	got := ReviewedHeads([]string{"body one" + first, "body two" + second})
	if len(got) != 2 || got[0] != "aaaaaaaaaaaa" || got[1] != "bbbbbbbbbbbb" {
		t.Errorf("ReviewedHeads=%v, want both heads in order", got)
	}
	if n := len(ReviewedHeads([]string{"a body Redline never posted"})); n != 0 {
		t.Errorf("a body with no marker yielded %d head(s)", n)
	}
}

// A finding on a file the walkthrough would have shown rides in the per-file
// table, and the recap leaves unmoved files out of that table. It has to fall back to the flat
// list, or posting with --recap drops it from the body without saying so.
//
// This is the one thing the recap must not do: it exists to stop a walkthrough
// being repeated, not to stop a finding being read.
func TestTheRecapBodyStillCarriesAFindingOnAShownFile(t *testing.T) {
	p := recapPayload(t)
	p.bodyFindings = []findings.Finding{
		{File: "a.go", Line: 12, Rule: "shown-file", Severity: findings.SeverityWarning,
			Message: "this rides in the per-file table"},
		{Rule: "no-file", Severity: findings.SeverityWarning,
			Message: "this was always in the flat list"},
	}
	// The ordinary body puts the first one in the table, which is what makes
	// the recap body the interesting case.
	before := p.renderBody()
	if !strings.Contains(before, "this rides in the per-file table") {
		t.Fatal("the ordinary body does not carry the per-file finding, so this test is not comparing what it thinks")
	}
	// a.go did not move since the last review, so it is not listed and its
	// finding has no row to ride under.
	after := p.WithRecap("The transaction boundary moved inside the loop.", "abc1234def5678", []string{"b.go"}).Body
	if !strings.Contains(after, "this was always in the flat list") {
		t.Error("the recap body dropped a finding that never depended on the table")
	}
	if !strings.Contains(after, "this rides in the per-file table") {
		t.Error("the recap body dropped the finding on a shown file, which is the whole bug")
	}
}

// Only the walkthrough body has a walkthrough for the recap to replace, so on
// the evidence body WithRecap changes nothing. That is why cmdPost refuses
// --recap there rather than applying it: the flag would pass every check and
// then quietly make no difference to what gets posted.
func TestARecapDoesNothingToTheEvidenceBody(t *testing.T) {
	p := recapPayload(t)
	p.profile = &Profile{BodyStyle: BodyEvidence}
	after := p.WithRecap("The transaction boundary moved inside the loop.", "abc1234def5678", nil).Body
	if strings.Contains(after, "transaction boundary moved") || strings.Contains(after, "Since the last review") {
		t.Error("the evidence body rendered a recap, so cmdPost need not refuse one")
	}
}
