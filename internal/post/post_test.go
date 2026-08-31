package post

import (
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/target"
)

func sampleReport() *findings.Report {
	rep := &findings.Report{
		Coverage: findings.Coverage{ChangedFiles: 5, ExaminedFiles: 3},
		Substrates: []findings.SubstrateStatus{
			{Name: "migrations", State: findings.SubstrateRan},
			{Name: "reviewer:claude", State: findings.SubstrateRan},
			{Name: "diff-coverage", State: findings.SubstrateSkipped},
		},
		Findings: []findings.Finding{
			{File: "a.go", Line: 12, Rule: "review", Substrate: "reviewer:claude",
				Severity: findings.SeverityError, Message: "nil deref", Source: findings.SourceLLM,
				Reviewer: "claude", Confidence: "high"},
			{Rule: "migration-edited", Substrate: "redline/sql",
				Severity: findings.SeverityWarning, Message: "migration edited after merge",
				Source: findings.SourceDeterministic},
		},
	}
	rep.Finalize()
	return rep
}

func prTarget() *target.Target {
	return &target.Target{Kind: target.KindPR, Head: "deadbeef",
		PR: &target.PullRequest{Number: 7, URL: "https://github.com/o/r/pull/7"}}
}

func TestBuildSplitsLocatedFromBodyFindings(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "https://ci/report.html")

	if p.CommitID != "deadbeef" {
		t.Fatalf("commit id should be the head SHA: %q", p.CommitID)
	}
	if len(p.Comments) != 1 {
		t.Fatalf("only the file:line finding is a line comment: %+v", p.Comments)
	}
	c := p.Comments[0]
	if c.Path != "a.go" || c.Line != 12 {
		t.Fatalf("comment anchor: %+v", c)
	}
	if !strings.Contains(c.Body, "nil deref") || !strings.Contains(c.Body, "claude") {
		t.Fatalf("comment body should name the finding and reviewer: %q", c.Body)
	}
	if c.Fingerprint == "" || !strings.Contains(c.Body, fpMarkerPrefix) {
		t.Fatalf("comment must carry its fingerprint marker: %q", c.Body)
	}
	if !Fingerprints([]string{c.Body})[c.Fingerprint] {
		t.Fatalf("the marker must decode back to this comment's fingerprint: %q", c.Body)
	}
	// The unlocated deterministic finding rides in the body, not as a comment.
	if !strings.Contains(p.Body, "migration edited after merge") {
		t.Fatalf("unlocated finding should be in the body: %q", p.Body)
	}
}

func TestBuildPreambleStatesCoverageAndProvenance(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "https://ci/report.html")

	for _, want := range []string{
		"Reviewed by claude.",    // reviewer that ran
		"does not gate",          // reports, never blocks
		"3 of 5 changed file(s)", // coverage numerator/denominator
		"1 error, 1 warning",     // finding counts
		"[Full report](https://ci/report.html)",
	} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, p.Body)
		}
	}
	// "what was not checked" must name the skipped pane.
	if !strings.Contains(p.Body, "diff-coverage") {
		t.Fatalf("body should list the skipped check:\n%s", p.Body)
	}
	// The head-SHA marker makes the summary idempotent per commit.
	if !strings.Contains(p.Body, reviewMarkerPrefix+"deadbeef") {
		t.Fatalf("body should carry the review marker:\n%s", p.Body)
	}
}

func TestBuildOmitsReportLinkWhenEmpty(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "")
	if strings.Contains(p.Body, "Full report") {
		t.Fatalf("no link should render when reportURL is empty:\n%s", p.Body)
	}
}

func TestUnpostedDropsAlreadyPostedComments(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "")
	if len(p.Comments) != 1 {
		t.Fatalf("precondition: one comment, got %d", len(p.Comments))
	}
	fp := p.Comments[0].Fingerprint

	// Nothing posted yet: the comment survives.
	if got := p.Unposted(map[string]bool{}); len(got.Comments) != 1 {
		t.Fatalf("unposted with empty set should keep the comment: %+v", got.Comments)
	}
	// Already posted: the comment is filtered, so a re-post never duplicates it.
	got := p.Unposted(map[string]bool{fp: true})
	if len(got.Comments) != 0 {
		t.Fatalf("already-posted comment must be dropped: %+v", got.Comments)
	}
	// The body is intact; the coverage summary is worth restating.
	if got.Body != p.Body {
		t.Fatal("Unposted must not alter the body")
	}
}

func TestFingerprintsRoundTripFromBodies(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "")
	bodies := []string{p.Comments[0].Body, "an unrelated human comment"}
	got := Fingerprints(bodies)
	if !got[p.Comments[0].Fingerprint] {
		t.Fatalf("fingerprint should be recovered from the comment body: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("no phantom fingerprints from plain text: %v", got)
	}
}

func TestReviewedAtMatchesHeadSHA(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "")
	if !ReviewedAt([]string{p.Body}, "deadbeef") {
		t.Fatal("should recognize the review marker for this head")
	}
	if ReviewedAt([]string{p.Body}, "cafef00d") {
		t.Fatal("a different head SHA is a new push, not already reviewed")
	}
	if ReviewedAt([]string{"no marker here"}, "deadbeef") {
		t.Fatal("no marker means not reviewed")
	}
	if ReviewedAt([]string{p.Body}, "") {
		t.Fatal("an empty head is never 'already reviewed'")
	}
}
