package houserules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func find(t *testing.T, root string, changed []string, path string) (found bool, content string) {
	t.Helper()
	env, err := Resolve(root, changed)
	if err != nil {
		t.Fatal(err)
	}
	if env == nil {
		return false, ""
	}
	for _, x := range env.Expansions {
		if x.File == path {
			return true, x.Content
		}
	}
	return false, ""
}

// A repository with nothing written down is not a failure to look. It has to
// be distinguishable from a read that broke, because the report says which.
func TestNoInstructionFilesIsNotAnError(t *testing.T) {
	env, err := Resolve(t.TempDir(), []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("an empty repository produced an envelope: %+v", env)
	}
}

// The point of applyTo is that a rule scoped to paths this change does not
// touch is not carried. Carrying it spends the budget on a rule that cannot
// bear on the review, and invites a finding about code that did not change.
func TestApplyToDecidesWhetherARuleIsCarried(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/instructions/go.md", "---\napplyTo: \"**/*.go\"\n---\nNever return a bare error.\n")
	write(t, root, ".github/instructions/sql.md", "---\napplyTo: 'migrations/**'\n---\nEvery migration needs a down.\n")

	if ok, _ := find(t, root, []string{"internal/a.go"}, ".github/instructions/go.md"); !ok {
		t.Error("the Go rule was not carried for a Go change")
	}
	if ok, _ := find(t, root, []string{"internal/a.go"}, ".github/instructions/sql.md"); ok {
		t.Error("the migration rule was carried for a change that touches no migration")
	}
	if ok, _ := find(t, root, []string{"migrations/001.sql"}, ".github/instructions/sql.md"); !ok {
		t.Error("the migration rule was not carried for a migration change")
	}
}

// The repository-wide file has no applyTo and governs every change, including
// one that matches no scoped rule at all.
func TestTheRepositoryWideFileAlwaysApplies(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/copilot-instructions.md", "# House rules\n\nNo em dashes in prose.\n")
	write(t, root, ".github/instructions/go.md", "---\napplyTo: \"**/*.go\"\n---\nGo rule.\n")

	ok, content := find(t, root, []string{"README.md"}, ".github/copilot-instructions.md")
	if !ok {
		t.Fatal("the repository-wide rules were not carried")
	}
	if !strings.Contains(content, "No em dashes") {
		t.Errorf("the rule's text did not reach the expansion: %q", content)
	}
	// Frontmatter is metadata about when to read the file, not part of the
	// rule, and sending it spends budget on YAML.
	if strings.Contains(content, "applyTo") {
		t.Error("frontmatter was carried into the content")
	}
	if ok, _ := find(t, root, []string{"README.md"}, ".github/instructions/go.md"); ok {
		t.Error("a Go-scoped rule was carried for a change with no Go in it")
	}
}

// A scoped file with no applyTo is a rule with no stated scope, which is a
// rule for the repository. Dropping it would silently lose a file the author
// put in the directory on purpose.
func TestAScopedFileWithoutApplyToStillApplies(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/instructions/all.md", "---\ndescription: Everything\n---\nOne rule for all.\n")
	if ok, _ := find(t, root, []string{"README.md"}, ".github/instructions/all.md"); !ok {
		t.Error("a rule with no applyTo was not carried")
	}
}

// An empty file is not a rule. Carrying it makes the report claim a rule was
// read and gives the model a heading with nothing under it.
func TestAnEmptyRuleFileIsNotCarried(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/copilot-instructions.md", "---\napplyTo: \"**\"\n---\n\n   \n")
	env, err := Resolve(root, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("an empty rule file produced an envelope: %+v", env)
	}
}

// The envelope has to satisfy the contract every provider's does, because
// Redline budgets and renders it through the same path. The role is
// deliberately one Redline does not rank, and that must stay visible rather
// than being quietly promoted.
func TestTheEnvelopeIsOneRedlineCanSpend(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/copilot-instructions.md", "# Rules\n\nBe terse.\n")
	env, err := Resolve(root, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("the envelope is not spendable: %v", err)
	}
	if got := env.UnknownRoles(); len(got) != 1 || got[0] != string(RoleGuideline) {
		t.Errorf("UnknownRoles() = %v, want [%s]", got, RoleGuideline)
	}
	// The header names an unranked role and leaves the description to the
	// provider, so a fragment that does not describe it leaves the model with
	// a label and no meaning.
	if !strings.Contains(env.PromptFragment, string(RoleGuideline)) {
		t.Error("the prompt fragment does not describe the guideline role")
	}
	if env.Expansions[0].Priority != priority {
		t.Errorf("priority = %d, want %d: an unranked role sorts on priority alone",
			env.Expansions[0].Priority, priority)
	}
}

// A rule too long to carry is carried up to the bound and says so. Silent
// truncation of a rule is the worst case: half a rule reads as the whole one.
func TestALongRuleIsBoundedAndSaysSo(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".github/copilot-instructions.md", strings.Repeat("a rule line\n", maxLines+50))
	env, err := Resolve(root, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	x := env.Expansions[0]
	if got := strings.Count(x.Content, "\n") + 1; got != maxLines {
		t.Errorf("carried %d lines, want %d", got, maxLines)
	}
	if x.Details["span"] != "truncated" {
		t.Errorf("a truncated rule does not say so: %v", x.Details)
	}
}

// The failure this was built for. Field use produced findings the author
// dismissed for contradicting rules the team had written in CLAUDE.md, and
// nothing read that file unless the scout was on, which it is not by default.
func TestTheConventionFileAtTheRootIsCarried(t *testing.T) {
	root := t.TempDir()
	write(t, root, "CLAUDE.md", "# House rules\n\nBulk ingest uses CopyFrom with an explicit column list.\n")

	found, content := find(t, root, []string{"internal/feed/offer.go"}, "CLAUDE.md")
	if !found {
		t.Fatal("a root CLAUDE.md governs every change and was not carried")
	}
	if !strings.Contains(content, "CopyFrom") {
		t.Fatalf("the rule's own words have to reach the reviewer: %q", content)
	}
}

// A rule beside the changed code is the one a review of that code is most
// likely to contradict, and it is the one a reviewer reading only the root
// file will never know about.
func TestANestedConventionFileIsScopedToItsDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/feed/AGENTS.md", "# Feed\n\nrow_index is zero-based here.\n")

	if found, _ := find(t, root, []string{"internal/feed/offer.go"}, "internal/feed/AGENTS.md"); !found {
		t.Fatal("a change inside the directory must carry its rule")
	}
	if found, _ := find(t, root, []string{"cmd/main.go"}, "internal/feed/AGENTS.md"); found {
		t.Fatal("a change elsewhere must not; the rule was written about that directory")
	}
}

// The scope reaches the reader, because a block that says "this is the rule
// for internal/feed" is a different claim from one that governs everything.
func TestANestedRuleSaysWhatItGoverns(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/feed/AGENTS.md", "# Feed\n\nzero-based\n")
	env, err := Resolve(root, []string{"internal/feed/offer.go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range env.Expansions {
		if x.File == "internal/feed/AGENTS.md" {
			if x.Details["scope"] != "internal/feed/**" {
				t.Fatalf("scope = %q, want the directory it was written about", x.Details["scope"])
			}
			return
		}
	}
	t.Fatal("the nested rule was not carried")
}

// A rule on the way from the root to the changed file governs it. An agent
// walking up from a file reads every one of these, and a reviewer that read
// only the two ends would miss the middle.
func TestConventionsOnTheWalkUpAreAllCarried(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# Root\n\nno em dashes\n")
	write(t, root, "internal/AGENTS.md", "# Internal\n\npackages own their errors\n")
	write(t, root, "internal/feed/AGENTS.md", "# Feed\n\nzero-based row_index\n")

	env, err := Resolve(root, []string{"internal/feed/offer.go"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range env.Expansions {
		got = append(got, x.File)
	}
	want := []string{"AGENTS.md", "internal/AGENTS.md", "internal/feed/AGENTS.md"}
	if len(got) != len(want) {
		t.Fatalf("carried %v, want all three", got)
	}
	// Outermost first: the repository-wide rule is the frame a nested one
	// narrows, so a reader meets the general statement before its exception.
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want outermost first %v", got, want)
		}
	}
}

// A monorepo where every package writes rules would otherwise spend the whole
// guideline budget on a wide change. What is cut is counted, because a rule
// that was found and dropped is not the same as one that does not exist.
func TestTooManyConventionFilesKeepsTheNearestAndSaysSo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# Root\n\ngeneral\n")
	var changed []string
	for _, pkg := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		write(t, root, "internal/"+pkg+"/AGENTS.md", "# "+pkg+"\n\nrule for "+pkg+"\n")
		changed = append(changed, "internal/"+pkg+"/x.go")
	}

	env, err := Resolve(root, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Expansions) != maxConventionFiles {
		t.Fatalf("carried %d rules, want the bound of %d", len(env.Expansions), maxConventionFiles)
	}
	for _, x := range env.Expansions {
		if x.File == "AGENTS.md" {
			t.Fatal("the root file is the one to drop; the nearest have something specific to say")
		}
	}
	if len(env.Notes) == 0 || !strings.Contains(env.Notes[0], "not carried") {
		t.Fatalf("the drop has to be visible: %v", env.Notes)
	}
}

// One file reached from two changed paths is one rule. Carrying it twice
// would spend the budget on a copy and read as two rules that agree.
func TestOneConventionFileIsCarriedOnce(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/feed/AGENTS.md", "# Feed\n\nzero-based\n")

	env, err := Resolve(root, []string{"internal/feed/a.go", "internal/feed/b.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Expansions) != 1 {
		t.Fatalf("carried %d copies of one rule", len(env.Expansions))
	}
}
