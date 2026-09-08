package findings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// The category an agent hand-writes is prose, not an enum: SKILL.md documents a
// review.json written by hand, and "Correlation" is how a sentence spells it.
// Taken verbatim it matched nothing, so the one finding the second wave exists
// to produce arrived as an ordinary remark: rule agent-comment, category
// review, severity info. Case is forgiven here the way it is for severity and
// confidence.
func TestLoadReviewForgivesCategoryCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	data := `{
  "comments": [
    {"file": "internal/store/user.go", "line": 8, "category": "Correlation",
     "body": "the migration makes tenant_id NOT NULL and this field has no default",
     "relatedFindings": ["f2076e74fbd"]}
  ]
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := findings.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Comments) != 1 || r.Comments[0].Category != findings.CategoryCorrelation {
		t.Fatalf("comments = %+v", r.Comments)
	}
	fs := r.CommentFindings()
	if len(fs) != 1 {
		t.Fatalf("findings = %+v", fs)
	}
	if fs[0].Category != findings.CategoryCorrelation || fs[0].Rule != "correlation" {
		t.Fatalf("category/rule = %s/%s", fs[0].Category, fs[0].Rule)
	}
	if fs[0].Severity != findings.SeverityWarning {
		t.Fatalf("a correlation defaults above a remark, got %s", fs[0].Severity)
	}
}

// A category nobody accepts falls back rather than flowing through: only
// correlation may be claimed, so a reviewer labelling its own remark reads as
// the review category it would have had anyway.
func TestLoadReviewFallsBackOnAnUnknownCategory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	data := `{"comments": [{"file": "a.go", "line": 1, "category": "vibes", "body": "x"}]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := findings.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Comments[0].Category != findings.CategoryReview {
		t.Fatalf("category = %q", r.Comments[0].Category)
	}
}
