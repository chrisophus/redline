package findings

import "testing"

func TestCorrelationCommentBecomesItsOwnCategory(t *testing.T) {
	r := &Review{Comments: []ReviewComment{{
		File: "internal/store/user.go", Line: 12,
		Body:            "the migration adds a NOT NULL column and this field is a non-pointer with no default",
		Category:        CategoryCorrelation,
		RelatedFindings: []string{"fp-migration", "fp-apidelta"},
	}}}
	got := r.CommentFindings()
	if len(got) != 1 {
		t.Fatalf("comments = %d", len(got))
	}
	f := got[0]
	if f.Category != CategoryCorrelation || f.Rule != "correlation" {
		t.Fatalf("category/rule = %s/%s", f.Category, f.Rule)
	}
	if f.Severity != SeverityWarning {
		t.Fatalf("a cross-producer contradiction defaults above a remark, got %s", f.Severity)
	}
	if len(f.RelatedFindings) != 2 {
		t.Fatal("a correlation must reference the priors rather than restate them")
	}
	if f.Source != SourceLLM {
		t.Fatal("Redline attributes the reading, the file does not")
	}
}

func TestAReviewCannotLabelItsRemarkAsAMeasurement(t *testing.T) {
	r := &Review{Comments: []ReviewComment{{
		File: "a.go", Line: 1, Body: "x", Category: CategorySchema,
	}}}
	got := r.CommentFindings()
	if got[0].Category != CategoryReview {
		t.Fatalf("only correlation may be claimed, got %s", got[0].Category)
	}
}

func TestConfidenceIsNormalizedAndOnlyForReviewers(t *testing.T) {
	rep := &Report{Findings: []Finding{
		{Rule: "r", File: "a.go", Message: "m", Source: SourceLLM, Confidence: "SPECULATIVE"},
		{Rule: "r2", File: "a.go", Message: "m", Source: SourceLLM},
		{Rule: "r3", File: "a.go", Message: "m", Confidence: "high"},
	}}
	rep.Finalize()
	if rep.Findings[0].Confidence != ConfidenceLow {
		t.Fatalf("confidence[0] = %q, want low", rep.Findings[0].Confidence)
	}
	if rep.Findings[1].Confidence != ConfidenceMedium {
		t.Fatalf("an unstated confidence must still land in a bucket, got %q", rep.Findings[1].Confidence)
	}
	if rep.Findings[2].Confidence != "" {
		t.Fatal("a measurement carries no confidence, and a review file cannot smuggle one onto a pane's finding")
	}
}

func TestRelatedFindingsKeyOnTheFingerprintThatAlreadyExists(t *testing.T) {
	rep := &Report{Findings: []Finding{
		{Rule: "migration-not-null-no-default", File: "m.sql", Message: "adds NOT NULL"},
	}}
	rep.Finalize()
	prior := rep.Findings[0].Fingerprint
	if prior == "" {
		t.Fatal("wave one findings need a stable id for wave two to reference")
	}
	r := &Review{Comments: []ReviewComment{{
		File: "s.go", Line: 3, Body: "correlates", Category: CategoryCorrelation,
		RelatedFindings: []string{prior},
	}}}
	rep.Findings = append(rep.Findings, r.CommentFindings()...)
	rep.Finalize()
	if rep.Findings[1].RelatedFindings[0] != prior && rep.Findings[0].RelatedFindings == nil {
		// order is not guaranteed before Sort; find it
		var found bool
		for _, f := range rep.Findings {
			for _, id := range f.RelatedFindings {
				if id == prior {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("the reference did not survive Finalize")
		}
	}
}
