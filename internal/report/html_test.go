package report

import (
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
	"github.com/ccason/redline/internal/target"
)

func TestHighlightDiffForUsesSourceLineNumbers(t *testing.T) {
	diff := "diff --git a/foo.go b/foo.go\n" +
		"--- a/foo.go\n" +
		"+++ b/foo.go\n" +
		"@@ -10,3 +10,4 @@ func x() {\n" +
		" context\n" +
		"-old\n" +
		"+new\n" +
		" more\n"
	got := highlightDiffFor("foo.go", diff)
	if !strings.Contains(got, `data-file="foo.go" data-line="11" data-side="new">+new</span>`) {
		t.Fatalf("added line should be new-file line 11, got:\n%s", got)
	}
	if !strings.Contains(got, `data-file="foo.go" data-line="11" data-side="old">-old</span>`) {
		t.Fatalf("deleted line should be old-file line 11, got:\n%s", got)
	}
	if !strings.Contains(got, `data-line="10" data-side="new"> context</span>`) {
		t.Fatalf("context line should be new-file line 10, got:\n%s", got)
	}
	if strings.Contains(got, `data-line="7"`) || strings.Contains(got, `data-line="8"`) {
		t.Fatalf("must not use dump-row indexes:\n%s", got)
	}
}

func TestReviewIdentityDiffersForWorktreeChanges(t *testing.T) {
	base := "aaaaaaaaaaaaaaaa"
	a := reviewIdentity(base, "worktree", &packet.Packet{
		Files: []packet.FileChange{{Path: "a.go", Diff: "+one"}},
	})
	b := reviewIdentity(base, "worktree", &packet.Packet{
		Files: []packet.FileChange{{Path: "a.go", Diff: "+two"}},
	})
	if a == b {
		t.Fatal("different working-tree diffs must not share a comment key")
	}
	c := reviewIdentity(base, "bbbbbbbbbbbbbbbb", nil)
	d := reviewIdentity(base, "bbbbbbbbbbbbbbbb", nil)
	if c != d {
		t.Fatal("same base+head SHA must share a comment key")
	}
}

func TestHTMLIdentityAttribute(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{BaseSHA: "abcdef0123456789", Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}},
		Packet: &packet.Packet{
			Target: &target.Target{Kind: target.KindBranch, Head: "ffffffffffffffff"},
			Files:  []packet.FileChange{{Path: "a.go", Diff: "@@ -1 +1 @@\n-a\n+b\n", Areas: []string{"code"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `data-review="abcdef01:ffffffff"`) {
		t.Fatalf("expected identity in body, got a prefix of:\n%s", html[:400])
	}
}

func TestHTMLFileWalkUsesPacketAndAgentNotes(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 2, ExaminedFiles: 0}},
		Packet: &packet.Packet{
			Files: []packet.FileChange{
				{Path: "cmd/redline/main.go", Status: "modified", Added: 10, Removed: 2, Areas: []string{"code"}},
				{Path: "README.md", Status: "modified", Added: 3, Removed: 1, Areas: []string{"code"}},
			},
		},
		Review: &packet.Review{
			Summary: "Serve the report over loopback.",
			Files: []packet.FileNote{
				{Path: "cmd/redline/main.go", Summary: "Prints the Report: URL after ingest."},
				{Path: "ghost.go", Summary: "Must not appear — not in the packet."},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<h2 id="files">Files</h2>`) {
		t.Fatal("report must lead with a file walkthrough")
	}
	if !strings.Contains(html, "Prints the Report: URL after ingest.") {
		t.Fatal("agent file summary must render")
	}
	if !strings.Contains(html, "README.md") {
		t.Fatal("every packet file must appear even without a note")
	}
	if strings.Contains(html, "ghost.go") {
		t.Fatal("notes for paths outside the packet must be dropped")
	}
}

func TestGeneratedExclusionsAreNamedOnBothReports(t *testing.T) {
	rep := &findings.Report{
		Coverage: findings.Coverage{
			ChangedFiles: 1, ExaminedFiles: 1,
			Generated: []string{"internal/api/oas_schemas_gen.go", "go.sum"},
		},
	}
	pkt := &packet.Packet{Files: []packet.FileChange{{Path: "a.go", Areas: []string{"code"}}}}

	html, err := HTML(HTMLInput{Report: rep, Packet: pkt})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 generated files excluded", "oas_schemas_gen.go", "go.sum"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML must name every exclusion; missing %q", want)
		}
	}

	md := Markdown(rep, nil, nil, pkt, nil)
	for _, want := range []string{"2 generated file(s) excluded", "oas_schemas_gen.go", "go.sum"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown must name every exclusion; missing %q", want)
		}
	}
}

// The tiles block once closed .wrap early, so every section below it rendered
// outside the page's max-width and padding.
func TestPageWrapperClosesOnce(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}},
		Packet: &packet.Packet{Files: []packet.FileChange{{Path: "a.go", Areas: []string{"code"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "\n/div>") {
		t.Error("stray /div> renders as literal text on the page")
	}
	if opens, closes := strings.Count(html, "<div"), strings.Count(html, "</div>"); opens != closes {
		t.Errorf("unbalanced divs: %d opened, %d closed", opens, closes)
	}
}

func TestMarkdownFileSection(t *testing.T) {
	md := Markdown(&findings.Report{Coverage: findings.Coverage{ChangedFiles: 1}}, nil, nil,
		&packet.Packet{Files: []packet.FileChange{{Path: "a.go", Status: "added", Added: 4}}},
		&packet.Review{Files: []packet.FileNote{{Path: "a.go", Summary: "New entry point."}}})
	if !strings.Contains(md, "## Files") || !strings.Contains(md, "`a.go`") || !strings.Contains(md, "New entry point.") {
		t.Fatalf("expected file walkthrough, got:\n%s", md)
	}
}

func TestMarkdownSection3DoesNotClaimCompleteCoverage(t *testing.T) {
	rep := findings.Report{
		Coverage: findings.Coverage{
			ChangedFiles:  2,
			ExaminedFiles: 1,
			Unexamined:    []string{"README.md"},
		},
	}
	md := Markdown(&rep, nil, nil, nil, nil)
	if strings.Contains(md, "Nothing. Every check that applies") {
		t.Fatal("section 3 must not claim every check ran when files are unexamined")
	}
	if !strings.Contains(md, "were not in any pane's scope") {
		t.Fatalf("expected an unexamined-files sentence, got:\n%s", md)
	}
}

// A deleted line whose text begins with "--" is content, not a file header.
// Misreading it left the old-side cursor behind and shifted every following
// line number in the hunk — the exact failure line anchoring exists to avoid.
func TestDiffLineStartingWithDashesIsContent(t *testing.T) {
	diff := "--- a/README.md\n+++ b/README.md\n@@ -10,4 +10,3 @@\n context\n---port N\n+++count M\n more\n"
	html := highlightDiffFor("README.md", diff)

	if !strings.Contains(html, `data-line="11" data-side="old"`) {
		t.Fatalf("deleted --port line was not anchored to old line 11:\n%s", html)
	}
	if !strings.Contains(html, `data-line="11" data-side="new"`) {
		t.Fatalf("added ++count line was not anchored to new line 11:\n%s", html)
	}
	// " more" is the second context line: old 12, new 12.
	if !strings.Contains(html, `data-line="12" data-side="new"> more`) {
		t.Fatalf("context after the dashed lines is misnumbered:\n%s", html)
	}
}

func TestFileHeadersStillDetected(t *testing.T) {
	diff := "diff --git a/x.go b/x.go\nindex 1..2 100644\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n"
	html := highlightDiffFor("x.go", diff)
	if strings.Contains(html, `data-line="1" data-side="old">--- a/x.go`) {
		t.Fatal("the --- file header was treated as content")
	}
	if !strings.Contains(html, `data-line="2" data-side="old">-b`) {
		t.Fatalf("deletion not anchored to old line 2:\n%s", html)
	}
	if !strings.Contains(html, `data-line="2" data-side="new">+c`) {
		t.Fatalf("addition not anchored to new line 2:\n%s", html)
	}
}

// The walk is the report's first screen. Rendering it as inert text while the
// diffs sit inside a collapsed section below is what made the page look broken:
// a reviewer clicks the file they care about and nothing happens.
func TestWalkRowsAreControlsThatCarryFindingCounts(t *testing.T) {
	rep := &findings.Report{
		Coverage: findings.Coverage{ChangedFiles: 2, ExaminedFiles: 0},
		Findings: []findings.Finding{{
			File: "a.go", Line: 3, Rule: "review", Severity: findings.SeverityError,
			Message: "boom", Source: findings.SourceLLM, Reviewer: "claude", Confidence: "high",
		}},
		Substrates: []findings.SubstrateStatus{
			{Name: "reviewer:claude", State: findings.SubstrateRan, Detail: "1 findings"},
		},
	}
	pkt := &packet.Packet{Files: []packet.FileChange{
		{Path: "a.go", Status: "modified", Added: 1},
		{Path: "b.go", Status: "modified", Added: 1},
	}}

	html, err := HTML(HTMLInput{Report: rep, Packet: pkt})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(html, `data-jump="a.go"`) {
		t.Fatal("walk rows must be jump controls")
	}
	if !strings.Contains(html, "1 finding<") {
		t.Fatal("a file carrying a finding must say so in the walk")
	}
	if !strings.Contains(html, "judged by claude") {
		t.Fatal("a reviewer finding must name its reviewer")
	}
	// The old banner claimed an empty findings list, on a page showing findings.
	if strings.Contains(html, "an empty findings list says nothing") {
		t.Fatal("banner contradicts the findings on the page when a reviewer ran")
	}
	if !strings.Contains(html, "claude&#39;s reading of the change") {
		t.Fatal("banner should say whose reading this is")
	}
}
