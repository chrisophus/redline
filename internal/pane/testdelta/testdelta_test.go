package testdelta

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/pane"
)

func TestSkipKind(t *testing.T) {
	skips := []string{
		"\tt.Skip(\"flaky\")",
		"    b.SkipNow()",
		"  it.skip('x', () => {})",
		"@pytest.mark.skip",
		"  self.skipTest('n')",
		"  xit('x', () => {})",
	}
	focus := []string{
		"  describe.only('x', () => {})",
		"  fit('x', () => {})",
	}
	none := []string{
		"\treturn nil",
		"\tt.Errorf(\"boom\")",
		"\t// skip this comment mentions skip but is not a directive",
	}
	for _, l := range skips {
		if got := skipKind(l); got != "skips a test" {
			t.Errorf("skipKind(%q) = %q, want a skip", l, got)
		}
	}
	for _, l := range focus {
		if got := skipKind(l); !strings.HasPrefix(got, "focuses") {
			t.Errorf("skipKind(%q) = %q, want a focus", l, got)
		}
	}
	for _, l := range none {
		if got := skipKind(l); got != "" {
			t.Errorf("skipKind(%q) = %q, want none", l, got)
		}
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
