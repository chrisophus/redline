package testdelta

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/pane"
)

func TestSkipKind(t *testing.T) {
	const focus = "focuses tests, so the others in the file will not run"
	cases := []struct {
		line string
		lang string
		want string
	}{
		{"\tt.Skip(\"flaky\")", langGo, "skips a test"},
		{"    b.SkipNow()", langGo, "skips a test"},
		{"  it.skip('x', () => {})", langJS, "skips a test"},
		{"@pytest.mark.skip", langPy, "skips a test"},
		{"  self.skipTest('n')", langPy, "skips a test"},
		{"  xit('x', () => {})", langJS, "skips a test"},
		{"  describe.only('x', () => {})", langJS, focus},
		{"  fit('x', () => {})", langJS, focus},
		{"fit('x', () => {})", langJS, focus},
		{"xdescribe('x', () => {})", langJS, "skips a test"},
		{"  chart.fit(width, height)", langJS, ""},
		{"  const size = layout.fdescribe(node)", langJS, ""},
		{"  queue.xit(job)", langJS, ""},
		{"\treturn nil", langGo, ""},
		{"\tt.Errorf(\"boom\")", langGo, ""},
		{"\t// skip this comment mentions skip but is not a directive", langGo, ""},
		{"\t// t.Skip(\"disabled for now\") — left as a note", langGo, ""},
		{"  // it.skip('x', () => {})", langJS, ""},
		{"  # self.skipTest('n')", langPy, ""},
	}
	for _, c := range cases {
		if got := skipKind(c.line, c.lang); got != c.want {
			t.Errorf("skipKind(%q, %q) = %q, want %q", c.line, c.lang, got, c.want)
		}
	}
}

// The three findings this pane reported on redline's own PR were all the English
// word "fit" in a Go comment or string literal, caught by the Jasmine
// fit/fdescribe pattern. Each claimed the change focuses tests, which was
// categorically false.
func TestSkipKindIgnoresProse(t *testing.T) {
	prose := []string{
		"// to fit the ceiling one review is budgeted for.",
		"\t\tt.Fatal(\"context should have been trimmed to fit alongside the diff\")",
		"\t\tt.Fatalf(\"no context can fit, got room for %d tokens\", got.ContextRoom)",
	}
	for _, l := range prose {
		if got := skipKind(l, langGo); got != "" {
			t.Errorf("skipKind(%q, go) = %q, want none", l, got)
		}
	}
}

// Whether `fit` is a directive is decided by the path: a call in a .test.ts file,
// an ordinary word in Go.
func TestSkipKindLanguageGate(t *testing.T) {
	const line = "  fit('renders', () => {})"
	if got := skipKind(line, testLang("ui/src/a.test.ts")); !strings.HasPrefix(got, "focuses") {
		t.Errorf("skipKind(%q, .test.ts) = %q, want a focus", line, got)
	}
	if got := skipKind("\tfit := budget.fit(n)", testLang("internal/a/a_test.go")); got != "" {
		t.Errorf("fit in a Go test file = %q, want none", got)
	}
}

func TestIsTestFile(t *testing.T) {
	yes := []string{
		"internal/foo/foo_test.go",
		"ui/src/a.test.ts",
		"ui/src/a.spec.tsx",
		"py/test_x.py",
		"py/x_test.py",
		"ui/src/__tests__/a.js",
	}
	no := []string{"internal/foo/foo.go", "ui/src/a.ts", "README.md", "py/x.py"}
	for _, f := range yes {
		if !isTestFile(f) {
			t.Errorf("isTestFile(%q) = false, want true", f)
		}
	}
	for _, f := range no {
		if isTestFile(f) {
			t.Errorf("isTestFile(%q) = true, want false", f)
		}
	}
}

func TestAssertionRe(t *testing.T) {
	match := []string{
		"\tassert.Equal(t, a, b)",
		"\trequire.NoError(t, err)",
		"\texpect(x).toBe(1)",
		"\tself.assertEqual(a, b)",
		"\tt.Fatal(\"no\")",
	}
	none := []string{"\tx := 1", "\treturn nil", "\t// assert something"}
	for _, l := range match {
		if !assertionRe.MatchString(l) {
			t.Errorf("assertionRe should match %q", l)
		}
	}
	for _, l := range none {
		if assertionRe.MatchString(l) {
			t.Errorf("assertionRe should not match %q", l)
		}
	}
}

func TestCountAssertionsReadsWholeFileMarkers(t *testing.T) {
	deleted := "diff --git a/old_test.go b/old_test.go\ndeleted file mode 100644\n--- a/old_test.go\n+++ /dev/null\n@@ -1,3 +0,0 @@\n-func TestA(t *testing.T) {\n-\tassert.Equal(t, 1, 1)\n-}\n"
	created := "diff --git a/new_test.go b/new_test.go\nnew file mode 100644\n--- /dev/null\n+++ b/new_test.go\n@@ -0,0 +1,3 @@\n+func TestA(t *testing.T) {\n+\tassert.Equal(t, 1, 1)\n+}\n"
	if got := countAssertions("old_test.go", deleted); !got.deleted || got.created || got.removed != 1 || got.added != 0 {
		t.Errorf("deleted file counted as %+v", got)
	}
	if got := countAssertions("new_test.go", created); !got.created || got.deleted || got.added != 1 || got.removed != 0 {
		t.Errorf("created file counted as %+v", got)
	}
}

// ChangedPaths has no rename detection, so a moved test arrives as a deleted
// path and a created one. The move must not read as a loss, and a real drop in
// a file that only changed must not be hidden by assertions a new file adds.
func TestAssertionLosses(t *testing.T) {
	paths := func(fs []testAssertions) string {
		var out []string
		for _, f := range fs {
			out = append(out, f.path)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		name  string
		files []testAssertions
		want  string
	}{
		{"rename", []testAssertions{
			{path: "old_test.go", removed: 2, deleted: true},
			{path: "new_test.go", added: 2, created: true},
		}, ""},
		{"split into two files", []testAssertions{
			{path: "all_test.go", removed: 4, deleted: true},
			{path: "a_test.go", added: 2, created: true},
			{path: "b_test.go", added: 2, created: true},
		}, ""},
		{"rename that also drops assertions", []testAssertions{
			{path: "old_test.go", removed: 3, deleted: true},
			{path: "new_test.go", added: 1, created: true},
		}, "old_test.go"},
		{"deleted with nothing replacing it", []testAssertions{
			{path: "old_test.go", removed: 2, deleted: true},
		}, "old_test.go"},
		{"drop in a changed file beside a new test", []testAssertions{
			{path: "a_test.go", added: 1, removed: 3},
			{path: "b_test.go", added: 5, created: true},
		}, "a_test.go"},
	}
	for _, c := range cases {
		if got := paths(assertionLosses(c.files)); got != c.want {
			t.Errorf("%s: losses = %q, want %q", c.name, got, c.want)
		}
	}
}

// A Go package whose source changed with no test change in it is reported once;
// a package that also changed a test is not.
func TestSourceWithoutTest(t *testing.T) {
	p := &Pane{changed: []string{
		"internal/a/a.go",
		"internal/b/b.go",
		"internal/b/b_test.go",
		"README.md",
	}}
	var res pane.Result
	var lines []string
	p.sourceWithoutTest(&res, &lines)
	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Rule != "source-without-test" || f.Anchor == nil || f.Anchor.ID != "internal/a" {
		t.Fatalf("finding = %+v (anchor %+v)", f, f.Anchor)
	}
}

// A change with no Go file in it never reached the source-without-test check, so
// the confirmation must not report that every changed package changed a test.
func TestConfirmationWithoutGoOmitsPackageClause(t *testing.T) {
	p := &Pane{}
	p.Scope([]string{"ui/src/app.ts", "ui/src/util.ts"})
	res, err := p.Diff(&observation{Rev: "base"}, &observation{Rev: "head"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Confirmations) != 1 {
		t.Fatalf("want 1 confirmation, got %+v", res.Confirmations)
	}
	got := res.Confirmations[0].Message
	if got != "no test was skipped or focused, and no assertions were removed" {
		t.Errorf("confirmation = %q, want only the checks that ran", got)
	}
}
