package change

import "testing"

func TestCompositionGroupsByLanguageAndKind(t *testing.T) {
	files := []File{
		{Path: "internal/change/change.go", Language: "go", Added: 20, Removed: 3},
		{Path: "internal/change/change_test.go", Language: "go", Added: 80, Removed: 0},
		{Path: ".golangci.yml", Language: "yaml", Added: 39, Removed: 0},
		{Path: "README.md", Added: 5, Removed: 1},
	}
	rows := Composition(files)

	byKind := map[string]LinesRow{}
	for _, r := range rows {
		byKind[r.Language+"/"+r.Kind] = r
	}

	src := byKind["go/source"]
	if src.Files != 1 || src.Added != 20 || src.Removed != 3 {
		t.Errorf("go/source = %+v, want 1 file, +20/-3", src)
	}
	test := byKind["go/test"]
	if test.Files != 1 || test.Added != 80 {
		t.Errorf("go/test = %+v, want 1 file, +80", test)
	}
	cfg := byKind["yaml/config"]
	if cfg.Files != 1 || cfg.Added != 39 {
		t.Errorf("yaml/config = %+v, want 1 file, +39", cfg)
	}
	docs := byKind["other/docs"]
	if docs.Files != 1 || docs.Added != 5 {
		t.Errorf("other/docs = %+v, want 1 file, +5 (no Language set -> \"other\")", docs)
	}
}

// The row with the most added lines leads: a reviewer scanning the table
// sees the dominant part of the change first, not an alphabetical shuffle.
func TestCompositionSortsByAddedLinesDescending(t *testing.T) {
	files := []File{
		{Path: "a.go", Language: "go", Added: 5},
		{Path: "b_test.go", Language: "go", Added: 500},
		{Path: "c.md", Added: 50},
	}
	rows := Composition(files)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %+v", rows)
	}
	if rows[0].Kind != "test" || rows[0].Added != 500 {
		t.Errorf("row 0 must be the largest addition: %+v", rows[0])
	}
	if rows[len(rows)-1].Kind != "source" || rows[len(rows)-1].Added != 5 {
		t.Errorf("row last must be the smallest addition: %+v", rows[len(rows)-1])
	}
}

func TestKindClassification(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"internal/foo/bar_test.go", KindTest},
		{"web/src/App.spec.ts", KindTest},
		{"testdata/fixture.go", KindTest},
		{"README.md", KindDocs},
		{"docs/design.md", KindDocs},
		{"CHANGELOG.md", KindDocs},
		{".golangci.yml", KindConfig},
		{"Makefile", KindConfig},
		{"go.mod", KindConfig},
		{"package.json", KindConfig},
		{".gitignore", KindConfig},
		{"internal/change/change.go", KindSource},
		{"cmd/redline/main.go", KindSource},
	}
	for _, tc := range cases {
		if got := kind(tc.path); got != tc.want {
			t.Errorf("kind(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// Generated files are filtered out of the change before Build ever sees
// them (run.go), so Composition never needs its own generated bucket — an
// empty input, the shape a change with only generated files reduces to,
// must not panic or report a phantom row.
func TestCompositionEmptyInput(t *testing.T) {
	if rows := Composition(nil); len(rows) != 0 {
		t.Errorf("expected no rows for no files, got %+v", rows)
	}
}

// Holding test bodies back from a review is a bargain with a reason: coverage
// and mutation already answer whether the tests assert enough. None of that
// touches a Markdown file that happens to live under testdata, and this
// repository's own fixture README reached a review labelled as a test file
// that had "moved", with the review told to judge the code it tests.
func TestDocsUnderTestdataAreNotTestCode(t *testing.T) {
	const doc = "testdata/fixtures/README.md"
	if !IsTest(doc) {
		t.Fatal("the composition table still counts it as test material, by where it lives")
	}
	if IsTestCode(doc) {
		t.Fatal("prose is what a reviewer should read; it must not be held back as a test body")
	}
	if kind(doc) != KindTest {
		t.Fatalf("kind = %q; the table's own reading is unchanged", kind(doc))
	}
}

// The narrowing is only about prose. Real test code stays held back, which is
// the whole point of the exclusion.
func TestRealTestCodeIsStillHeldBack(t *testing.T) {
	for _, p := range []string{
		"internal/review/review_test.go",
		"testdata/fixtures/golden.json",
		"ui/src/app.spec.ts",
		"tests/e2e/login.py",
	} {
		if !IsTestCode(p) {
			t.Errorf("%s should still be held back from the review request", p)
		}
	}
}
