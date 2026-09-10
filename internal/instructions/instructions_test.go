package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".redline.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A rule the team wrote for particular paths reaches a review of those paths
// and no others. That scoping is why the rule can be specific enough to be
// worth stating.
func TestARuleReachesOnlyTheChangesItIsAbout(t *testing.T) {
	root := t.TempDir()
	write(t, root, `
review:
  instructions:
    - scope: ["internal/feed/**"]
      text: Bulk ingest uses CopyFrom with an explicit column list.
`)
	env, err := Resolve(root, []string{"internal/feed/offer.go"})
	if err != nil {
		t.Fatal(err)
	}
	if env == nil || len(env.Expansions) != 1 {
		t.Fatalf("the rule did not reach a change it governs: %+v", env)
	}
	if !strings.Contains(env.Expansions[0].Content, "CopyFrom") {
		t.Fatalf("content = %q", env.Expansions[0].Content)
	}

	away, err := Resolve(root, []string{"cmd/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if away != nil {
		t.Fatal("a rule about the feed package reached a change that touches none of it")
	}
}

// The guard on the whole idea. A learnings file grows from moments when
// somebody was defending their own change, so it must never be able to
// overrule what the team sat down and decided.
func TestALearnedRuleRanksBelowAWrittenOne(t *testing.T) {
	root := t.TempDir()
	write(t, root, `
review:
  instructions:
    - text: somebody said this once on a thread
      learned_from: "https://github.com/o/r/pull/1#discussion_r1"
    - text: the team decided this deliberately
`)
	env, err := Resolve(root, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Expansions) != 2 {
		t.Fatalf("want both rules, got %+v", env.Expansions)
	}
	first, second := env.Expansions[0], env.Expansions[1]
	if !strings.Contains(first.Content, "decided this deliberately") {
		t.Fatal("the team's own decision has to be read first")
	}
	if first.Priority <= second.Priority {
		t.Fatalf("a learned rule outranks a written one: %d vs %d", first.Priority, second.Priority)
	}
	if second.Details["source"] != "learned" {
		t.Fatalf("provenance = %v, want the rule marked as learned", second.Details)
	}
	if second.Details["learnedFrom"] == "" {
		t.Fatal("a learned rule without its thread cannot be checked by anyone")
	}
}

// The reader has to be told the difference, or a thing one person said while
// explaining their own change reads as the team's policy.
func TestTheFragmentSeparatesADecisionFromAnAccountOfOne(t *testing.T) {
	root := t.TempDir()
	write(t, root, "review:\n  instructions:\n    - text: x\n")
	env, err := Resolve(root, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	// The constant is wrapped prose, so the assertion reads it the way a
	// reader does rather than the way the source file happens to break lines.
	flat := strings.Join(strings.Fields(env.PromptFragment), " ")
	for _, want := range []string{
		"one person's account",
		"never outranks a rule the team wrote in a file",
		"contradicts a learned rule, say so",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("the fragment is missing %q", want)
		}
	}
}

// A rule with no words is not a rule, and carrying it would make the report
// claim a rule was read.
func TestARuleWithNoTextIsAnError(t *testing.T) {
	root := t.TempDir()
	write(t, root, "review:\n  instructions:\n    - scope: [\"**\"]\n      text: \"  \"\n")
	if _, err := Resolve(root, []string{"a.go"}); err == nil {
		t.Fatal("an empty rule should be reported, not carried")
	}
}

// A learnings list grows by a line every time somebody dismisses a finding, so
// it grows without anyone deciding to let it. What is cut is the learned half,
// and it is counted.
func TestTooManyRulesKeepsTheWrittenOnes(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("review:\n  instructions:\n")
	for i := 0; i < maxRules+5; i++ {
		b.WriteString("    - text: learned thing\n      learned_from: \"t\"\n")
	}
	b.WriteString("    - text: the team's own rule\n")
	write(t, root, b.String())

	env, err := Resolve(root, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Expansions) != maxRules {
		t.Fatalf("carried %d rules, want the bound of %d", len(env.Expansions), maxRules)
	}
	if !strings.Contains(env.Expansions[0].Content, "the team's own rule") {
		t.Fatal("the written rule is the last thing to drop, not the first")
	}
	if len(env.Notes) == 0 || !strings.Contains(env.Notes[0], "not carried") {
		t.Fatalf("the drop has to be visible: %v", env.Notes)
	}
}

// A repository that has configured nothing is the common case.
func TestNoRulesIsNotAnError(t *testing.T) {
	env, err := Resolve(t.TempDir(), []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("want nil, got %+v", env)
	}
}
