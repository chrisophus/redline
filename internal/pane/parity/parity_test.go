package parity

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/pane"
)

// providerTree is the shape this pane exists for: three parallel
// implementations with no interface between them, so nothing else in Redline
// can say a capability landed in one and not the others.
var providerTree = []string{
	"internal/provider/aws/offer.go",
	"internal/provider/aws/client.go",
	"internal/provider/aws/auth.go",
	"internal/provider/azure/offer.go",
	"internal/provider/azure/client.go",
	"internal/provider/azure/auth.go",
	"internal/provider/gcp/offer.go",
	"internal/provider/gcp/client.go",
	"internal/provider/gcp/auth.go",
}

func run(t *testing.T, base, head []string, changed []string) pane.Result {
	t.Helper()
	p := &Pane{}
	p.Scope(changed)
	res, err := p.Diff(&observation{Rev: "base", Files: base}, &observation{Rev: "", Files: head})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestACapabilityAddedToOneProviderOnly(t *testing.T) {
	head := append(append([]string{}, providerTree...), "internal/provider/gcp/offer_amend.go")
	res := run(t, providerTree, head, []string{"internal/provider/gcp/offer_amend.go"})
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want the parity gap", res.Findings)
	}
	f := res.Findings[0]
	if !strings.Contains(f.Message, "offer_amend.go") {
		t.Errorf("message does not name the file: %s", f.Message)
	}
	if !strings.Contains(f.Message, "aws and azure") {
		t.Errorf("message does not name the siblings without it: %s", f.Message)
	}
	if f.File != "internal/provider/gcp/offer_amend.go" {
		t.Errorf("finding anchored at %q", f.File)
	}
}

// Directories that merely sit side by side are not parallel implementations.
// Redline's own internal/pane/{lint,migrations,openapi} share no filenames, so
// a change to one says nothing about the others.
func TestNeighbouringDirectoriesAreNotSiblings(t *testing.T) {
	tree := []string{
		"internal/pane/lint/lint.go",
		"internal/pane/lint/config.go",
		"internal/pane/lint/suppressions.go",
		"internal/pane/migrations/migrations.go",
		"internal/pane/migrations/git.go",
		"internal/pane/openapi/openapi.go",
	}
	head := append(append([]string{}, tree...), "internal/pane/lint/delta.go")
	res := run(t, tree, head, []string{"internal/pane/lint/delta.go"})
	if len(res.Findings) != 0 {
		t.Errorf("fired on directories that share no shape: %+v", res.Findings)
	}
	if len(res.Confirmations) == 0 {
		t.Error("a pane that checked and found nothing must say so")
	}
}

// A gap that predates the change belongs to whoever made it. Reporting it
// would put a finding on every future change to that directory.
func TestAPreexistingGapIsNotThisChangesNews(t *testing.T) {
	base := append(append([]string{}, providerTree...), "internal/provider/gcp/legacy.go")
	head := append(append([]string{}, base...), "internal/provider/gcp/offer_amend.go")
	res := run(t, base, head, []string{"internal/provider/gcp/legacy.go", "internal/provider/gcp/offer_amend.go"})
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want only the newly added file", len(res.Findings))
	}
	if !strings.Contains(res.Findings[0].File, "offer_amend.go") {
		t.Errorf("reported the old gap: %s", res.Findings[0].File)
	}
}

func TestAFileEverySiblingHasIsNotAGap(t *testing.T) {
	res := run(t, providerTree, providerTree, []string{"internal/provider/gcp/offer.go"})
	if len(res.Findings) != 0 {
		t.Errorf("fired on a file every sibling has: %+v", res.Findings)
	}
}

func TestFindingsAreInfoNotGates(t *testing.T) {
	head := append(append([]string{}, providerTree...), "internal/provider/gcp/offer_amend.go")
	res := run(t, providerTree, head, []string{"internal/provider/gcp/offer_amend.go"})
	// A missing sibling file is a question for the author, not a defect. Some
	// capabilities genuinely only exist on one provider.
	if res.Findings[0].Severity != "info" {
		t.Errorf("severity = %q, want info", res.Findings[0].Severity)
	}
}

func TestScopeSkipsTopLevelFiles(t *testing.T) {
	p := &Pane{}
	got := p.Scope([]string{"README.md", "internal/provider/gcp/offer.go"})
	if len(got) != 1 || got[0] != "internal/provider/gcp/offer.go" {
		t.Errorf("scope = %v; a file with no parent set cannot have siblings", got)
	}
}

func TestSiblingsNeedAMinimumSharedShape(t *testing.T) {
	// Two files in common is below the bar: that is a coincidence, not a
	// parallel implementation.
	tree := []string{
		"svc/a/main.go", "svc/a/util.go", "svc/a/only_a.go",
		"svc/b/main.go", "svc/b/util.go",
	}
	head := append(append([]string{}, tree...), "svc/a/feature.go")
	if res := run(t, tree, head, []string{"svc/a/feature.go"}); len(res.Findings) != 0 {
		t.Errorf("fired on a two-file overlap: %+v", res.Findings)
	}
	// Three is the bar.
	tree = append(tree, "svc/b/only_a.go")
	head = append(append([]string{}, tree...), "svc/a/feature.go")
	if res := run(t, tree, head, []string{"svc/a/feature.go"}); len(res.Findings) != 1 {
		t.Errorf("did not fire once the directories share a shape: %+v", res.Findings)
	}
}
