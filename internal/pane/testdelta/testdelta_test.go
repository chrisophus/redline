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
