package scout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
)

func docTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func paths(found []docFile) []string {
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.Path
	}
	return out
}

// The rules nearest the change are the specific ones. A package with its own
// AGENTS.md has said something about that package, and a reviewer that only
// ever sees the root file will not know it.
func TestGuidelinesFindTheNearestRulesFirst(t *testing.T) {
	root := docTree(t, map[string]string{
		"AGENTS.md":                    "# House style\n",
		"internal/store/AGENTS.md":     "# Store package rules\n",
		"internal/unrelated/AGENTS.md": "# Rules for a package nobody touched\n",
		"internal/store/user.go":       "package store\n",
	})
	got := paths(guidelines(root, []string{"internal/store/user.go"}))
	if len(got) != 2 {
		t.Fatalf("found %v, want the root rules and the store package's", got)
	}
	if got[0] != "internal/store/AGENTS.md" {
		t.Errorf("first = %q; the rules nearest the change should come first", got[0])
	}
	for _, p := range got {
		if strings.Contains(p, "unrelated") {
			t.Errorf("%s belongs to a package this change does not touch", p)
		}
	}
}

func TestGuidelinesCoverTheUsualNames(t *testing.T) {
	root := docTree(t, map[string]string{
		"AGENTS.md":               "# a\n",
		"CLAUDE.md":               "# b\n",
		"CONTRIBUTING.md":         "# c\n",
		"CONVENTIONS.md":          "# d\n",
		".cursorrules":            "no em dashes\n",
		".github/CONTRIBUTING.md": "# e\n",
		"README.md":               "# not a rules file\n",
	})
	got := paths(guidelines(root, nil))
	if len(got) != 6 {
		t.Fatalf("found %v, want every conventions file and not the README", got)
	}
	for _, p := range got {
		if p == "README.md" {
			t.Error("the README was taken for a rules file")
		}
	}
}

// A repository with no rules gets no section, rather than an empty heading
// telling the scout to read nothing.
func TestNoRulesMeansNoSection(t *testing.T) {
	root := docTree(t, map[string]string{"main.go": "package main\n"})
	if got := guidelineBrief(root, guidelines(root, []string{"main.go"}), 150, 400); got != "" {
		t.Errorf("brief = %q, want nothing", got)
	}
}

// Short rules go in the opening turn: a round trip costs more than the couple
// of hundred tokens they take, and every change runs into them.
func TestShortRulesAreInlinedAndLongOnesAreListed(t *testing.T) {
	long := strings.Repeat("a rule nobody will read in full\n", 300)
	root := docTree(t, map[string]string{
		"AGENTS.md":       "# House style\n\nNo em dashes.\n",
		"CONTRIBUTING.md": long,
	})
	got := guidelineBrief(root, guidelines(root, nil), 150, 400)
	if !strings.Contains(got, "No em dashes.") {
		t.Errorf("the short rules file was not shown in full:\n%s", got)
	}
	// The listing names a file by its own first line, so one occurrence is
	// the heading. More than that means the body was inlined.
	if n := strings.Count(got, "a rule nobody will read in full"); n != 1 {
		t.Errorf("the 300-line file appears %d times; it should be listed by its heading, not inlined", n)
	}
	if !strings.Contains(got, "CONTRIBUTING.md (300 lines, read it if it bears on this change)") {
		t.Errorf("the long file was not offered to be read:\n%s", got)
	}
	if !strings.Contains(got, "AGENTS.md (3 lines, shown in full)") {
		t.Errorf("the short file was not inlined:\n%s", got)
	}
}

func TestListDocsNamesDocumentsByTheirOwnHeading(t *testing.T) {
	root := docTree(t, map[string]string{
		"docs/plans/graph.md":  "# A standing graph beside the per-change envelope\n\nbody\n",
		"internal/x.go":        "package x\n",
		"vendor/dep/README.md": "# vendored, not ours\n",
	})
	got := listDocs(root, 80)
	if len(got) != 1 {
		t.Fatalf("listed %v, want the one document that is ours", paths(got))
	}
	if got[0].Heading != "A standing graph beside the per-change envelope" {
		t.Errorf("heading = %q; the file's own words are what make a listing worth reading", got[0].Heading)
	}
}

// The reviewer contradicting the house style is the failure this role exists
// to stop, so a rule must not be the first thing the budget drops.
func TestAGuidelineOutranksALateNeighbor(t *testing.T) {
	var schema strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&schema, "CREATE TABLE t%d (id INT);\n", i)
	}
	root := docTree(t, map[string]string{
		"AGENTS.md":     "# Style\n\nNo em dashes.\n",
		"db/schema.sql": schema.String(),
	})
	r := newResolver(root, Limits{})
	// The scout finds code first and reads the rules last, which is the order
	// that would otherwise decide this.
	var records []record
	for i := 0; i < 30; i++ {
		records = append(records, record{
			Role: RoleNeighbor, File: "db/schema.sql", StartLine: i + 1, EndLine: i + 1,
			Symbol: fmt.Sprintf("t%d", i),
		})
	}
	records = append(records, record{
		Role: RoleGuideline, File: "AGENTS.md", StartLine: 3, EndLine: 3, Symbol: "No em dashes",
	})
	got := r.Expansions(records)
	if len(got) != 31 {
		t.Fatalf("got %d expansions, want all of them", len(got))
	}
	if got[len(got)-1].Role == RoleGuideline {
		t.Error("the rule sorted last, so the budget drops it first")
	}
	var guideline *envelope.Expansion
	beaten := 0
	for i := range got {
		if got[i].Role == RoleGuideline {
			guideline = &got[i]
			continue
		}
		if guideline != nil {
			beaten++
		}
	}
	if guideline == nil {
		t.Fatal("the rule did not survive")
	}
	if beaten == 0 {
		t.Errorf("the rule (priority %d) outranks nothing it was recorded after", guideline.Priority)
	}
}

func TestGuidelineIsARoleRedlineReportsRatherThanRanks(t *testing.T) {
	if _, known := RoleGuideline.Rank(); known {
		t.Error("guideline is in the vocabulary; the measurement that would justify it has not run")
	}
	r := newResolver(docTree(t, map[string]string{"AGENTS.md": "# Style\n\nNo em dashes.\n"}), Limits{})
	if err := r.validate(record{Role: RoleGuideline, File: "AGENTS.md", StartLine: 1, EndLine: 3}); err != nil {
		t.Fatalf("the role the scout is told to use was refused: %v", err)
	}
}

// The rules are quoted, not paraphrased: the same copying rule that applies to
// source applies to a style guide, and for the same reason.
func TestGuidelineContentIsQuotedFromTheFile(t *testing.T) {
	root := docTree(t, map[string]string{"AGENTS.md": "# Style\n\nNo em dashes. Use a comma.\n"})
	r := newResolver(root, Limits{})
	x, ok := r.resolve(record{
		Role: RoleGuideline, File: "AGENTS.md", StartLine: 3, EndLine: 3,
		Symbol: "the style rules, which mostly concern naming", FoundVia: "docs",
	}, 80)
	if !ok {
		t.Fatal("did not resolve")
	}
	if x.Content != "No em dashes. Use a comma.\n" {
		t.Errorf("content = %q, want the file's own line", x.Content)
	}
	if strings.Contains(x.Content, "naming") {
		t.Error("the scout's description of the rules reached the content")
	}
}
