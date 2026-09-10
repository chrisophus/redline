package precedent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func resolve(t *testing.T, root string, changed ...string) map[string]string {
	t.Helper()
	env, err := Resolve(root, changed)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	if env == nil {
		return out
	}
	for _, x := range env.Expansions {
		out[x.File] = x.Content
	}
	return out
}

// The case this package exists for. A new staging file beside an established
// one: the reviewer flagged three choices the neighbour had already made, and
// the author dismissed all three because the neighbour had made them.
func TestTheFileBesideTheChangedOneIsCarried(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/feed/aws_account_feed.go", "package feed\n\n// CopyFrom with an explicit column list.\n")
	write(t, root, "internal/feed/aws_offer_feed.go", "package feed\n")

	got := resolve(t, root, "internal/feed/aws_offer_feed.go")
	content, ok := got["internal/feed/aws_account_feed.go"]
	if !ok {
		t.Fatalf("the established sibling was not carried: %v", got)
	}
	if !strings.Contains(content, "column list") {
		t.Fatalf("the neighbour's own code has to reach the reviewer: %q", content)
	}
}

// Two names that share only their extension are not doing the same work. A
// package of one-word filenames must produce nothing at all.
func TestSingleWordNamesAreNotSiblings(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/pane/delta.go", "package pane\n")
	write(t, root, "internal/pane/config.go", "package pane\n")
	write(t, root, "internal/pane/tools.go", "package pane\n")

	if got := resolve(t, root, "internal/pane/delta.go"); len(got) != 0 {
		t.Fatalf("nothing here is a precedent for anything: %v", got)
	}
}

// The guard that makes the heuristic safe. When a token is the directory's own
// vocabulary rather than a shape two files share, picking one of the twenty
// files carrying it would be picking arbitrarily, so nothing is claimed.
func TestADirectoryThatSinglesNothingOutSaysNothing(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"user", "order", "offer", "account", "tenant"} {
		write(t, root, "internal/api/"+n+"_handler.go", "package api\n")
	}
	if got := resolve(t, root, "internal/api/user_handler.go"); len(got) != 0 {
		t.Fatalf("four equally good candidates single nothing out: %v", got)
	}
}

// Three is the bound, so a shape shared by a small group still resolves: those
// three do mean something about each other.
func TestASmallFamilyStillResolves(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/api/user_handler.go", "package api\n")
	write(t, root, "internal/api/order_handler.go", "package api // order\n")
	write(t, root, "internal/api/offer_handler.go", "package api\n")

	got := resolve(t, root, "internal/api/user_handler.go")
	if len(got) != 1 {
		t.Fatalf("one precedent per changed file: %v", got)
	}
}

// A sibling already in the diff is already in front of the reviewer. Sending
// it again spends the ceiling to repeat what was shown.
func TestASiblingInsideTheChangeIsNotSentTwice(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/feed/aws_account_feed.go", "package feed\n")
	write(t, root, "internal/feed/aws_offer_feed.go", "package feed\n")

	got := resolve(t, root, "internal/feed/aws_offer_feed.go", "internal/feed/aws_account_feed.go")
	if len(got) != 0 {
		t.Fatalf("both files are in the diff already: %v", got)
	}
}

// Redline holds test bodies back from the review, so a precedent that is a
// test is budget spent on something nobody will read.
func TestTestFilesAreNeitherAskedAboutNorOffered(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/feed/aws_account_feed.go", "package feed\n")
	write(t, root, "internal/feed/aws_account_feed_test.go", "package feed\n")
	write(t, root, "internal/feed/aws_offer_feed_test.go", "package feed\n")

	if got := resolve(t, root, "internal/feed/aws_offer_feed_test.go"); len(got) != 0 {
		t.Fatalf("a changed test needs no precedent: %v", got)
	}
	got := resolve(t, root, "internal/feed/aws_offer_feed.go")
	for p := range got {
		if strings.Contains(p, "_test.go") {
			t.Fatalf("a test must not be offered as precedent: %v", got)
		}
	}
}

// One shared part out of five is two files that happen to mention the same
// word. The bar scales with the shorter name rather than sitting at a
// constant.
func TestOneSharedPartOfManyIsNotEnough(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/x/aws_offer_amend_mutability_check.go", "package x\n")
	write(t, root, "internal/x/aws_tenant_billing_window_reset.go", "package x\n")

	if got := resolve(t, root, "internal/x/aws_offer_amend_mutability_check.go"); len(got) != 0 {
		t.Fatalf("sharing only the vendor prefix is not kinship: %v", got)
	}
}

// The provenance has to reach the reader. A block of real source under a role
// reads as an established fact, and the claim that these two files are related
// is a guess about a filename.
func TestTheExpansionSaysItWasMatchedByName(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/feed/aws_account_feed.go", "package feed\n")
	write(t, root, "internal/feed/aws_offer_feed.go", "package feed\n")

	env, err := Resolve(root, []string{"internal/feed/aws_offer_feed.go"})
	if err != nil {
		t.Fatal(err)
	}
	x := env.Expansions[0]
	if x.Details["found_via"] != "name" {
		t.Fatalf("provenance = %v, want a name match", x.Details)
	}
	if x.Details["beside"] != "internal/feed/aws_offer_feed.go" {
		t.Fatalf("the expansion has to say which changed file it is for: %v", x.Details)
	}
	if !strings.Contains(env.PromptFragment, "guess") {
		t.Fatal("the fragment must not present a filename guess as a resolved fact")
	}
}

// A wide change must not put a whole file in front of the reviewer for every
// path it touches. What is cut is counted.
func TestTheEnvelopeIsBoundedAndSaysWhatItDropped(t *testing.T) {
	root := t.TempDir()
	var changed []string
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		write(t, root, "internal/"+n+"/thing_one.go", "package "+n+"\n")
		write(t, root, "internal/"+n+"/thing_two.go", "package "+n+"\n")
		changed = append(changed, "internal/"+n+"/thing_one.go")
	}
	env, err := Resolve(root, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Expansions) != maxSiblings {
		t.Fatalf("carried %d siblings, want the bound of %d", len(env.Expansions), maxSiblings)
	}
	if len(env.Notes) == 0 || !strings.Contains(env.Notes[0], "not carried") {
		t.Fatalf("the drop has to be visible: %v", env.Notes)
	}
}

// A change with nothing beside it is the common case and is not an error.
func TestNoPrecedentIsNotAFailure(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package main\n")
	env, err := Resolve(root, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("nothing to say should be nil, got %+v", env)
	}
}

// A file the change added but never wrote, or one git lists and the tree does
// not hold, must not fail the run.
func TestAMissingDirectoryIsNotAnError(t *testing.T) {
	env, err := Resolve(t.TempDir(), []string{"gone/away/file_name.go"})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("want nil, got %+v", env)
	}
}
