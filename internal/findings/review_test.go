package findings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// The category an agent hand-writes is prose rather than an enum: SKILL.md documents a
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

// A comment on a removed line carries side LEFT, and CommentFindings has to
// carry it onto the finding: the post layer reads it there to anchor the
// comment on the old file rather than the new-file line of the same number.
// Case is forgiven, and an unnamed side reads as empty so the post layer can
// default it to the new file.
func TestCommentFindingsCarriesSide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	data := `{"comments": [
  {"file": "a.go", "line": 3, "side": "left", "body": "this deleted guard mattered"},
  {"file": "b.go", "line": 4, "body": "on the new file"}
]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := findings.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	fs := r.CommentFindings()
	if len(fs) != 2 {
		t.Fatalf("want two findings, got %d", len(fs))
	}
	if fs[0].Side != "LEFT" {
		t.Fatalf("left side lost or not normalized: %q", fs[0].Side)
	}
	if fs[1].Side != "" {
		t.Fatalf("an unnamed side must stay empty, got %q", fs[1].Side)
	}
}

// The note a review was given is kept in review.json and read back trimmed,
// so the report can show what the reviewer was pointed to.
func TestReviewKeepsTheNoteItWasGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(`{"overview":"o","note":"  Check the cache key.\n"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rev, err := findings.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Note != "Check the cache key." {
		t.Errorf("note = %q", rev.Note)
	}
}

// Recap survives a round trip through review.json. LoadReview builds a Review
// field by field from a wire struct rather than unmarshalling into one, so a
// field added to Review without a line here is read back as empty. That is
// how the recap reached the report as "" after the describing call had
// written it.
func TestRecapSurvivesAReviewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	body := `{"overview":"what it does","recap":"  what moved since  ","recapSince":"abc123",` +
		`"recapFiles":["a.go"],"files":{"a.go":"first"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := findings.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Recap != "what moved since" {
		t.Errorf("Recap read back as %q", got.Recap)
	}
	// The commit and files travel with it, or post cannot say what the recap
	// measures from.
	if got.RecapSince != "abc123" || len(got.RecapFiles) != 1 || got.RecapFiles[0] != "a.go" {
		t.Errorf("RecapSince=%q RecapFiles=%v, want abc123 and [a.go]", got.RecapSince, got.RecapFiles)
	}
}
