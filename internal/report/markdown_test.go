package report

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/findings"
)

// A stale profile mentions none of the lines a change adds, because it
// predates them. Reporting that as "nothing here is coverable" states the one
// thing the author could act on and gets it backwards: the change is not
// untestable, nobody has measured it yet. Both facts produce Percent == -1,
// so the render has to tell them apart.
func TestStaleProfileIsNotReportedAsNothingToTest(t *testing.T) {
	rep := &findings.Report{Coverage: findings.Coverage{
		CoverableFiles: 2,
		Diff: &cover.Result{
			Profile: "coverage.out",
			Percent: -1,
			Stale:   true,
		},
	}}
	md := Markdown(rep, nil, nil, nil)
	if strings.Contains(md, "nothing for a test to execute") {
		t.Errorf("a stale profile is reported as an untestable change:\n%s", md)
	}
	if !strings.Contains(md, "added lines appear in") {
		t.Errorf("the render does not say the lines are absent from the profile:\n%s", md)
	}
}

// The same sentinel with a fresh profile really does mean the change added no
// line a profile could describe, and that wording has to survive.
func TestNothingCoverableStillSaysSo(t *testing.T) {
	rep := &findings.Report{Coverage: findings.Coverage{
		CoverableFiles: 1,
		Diff: &cover.Result{
			Profile: "coverage.out",
			Percent: -1,
		},
	}}
	md := Markdown(rep, nil, nil, nil)
	if !strings.Contains(md, "nothing for a test to execute") {
		t.Errorf("a fresh profile with no coverable added line must say so:\n%s", md)
	}
}

func TestMarkdownRendersCompositionTable(t *testing.T) {
	ch := &change.Set{Files: []change.File{
		{Path: "internal/change/change.go", Language: "go", Added: 20, Removed: 3},
		{Path: "internal/change/change_test.go", Language: "go", Added: 80, Removed: 0},
	}}
	md := Markdown(&findings.Report{}, nil, nil, ch)
	if !strings.Contains(md, "## Lines by language and type") {
		t.Fatalf("expected a composition section:\n%s", md)
	}
	if !strings.Contains(md, "| go | source | 1 | 20 | 3 |") {
		t.Errorf("expected the source row, got:\n%s", md)
	}
	if !strings.Contains(md, "| go | test | 1 | 80 | 0 |") {
		t.Errorf("expected the test row, got:\n%s", md)
	}
}

func TestMarkdownOmitsCompositionForNoChange(t *testing.T) {
	md := Markdown(&findings.Report{}, nil, nil, nil)
	if strings.Contains(md, "Lines by language and type") {
		t.Errorf("a nil change must not render an empty composition section:\n%s", md)
	}
}
