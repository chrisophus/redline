package report

import (
	"strings"
	"testing"

	"github.com/ccason/redline/internal/change"
	"github.com/ccason/redline/internal/findings"
)

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
