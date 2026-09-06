package report

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/change"
	"github.com/ccason/redline/internal/cover"
	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/pane"
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
	a := reviewIdentity(base, "worktree", &change.Set{
		Files: []change.File{{Path: "a.go", Diff: "+one"}},
	})
	b := reviewIdentity(base, "worktree", &change.Set{
		Files: []change.File{{Path: "a.go", Diff: "+two"}},
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

// The HTML report dropped pane.Render (the lint delta's "N introduced, M
// resolved", a suppression's added-directive list) on the floor entirely —
// markdown has rendered it since section1 existed. A report with a real
// lint delta and nothing under a "Lint" heading anywhere on the page is
// this bug; this is the regression test for it.
func TestWhatChangedSectionRendersPaneSummaries(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report:  &findings.Report{Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}},
		Change:  &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}},
		Renders: []pane.Render{{Title: "Lint delta", Summary: "3 finding(s) introduced, 9 resolved (golangci-lint)"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<h2 id="changed">What changed</h2>`) {
		t.Fatal("a change with pane renders must carry a What changed heading")
	}
	if !strings.Contains(html, "Lint delta") || !strings.Contains(html, "9 resolved") {
		t.Errorf("the pane's own render must appear on the page:\n%s", html)
	}
}

func TestDrillGroupsByLanguageAndKind(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 2, ExaminedFiles: 2}},
		Change: &change.Set{Files: []change.File{
			{Path: "internal/change/change.go", Language: "go", Added: 20, Removed: 3, Diff: "@@ -1 +1 @@\n+x\n"},
			{Path: "internal/change/change_test.go", Language: "go", Added: 80, Removed: 0, Diff: "@@ -1 +1 @@\n+func TestX\n"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<h2 id="drill">Drill in</h2>`) {
		t.Fatal("a change with files must carry the drill-in list")
	}
	// The composition breakdown is now the drill grouping: one group per
	// language and kind, folded in so there is one place to browse files.
	if !strings.Contains(html, "go source") || !strings.Contains(html, "go test") {
		t.Errorf("drill must group by language and kind:\n%s", html)
	}
	for _, p := range []string{"internal/change/change.go", "internal/change/change_test.go"} {
		if !strings.Contains(html, `data-jump="`+p+`"`) {
			t.Errorf("every changed file must be a drill row; missing %q", p)
		}
	}
}

// A finding with a file offers a View code control that opens that file's diff
// in the drawer at the finding's line — the one source view, shared with the
// drill-in, rather than a second inline snippet.
func TestFindingLinksToItsFileInTheDrawer(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{
			Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1},
			Findings: []findings.Finding{
				{File: "a.go", Line: 4, Rule: "lint/errcheck", Message: "unchecked error", Severity: findings.SeverityWarning},
			},
		},
		Change: &change.Set{
			Files: []change.File{{Path: "a.go", Language: "go", Diff: "@@ -1,4 +1,4 @@\n+x := risky()\n"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `class="f-view" data-file="a.go" data-line="4"`) {
		t.Fatal("a finding with a file must offer a View code control targeting its line")
	}
	// The file's diff lives in the store the drawer opens, not in a second
	// inline snippet on the finding.
	if !strings.Contains(html, `data-path="a.go"`) {
		t.Error("the finding's file must be openable in the drawer")
	}
	if strings.Contains(html, "offending code") {
		t.Error("the inline snippet is gone; the drawer is the single source view")
	}
}

func TestHTMLIdentityAttribute(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{BaseSHA: "abcdef0123456789", Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}},
		Change: &change.Set{
			Target: &target.Target{Kind: target.KindBranch, Head: "ffffffffffffffff"},
			Files:  []change.File{{Path: "a.go", Diff: "@@ -1 +1 @@\n-a\n+b\n", Areas: []string{"code"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `data-review="abcdef01:ffffffff"`) {
		t.Fatalf("expected identity in body, got a prefix of:\n%s", html[:400])
	}
}

func TestHTMLFileWalkListsEveryChangedFile(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 2, ExaminedFiles: 0}},
		Change: &change.Set{
			Files: []change.File{
				{Path: "cmd/redline/main.go", Status: "modified", Added: 10, Removed: 2, Areas: []string{"code"}},
				{Path: "README.md", Status: "modified", Added: 3, Removed: 1, Areas: []string{"code"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<h2 id="drill">Drill in</h2>`) {
		t.Fatal("report must carry a file list")
	}
	for _, want := range []string{"cmd/redline/main.go", "README.md"} {
		if !strings.Contains(html, want) {
			t.Fatalf("every changed file must appear in the walk; missing %q", want)
		}
	}
}

// A --pr run knows its pull request; the subtitle carries it.
func TestPullRequestRendersFromTheTarget(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}},
		Change: &change.Set{
			Target: &target.Target{Kind: target.KindPR, PR: &target.PullRequest{
				Number: 42, Title: "Add the briefing", URL: "https://example.test/pr/42",
			}},
			Files: []change.File{{Path: "a.go", Areas: []string{"code"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "https://example.test/pr/42") {
		t.Error("the PR the target names must appear on the page")
	}
}

// The sidebar and the page must agree. A section that gains or loses a heading
// without its nav entry moving too leaves a dead link or an unreachable section,
// and neither is visible from reading either file alone.
func TestNavMatchesTheSectionsOnThePage(t *testing.T) {
	full := HTMLInput{
		Report: &findings.Report{
			Coverage: findings.Coverage{ChangedFiles: 2, ExaminedFiles: 1,
				Diff: &cover.Result{Profile: "coverage.out", Lines: 4, Covered: 2, Percent: 50}},
			Findings: []findings.Finding{
				{File: "b.sql", Rule: "obs", Message: "observed", Severity: findings.SeverityError},
			},
			Confirmations: []findings.Confirmation{{Rule: "r", Message: "held"}},
			Unknowns:      []findings.Unknown{{Substrate: "s", Message: "unknown"}},
		},
		Change: &change.Set{
			UITouched: true,
			Files:     []change.File{{Path: "a.go", Areas: []string{"code"}, Diff: "@@ -1 +1 @@\n+x\n"}},
		},
	}
	// The pre-push minimum: no findings, no captures, no profile.
	bare := HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 0}},
		Change: &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}},
	}

	navHref := regexp.MustCompile(`data-nav="([^"]+)"`)
	headID := regexp.MustCompile(`<h2 id="([^"]+)"`)

	for name, in := range map[string]HTMLInput{"full": full, "bare": bare} {
		html, err := HTML(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var nav, heads []string
		for _, m := range navHref.FindAllStringSubmatch(html, -1) {
			nav = append(nav, m[1])
		}
		for _, m := range headID.FindAllStringSubmatch(html, -1) {
			heads = append(heads, m[1])
		}
		if len(nav) == 0 {
			t.Fatalf("%s: no sidebar rendered", name)
		}
		if strings.Join(nav, ",") != strings.Join(heads, ",") {
			t.Errorf("%s: sidebar and sections disagree\n nav: %v\nheads: %v", name, nav, heads)
		}
	}
}

// The sidebar should show where the gaps are without scrolling to find them.
func TestNavMarksSectionsThatAreGaps(t *testing.T) {
	html, err := HTML(HTMLInput{
		// UI moved with nothing captured, and no coverage profile.
		Report: &findings.Report{
			Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 0},
			Unknowns: []findings.Unknown{{Substrate: "s", Message: "u"}},
		},
		Change: &change.Set{
			UITouched: true,
			Files:     []change.File{{Path: "web/src/App.tsx", Areas: []string{"ui"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"interface", "coverage", "unknowns"} {
		re := regexp.MustCompile(`data-nav="` + id + `"[^>]*class="gap"`)
		if !re.MatchString(html) {
			t.Errorf("%s should be marked as a gap in the sidebar", id)
		}
	}
	// A section with a real result is not a gap.
	if regexp.MustCompile(`data-nav="drill"[^>]*class="gap"`).MatchString(html) {
		t.Error("the drill-in file list is not a gap")
	}
}

func TestNavIsSelfContainedAndSticky(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}},
		Change: &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<nav class="nav" aria-label="Sections">`) {
		t.Error("the sidebar should be a labelled nav landmark")
	}
	if !strings.Contains(html, ".nav{position:sticky") {
		t.Error("the sidebar must stay put while the page scrolls — that is the whole point")
	}
	// Rendered server-side: the map must exist before any script runs.
	navAt := strings.Index(html, `data-nav=`)
	scriptAt := strings.Index(html, "<script>")
	if navAt < 0 || navAt > scriptAt {
		t.Error("the sidebar must be in the markup, not built by script")
	}
}

// Comments are written against one tree. A payload that does not name it can be
// applied to code the reviewer never saw.
func TestCommentPayloadNamesTheChangeItBelongsTo(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{BaseSHA: "abcdef0123456789", Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}},
		Change: &change.Set{
			Target: &target.Target{Kind: target.KindBranch, Head: "ffffffffffffffff", Label: "feat/x"},
			Files:  []change.File{{Path: "a.go", Diff: "@@ -1 +1 @@\n-a\n+b\n", Areas: []string{"code"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `data-target=`) {
		t.Error("the page must carry the target so the copied payload can name it")
	}
	if !strings.Contains(html, "redlineReviewComments") {
		t.Error("the payload needs a key an agent can recognise")
	}
	for _, want := range []string{"instruction:", "review:", "target:", "comments:"} {
		if !strings.Contains(html, want) {
			t.Errorf("payload missing %q", want)
		}
	}
	if !strings.Contains(html, "Address every one") {
		t.Error("the payload should tell the agent what to do with it")
	}
}

// UI capture is not built. Its absence is stated in proportion to whether the
// interface actually moved.
func TestInterfaceSectionIsHonestWhenEmpty(t *testing.T) {
	uiChange := &change.Set{
		UITouched: true,
		Files:     []change.File{{Path: "web/src/App.tsx", Areas: []string{"ui"}}},
	}
	rep := &findings.Report{Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 0}}

	html, err := HTML(HTMLInput{Report: rep, Change: uiChange})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "touches the interface and no routes were captured") {
		t.Error("a UI change with no captures must read as a gap")
	}
	// Loud, not a footnote.
	if !strings.Contains(html, `<div class="banner">This change touches the interface`) {
		t.Error("that gap belongs in a banner, not italic small print")
	}

	noUI := &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}}
	html, err = HTML(HTMLInput{Report: rep, Change: noUI})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "touches the interface and no routes were captured") {
		t.Error("a change with no UI files must not claim a UI gap")
	}
	if !strings.Contains(html, "absence of looking") {
		t.Error("even then, absence must not read as a finding of no change")
	}
}

func TestTestFilesAreBrowsableInTheDrill(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 2, ExaminedFiles: 1}},
		Change: &change.Set{Files: []change.File{
			{Path: "internal/run/run.go", Language: "go", Diff: "@@ -1 +1 @@\n-a\n+b\n"},
			{Path: "internal/run/run_test.go", Language: "go", Diff: "@@ -1 +1 @@\n-func TestOld\n+func TestNew\n"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Tests are their own language/kind group now, browsable like any file.
	if !strings.Contains(html, "go test") {
		t.Error("changed tests must appear in their own group")
	}
	if !strings.Contains(html, `data-jump="internal/run/run_test.go"`) {
		t.Error("the test file must be a drill row")
	}
	if !strings.Contains(html, "func TestNew") {
		t.Error("the test diff must be viewable in the drawer store")
	}
}

// The number stands in for reading the tests, so its absence has to be as
// legible as its presence. A missing profile rendering as 0% would read as
// "nothing is tested", which is a much stronger claim than "nobody measured".
func TestMissingCoverageProfileReadsAsUnknownNotZero(t *testing.T) {
	rep := &findings.Report{Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}}
	ch := &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}}

	html, err := HTML(HTMLInput{Report: rep, Change: ch})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "No coverage profile was found") {
		t.Error("the page must say the number is unknown")
	}
	if !strings.Contains(html, "not the same as untested") {
		t.Error("the page must distinguish unmeasured from untested")
	}
	if strings.Contains(html, "% of the") {
		t.Error("a missing profile must never render as a percentage")
	}
	// The tile reads as unanswered rather than as a score.
	if !strings.Contains(html, `<div class="n warning">?</div><div class="l">diff covered</div>`) {
		t.Error("the coverage tile must show ? when nothing measured it")
	}

	md := Markdown(rep, nil, nil, ch)
	if !strings.Contains(md, "No coverage profile was found") {
		t.Error("markdown must say the same")
	}
}

func TestCoverageNumberNamesItsProfileAndGaps(t *testing.T) {
	rep := &findings.Report{
		Coverage: findings.Coverage{
			ChangedFiles: 1, ExaminedFiles: 1,
			Diff: &cover.Result{
				Profile: "coverage.out", Lines: 10, Covered: 7, Percent: 70,
				Uncovered: []cover.FileGap{{Path: "internal/run/run.go", Lines: []int{12, 13, 14}}},
			},
		},
	}
	ch := &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}}

	html, err := HTML(HTMLInput{Report: rep, Change: ch})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"70%", "coverage.out", "internal/run/run.go", "7 covered, 3 not"} {
		if !strings.Contains(html, want) {
			t.Errorf("coverage section missing %q", want)
		}
	}

	md := Markdown(rep, nil, nil, ch)
	for _, want := range []string{"70%", "coverage.out", "internal/run/run.go"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown coverage missing %q", want)
		}
	}
}

func TestStaleCoverageProfileIsCalledOut(t *testing.T) {
	rep := &findings.Report{
		Coverage: findings.Coverage{
			ChangedFiles: 1, ExaminedFiles: 1,
			Diff: &cover.Result{Profile: "coverage.out", Lines: 4, Covered: 4, Percent: 100, Stale: true},
		},
	}
	ch := &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}}

	html, err := HTML(HTMLInput{Report: rep, Change: ch})
	if err != nil {
		t.Fatal(err)
	}
	// 100% from a profile that predates the change is the most misleading
	// number on the page, so it gets a banner rather than a footnote.
	if !strings.Contains(html, `class="banner">The profile`) {
		t.Error("a stale profile needs a banner beside its number")
	}
	if !strings.Contains(Markdown(rep, nil, nil, ch), "predates this change") {
		t.Error("markdown must flag the stale profile too")
	}
}

func TestGeneratedExclusionsAreNamedOnBothReports(t *testing.T) {
	rep := &findings.Report{
		Coverage: findings.Coverage{
			ChangedFiles: 1, ExaminedFiles: 1,
			Generated: []string{"internal/api/oas_schemas_gen.go", "go.sum"},
		},
	}
	ch := &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}}

	html, err := HTML(HTMLInput{Report: rep, Change: ch})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 generated files excluded", "oas_schemas_gen.go", "go.sum"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML must name every exclusion; missing %q", want)
		}
	}

	md := Markdown(rep, nil, nil, ch)
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
		Change: &change.Set{Files: []change.File{{Path: "a.go", Areas: []string{"code"}}}},
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
		&change.Set{Files: []change.File{{Path: "a.go", Status: "added", Added: 4}}})
	if !strings.Contains(md, "## Files") || !strings.Contains(md, "`a.go`") {
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
	md := Markdown(&rep, nil, nil, nil)
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

// The walk rows are buttons so clicking one opens the drawer. A button without
// its chrome reset lays out at its intrinsic width, which turned the report's
// primary list into a two-column jumble of centred text.
func TestWalkRowsAreStyledAsRowsNotButtons(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{Coverage: findings.Coverage{ChangedFiles: 2, ExaminedFiles: 1}},
		Change: &change.Set{Files: []change.File{
			{Path: "a.go", Areas: []string{"code"}},
			{Path: "b.go", Areas: []string{"code"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	styleEnd := strings.Index(html, "</style>")
	ruleStart := strings.Index(html[:styleEnd], ".walk-row.jump{")
	if styleEnd < 0 || ruleStart < 0 {
		t.Fatal("report must embed a .walk-row.jump style rule before </style>")
	}
	css := html[:styleEnd]
	rule := css[ruleStart:]
	ruleEnd := strings.Index(rule, "}")
	if ruleEnd < 0 {
		t.Fatal(".walk-row.jump rule must be closed")
	}
	rule = rule[:ruleEnd]
	for _, want := range []string{"display:block", "width:100%", "text-align:left"} {
		if !strings.Contains(rule, want) {
			t.Errorf(".walk-row.jump must set %s, got %q", want, rule)
		}
	}
}

func TestWalkRowsAreControlsThatCarryFindingCounts(t *testing.T) {
	rep := &findings.Report{
		Coverage: findings.Coverage{ChangedFiles: 2, ExaminedFiles: 1},
		Findings: []findings.Finding{{
			File: "a.go", Line: 3, Rule: "migration-modified-after-merge",
			Severity: findings.SeverityError, Message: "boom",
		}},
		Substrates: []findings.SubstrateStatus{
			{Name: "migrations", State: findings.SubstrateRan},
		},
	}
	ch := &change.Set{Files: []change.File{
		{Path: "a.go", Status: "modified", Added: 1, Areas: []string{"code"}},
		{Path: "b.go", Status: "modified", Added: 1, Areas: []string{"code"}},
	}}

	html, err := HTML(HTMLInput{Report: rep, Change: ch})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(html, `data-jump="a.go"`) {
		t.Fatal("walk rows must be jump controls")
	}
	if !strings.Contains(html, "1 finding<") {
		t.Fatal("a file carrying a finding must say so in the walk")
	}
}

func TestFindingRendersAgentVerdict(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{
			Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1},
			Findings: []findings.Finding{{
				File: "a.go", Line: 3, Rule: "suppression-added", Severity: findings.SeverityInfo,
				Message: "this change adds a nolint directive",
				Verdict: &findings.Verdict{Ruling: "rule-noisy", Rationale: "errcheck is noisy here", Source: findings.SourceLLM},
			}},
		},
		Change: &change.Set{Files: []change.File{{Path: "a.go", Language: "go", Diff: "@@ -1 +1 @@\n+x //nolint\n"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "rule-noisy") {
		t.Error("the agent's ruling must render on the finding card")
	}
	if !strings.Contains(html, "errcheck is noisy here") {
		t.Error("the rationale must render")
	}
	if !strings.Contains(html, `class="tag vd"`) {
		t.Error("the verdict needs its own tag, distinct from the deterministic finding")
	}
}

func TestExpandForFindingsAddsContextForOffDiffLine(t *testing.T) {
	dir := t.TempDir()
	src := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nBAD()\nl9\nl10\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// The diff only touches line 1; the finding sits on line 8, far outside the
	// default 3-line context, so it is not in the diff at all.
	diff := "@@ -1 +1 @@\n-old1\n+l1\n"
	out := expandForFindings(dir, "a.go", diff, []int{8})
	if !strings.Contains(out, "BAD()") {
		t.Fatalf("expected a context window around the off-diff finding line:\n%s", out)
	}
	// After highlighting, the finding line is addressable, so open() can mark it.
	if !strings.Contains(highlightDiffFor("a.go", out), `data-line="8"`) {
		t.Fatalf("the finding line must become addressable:\n%s", out)
	}
}

func TestExpandForFindingsSkipsLinesAlreadyInDiff(t *testing.T) {
	diff := "@@ -1,2 +1,2 @@\n-a\n+BAD()\n b\n"
	out := expandForFindings(t.TempDir(), "a.go", diff, []int{1})
	if out != diff {
		t.Fatalf("a finding line already in the diff must not add context:\n%s", out)
	}
}
