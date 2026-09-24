package post

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/target"
)

// changedFiles is the file list a payload is built from, from paths alone: a
// language off the extension and line counts that differ per file, so a test
// can tell one row from another.
func changedFiles(paths ...string) []change.File {
	out := make([]change.File, 0, len(paths))
	for i, path := range paths {
		lang := "other"
		if dot := strings.LastIndex(path, "."); dot >= 0 {
			lang = path[dot+1:]
		}
		out = append(out, change.File{Path: path, Language: lang, Added: 10 + i, Removed: i})
	}
	return out
}

func sampleReport() *findings.Report {
	rep := &findings.Report{
		Coverage: findings.Coverage{ChangedFiles: 5, ExaminedFiles: 3, CoverableFiles: 2},
		Substrates: []findings.SubstrateStatus{
			{Name: "migrations", State: findings.SubstrateRan},
			{Name: "openapi", State: findings.SubstrateSkipped, Detail: "this change touches none of the files this pane reads"},
			{Name: "lint", State: findings.SubstrateNotApplicable, Detail: "the repository has no files this pane reads"},
		},
		Findings: []findings.Finding{
			{File: "a.go", Line: 12, Rule: "migration-modified-after-merge", Substrate: "migrations",
				Severity: findings.SeverityError, Message: "merged migration edited"},
			{Rule: "migration-edited", Substrate: "redline/sql",
				Severity: findings.SeverityWarning, Message: "migration edited after merge"},
		},
	}
	rep.Finalize()
	return rep
}

func prTarget() *target.Target {
	return &target.Target{Kind: target.KindPR, Head: "deadbeef",
		PR: &target.PullRequest{Number: 7, URL: "https://github.com/o/r/pull/7"}}
}

func TestBuildSplitsLocatedFromBodyFindings(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "https://ci/report.html", nil)

	if p.CommitID != "deadbeef" {
		t.Fatalf("commit id should be the head SHA: %q", p.CommitID)
	}
	if len(p.Comments) != 1 {
		t.Fatalf("only the file:line finding is a line comment: %+v", p.Comments)
	}
	c := p.Comments[0]
	if c.Path != "a.go" || c.Line != 12 {
		t.Fatalf("comment anchor: %+v", c)
	}
	if !strings.Contains(c.Body, "merged migration edited") {
		t.Fatalf("comment body should carry the finding: %q", c.Body)
	}
	if c.Fingerprint == "" || !strings.Contains(c.Body, fpMarkerPrefix) {
		t.Fatalf("comment must carry its fingerprint marker: %q", c.Body)
	}
	if !Fingerprints([]string{c.Body})[c.Fingerprint] {
		t.Fatalf("the marker must decode back to this comment's fingerprint: %q", c.Body)
	}
	// The unlocated finding rides in the body, not as a comment.
	if !strings.Contains(p.Body, "migration edited after merge") {
		t.Fatalf("unlocated finding should be in the body: %q", p.Body)
	}
}

// hunkPatch is a patch touching new-file lines 12 to 14 (context, added,
// context) and, in a second hunk, lines 40 and 41. It carries no trailing
// newline, which is how GitHub's files API returns the patch field.
const hunkPatch = "@@ -10,3 +12,4 @@ func a()\n" +
	" ctx line 12\n" +
	"+added line 13\n" +
	"-removed old line\n" +
	" ctx line 14\n" +
	"@@ -38,2 +40,2 @@ func b()\n" +
	"+added line 40\n" +
	" ctx line 41"

func TestCommentableLinesParsesHunks(t *testing.T) {
	got := CommentableLines(map[string]string{"a.go": hunkPatch})["a.go"]
	for _, ln := range []int{12, 13, 14, 40, 41} {
		if !got[ln] {
			t.Fatalf("line %d should be commentable: %v", ln, got)
		}
	}
	if got[20] {
		t.Fatalf("line 20 is not in the diff and must not be commentable: %v", got)
	}
	// A removed line never advances the new-side counter, so line 15 (which does
	// not exist on the new side) is not commentable.
	if got[15] {
		t.Fatalf("no phantom commentable line from a removal: %v", got)
	}
	// The last hunk ends at line 41. Nothing beyond it is in the diff.
	if got[42] {
		t.Fatalf("no commentable line past the end of the last hunk: %v", got)
	}
}

// A newline-terminated patch leaves an empty final record when it is split.
// Counting that record as a context line marked line 42 commentable, and a
// finding anchored there would make GitHub reject the whole review.
func TestCommentableLinesIgnoresTrailingNewline(t *testing.T) {
	got := CommentableLines(map[string]string{"a.go": hunkPatch + "\n"})["a.go"]
	if got[42] {
		t.Fatalf("a trailing newline must not add a commentable line: %v", got)
	}
	if !got[41] {
		t.Fatalf("the real last line is still commentable: %v", got)
	}
}

// A blank line in the source arrives as a single space, not as an empty record,
// so requiring the space prefix does not drop it.
func TestCommentableLinesCountsBlankContextLine(t *testing.T) {
	patch := "@@ -1,3 +1,3 @@ func a()\n" +
		" first\n" +
		" \n" +
		"+third"
	got := CommentableLines(map[string]string{"a.go": patch})["a.go"]
	for _, ln := range []int{1, 2, 3} {
		if !got[ln] {
			t.Fatalf("line %d should be commentable: %v", ln, got)
		}
	}
}

func TestBuildDemotesOutOfDiffFindingToBody(t *testing.T) {
	// The report's line comment is on a.go:12, but the diff only touches a.go:99.
	commentable := map[string]map[int]bool{"a.go": {99: true}}
	p := Build(sampleReport(), prTarget(), "", commentable)

	if len(p.Comments) != 0 {
		t.Fatalf("a finding off the diff must not become a line comment: %+v", p.Comments)
	}
	if !strings.Contains(p.Body, "merged migration edited") {
		t.Fatalf("the demoted finding should appear in the body:\n%s", p.Body)
	}
	if !strings.Contains(p.Body, "a.go:12 — not on a changed line") {
		t.Fatalf("the body should say why it is not inline:\n%s", p.Body)
	}
}

func TestBuildKeepsInDiffFindingAsComment(t *testing.T) {
	commentable := map[string]map[int]bool{"a.go": {12: true}}
	p := Build(sampleReport(), prTarget(), "", commentable)
	if len(p.Comments) != 1 || p.Comments[0].Line != 12 {
		t.Fatalf("a finding on a changed line stays a line comment: %+v", p.Comments)
	}
}

func TestBuildBodyLeadsWithVerdictAndEvidenceTable(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "https://ci/report.html", nil)

	if !strings.HasPrefix(p.Body, "### Changes recommended\n") {
		t.Fatalf("body should lead with a verdict:\n%s", p.Body)
	}
	for _, want := range []string{
		"| Evidence | Result |",
		"| migrations | ran — 1 finding(s) |",
		"| openapi | did not apply |",
		"| diff coverage | not measured — no profile found |",
		"| files examined | 3/5 |",
		"[Full report](https://ci/report.html)",
		reviewMarkerPrefix + "deadbeef",
	} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, p.Body)
		}
	}
}

// A pane the repository has no files for gets no row: the table accounts for
// the checks that could have run, and "no linter is configured" is not one.
// The same for coverage on a change with nothing a profile could cover.
func TestBuildBodyOmitsWhatDoesNotApply(t *testing.T) {
	rep := sampleReport()
	if p := Build(rep, prTarget(), "", nil); strings.Contains(p.Body, "| lint |") {
		t.Fatalf("a not-applicable pane must not get a row:\n%s", p.Body)
	}
	rep.Coverage.CoverableFiles = 0
	if p := Build(rep, prTarget(), "", nil); strings.Contains(p.Body, "diff coverage") {
		t.Fatalf("coverage must not be mentioned when no changed file is coverable:\n%s", p.Body)
	}
}

// The posted review carries the agent's account of the change, which is what
// makes the pull request readable without opening the report: what the change
// does, then a line per file. Both are the agent's words, and the body says so
// — a reader who cannot tell measured evidence from written prose cannot tell
// which parts of the review are checkable.
func TestBuildBodyCarriesTheAgentsOverviewAndFileSummaries(t *testing.T) {
	rep := sampleReport()
	rep.Agent = &findings.AgentReview{
		Overview: "Adds a second nil rule and widens the first.",
		Files: map[string]string{
			"b.go": "Widens the guard rule to accept || chains.",
			"a.go": "New rule: a flow-fact scanner over one body.",
		},
	}
	p := Build(rep, prTarget(), "", nil)
	for _, want := range []string{
		"### What this change does",
		"Adds a second nil rule and widens the first.",
		"Written by the reviewing agent, not measured.",
		"Summary per file (2)",
		"| `a.go` | New rule: a flow-fact scanner over one body. |",
		// A pipe in a summary is escaped, or it ends the table cell early.
		"| `b.go` | Widens the guard rule to accept \\|\\| chains. |",
	} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, p.Body)
		}
	}
	// The evidence has to survive the prose: the verdict leads, and the table
	// and the markers a merge gate reads are still there.
	if !strings.HasPrefix(p.Body, "### Changes recommended\n") {
		t.Errorf("the verdict no longer leads the body:\n%s", p.Body)
	}
	for _, want := range []string{"| Evidence | Result |", reviewMarkerPrefix + "deadbeef"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("prose displaced %q", want)
		}
	}
}

// Redline writes no prose of its own. A run with no review must not grow a
// heading with nothing under it, and a summary map with only blank strings is
// not a summary.
func TestBuildBodyInventsNoNarrative(t *testing.T) {
	rep := sampleReport()
	if p := Build(rep, prTarget(), "", nil); strings.Contains(p.Body, "What this change does") ||
		strings.Contains(p.Body, "Summary per file") {
		t.Fatalf("a run with no agent review grew a narrative:\n%s", p.Body)
	}
	rep.Agent = &findings.AgentReview{Overview: "  ", Files: map[string]string{"a.go": "", "b.go": "   "}}
	if p := Build(rep, prTarget(), "", nil); strings.Contains(p.Body, "What this change does") ||
		strings.Contains(p.Body, "Summary per file") {
		t.Fatalf("blank prose produced a section:\n%s", p.Body)
	}
}

// A body over 65536 characters is rejected by GitHub, and the prose is written
// before the findings and the gate markers. A change with a summary per file of
// a hundred files must not be the reason a finding never reaches the review.
func TestBuildBodyBoundsTheNarrativeSoEvidenceSurvives(t *testing.T) {
	rep := sampleReport()
	files := map[string]string{}
	for i := range 400 {
		files[fmt.Sprintf("pkg/file%03d.go", i)] = strings.Repeat("a long summary of this file. ", 12)
	}
	rep.Agent = &findings.AgentReview{Overview: strings.Repeat("overview. ", 8000), Files: files}
	p := Build(rep, prTarget(), "", nil)
	if len(p.Body) >= 65536 {
		t.Errorf("body is %d characters, which GitHub rejects", len(p.Body))
	}
	for _, want := range []string{"| Evidence | Result |", "more file(s) summarised", reviewMarkerPrefix + "deadbeef"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("body missing %q at length %d", want, len(p.Body))
		}
	}
}

// A pane that applied and did not run is named in the body, loudly. That row is
// what makes the posted review's scope verifiable from the pull request alone.
func TestBuildBodyNamesAFailedPane(t *testing.T) {
	rep := sampleReport()
	rep.Substrates = append(rep.Substrates, findings.SubstrateStatus{
		Name: "openapi-diff", State: findings.SubstrateFailed, Detail: "spec parse error",
	})
	p := Build(rep, prTarget(), "", nil)
	if !strings.Contains(p.Body, "| openapi-diff | **did not run** — spec parse error |") {
		t.Fatalf("a failed pane must be named in the body:\n%s", p.Body)
	}
}

func TestBuildOmitsReportLinkWhenEmpty(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	if strings.Contains(p.Body, "Full report") {
		t.Fatalf("no link should render when reportURL is empty:\n%s", p.Body)
	}
}

func TestUnpostedDropsAlreadyPostedComments(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	if len(p.Comments) != 1 {
		t.Fatalf("precondition: one comment, got %d", len(p.Comments))
	}
	fp := p.Comments[0].Fingerprint

	// Nothing posted yet: the comment survives.
	if got := p.Unposted(map[string]bool{}); len(got.Comments) != 1 {
		t.Fatalf("unposted with empty set should keep the comment: %+v", got.Comments)
	}
	// Already posted for this head: the comment is filtered, so a re-post never
	// duplicates it.
	got := p.Unposted(map[string]bool{fp: true})
	if len(got.Comments) != 0 {
		t.Fatalf("already-posted comment must be dropped: %+v", got.Comments)
	}
	// The body is intact; the evidence table is worth restating.
	if got.Body != p.Body {
		t.Fatal("Unposted must not alter the body")
	}
}

// Idempotency is by fingerprint across the whole pull request, not per commit:
// a finding posted on any earlier commit is not said again. Posting once and
// letting GitHub keep the thread is quieter than re-anchoring on every push,
// and it is what stops the "this change skips a test" pane finding the field
// posted and dismissed across two versions from returning.
func TestUnpostedSuppressesRegardlessOfCommit(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	fp := p.Comments[0].Fingerprint
	if got := p.Unposted(map[string]bool{fp: true}); len(got.Comments) != 0 {
		t.Fatalf("a finding already said on the PR must not be repeated: %+v", got.Comments)
	}
}

// A finding that rides in the body is tracked like a line comment. Only located
// findings used to carry a marker, so a session whose new finding had no line
// produced no new comments and the command reported nothing to post.
func TestBodyFindingCarriesFingerprintMarker(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)

	var bodyFP string
	for _, f := range sampleReport().Findings {
		if f.File == "" {
			bodyFP = f.Fingerprint
		}
	}
	if bodyFP == "" {
		t.Fatal("precondition: the sample report has an unlocated finding")
	}
	if !Fingerprints([]string{p.Body})[bodyFP] {
		t.Fatalf("the body finding needs a marker recoverable from the body:\n%s", p.Body)
	}
	if p.NothingNew() {
		t.Fatal("a payload with a body finding has something new to say")
	}
}

// Once the body finding is posted for this commit, a re-post drops it and says
// nothing new, so the same text does not reappear in a second review body.
func TestUnpostedDropsAlreadyPostedBodyFinding(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	posted := Fingerprints([]string{p.Body, p.Comments[0].Body})

	got := p.Unposted(posted)
	if !got.NothingNew() {
		t.Fatalf("everything was posted, so nothing is new: %+v", got.Comments)
	}
	if strings.Contains(got.Body, "migration edited after merge") {
		t.Fatalf("an already-posted body finding must not render again:\n%s", got.Body)
	}
	// The evidence table is not a finding and still leads the body.
	if !strings.Contains(got.Body, "| Evidence | Result |") {
		t.Fatalf("the evidence table survives filtering:\n%s", got.Body)
	}
}

func TestFingerprintsRoundTripFromBodies(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	bodies := []string{p.Comments[0].Body, "an unrelated human comment"}
	got := Fingerprints(bodies)
	if !got[p.Comments[0].Fingerprint] {
		t.Fatalf("fingerprint should be recovered from the comment body: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("no phantom fingerprints from plain text: %v", got)
	}
}

// Markers written before the SHA joined the key carry one hex run instead of
// two. They must not decode to anything, or the fingerprint would be read as a
// SHA and match nothing while looking like it matched something.
func TestFingerprintsIgnoresMarkerWithoutHeadSHA(t *testing.T) {
	old := "<!-- " + fpMarkerPrefix + "6162630064006d7367" + " -->"
	if got := Fingerprints([]string{old}); len(got) != 0 {
		t.Fatalf("a marker with no head SHA must not parse: %v", got)
	}
}

func TestReviewedAtMatchesHeadSHA(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	if !ReviewedAt([]string{p.Body}, "deadbeef") {
		t.Fatal("should recognize the review marker for this head")
	}
	if ReviewedAt([]string{p.Body}, "cafef00d") {
		t.Fatal("a different head SHA is a new push, not already reviewed")
	}
	if ReviewedAt([]string{"no marker here"}, "deadbeef") {
		t.Fatal("no marker means not reviewed")
	}
	if ReviewedAt([]string{p.Body}, "") {
		t.Fatal("an empty head is never 'already reviewed'")
	}
}

func TestCommentBodyIncludesSuggestion(t *testing.T) {
	rep := sampleReport()
	rep.Findings[0].Suggestion = "return err"
	p := Build(rep, prTarget(), "", nil)
	if !strings.Contains(p.Comments[0].Body, "```suggestion\nreturn err\n```") {
		t.Fatalf("comment should carry a suggestion block:\n%s", p.Comments[0].Body)
	}
}

func TestCommentKeepsStartLineWhenCommentable(t *testing.T) {
	rep := sampleReport()
	rep.Findings[0].StartLine = 10
	rep.Findings[0].Line = 12
	commentable := map[string]map[int]bool{"a.go": {10: true, 11: true, 12: true}}
	p := Build(rep, prTarget(), "", commentable)
	if len(p.Comments) != 1 || p.Comments[0].StartLine != 10 || p.Comments[0].Line != 12 {
		t.Fatalf("ranged comment: %+v", p.Comments)
	}
}

func TestCommentDropsStartLineOutsideDiff(t *testing.T) {
	rep := sampleReport()
	rep.Findings[0].StartLine = 1
	rep.Findings[0].Line = 12
	commentable := map[string]map[int]bool{"a.go": {12: true}}
	p := Build(rep, prTarget(), "", commentable)
	if len(p.Comments) != 1 || p.Comments[0].StartLine != 0 {
		t.Fatalf("start outside the diff must fall back to a single line: %+v", p.Comments)
	}
}

func TestBuildAttestStampsFailMarkers(t *testing.T) {
	prof := &Profile{
		ReviewMarker:  "example-agent-review:v1",
		FindingMarker: "example-agent-finding:v1",
		Blocking:      []findings.Severity{findings.SeverityError, findings.SeverityWarning},
	}
	p := BuildAttest(sampleReport(), prTarget(), "", nil, prof, nil)
	if p.GateVerdict != "fail" {
		t.Fatalf("error and warning should fail, got %q", p.GateVerdict)
	}
	if !strings.Contains(p.Comments[0].Body, "example-agent-finding:v1 severity=high") {
		t.Fatalf("error comment needs finding marker:\n%s", p.Comments[0].Body)
	}
	if !strings.Contains(p.Body, "example-agent-finding:v1 severity=medium") {
		t.Fatalf("warning in the body needs finding marker:\n%s", p.Body)
	}
	if !strings.Contains(p.Body, "example-agent-review:v1 verdict=fail head=deadbeef") {
		t.Fatalf("review body needs fail marker:\n%s", p.Body)
	}
}

func TestBuildAttestPassHasNoFindingMarkers(t *testing.T) {
	prof := &Profile{
		ReviewMarker:  "example-agent-review:v1",
		FindingMarker: "example-agent-finding:v1",
		Blocking:      []findings.Severity{findings.SeverityError, findings.SeverityWarning},
	}
	rep := &findings.Report{Findings: []findings.Finding{{
		File: "a.go", Line: 1, Rule: "note", Severity: findings.SeverityInfo, Message: "coverage unknown",
	}}}
	rep.Finalize()
	p := BuildAttest(rep, prTarget(), "", nil, prof, nil)
	if p.GateVerdict != "pass" {
		t.Fatalf("info-only should pass, got %q", p.GateVerdict)
	}
	if strings.Contains(p.Comments[0].Body, "example-agent-finding:v1") {
		t.Fatalf("info must not carry a gate finding marker:\n%s", p.Comments[0].Body)
	}
	if !strings.Contains(p.Body, "verdict=pass head=deadbeef") {
		t.Fatalf("pass marker missing:\n%s", p.Body)
	}
}

// CRLF line endings must not shift the commentable-line count or break hunk
// parsing: the "+" and " " prefixes are unaffected by a trailing \r.
func TestCommentableLinesHandlesCRLF(t *testing.T) {
	patch := "@@ -1,3 +1,3 @@\r\n ctx\r\n+add\r\n ctx2\r\n"
	got := CommentableLines(map[string]string{"a.go": patch})["a.go"]
	for _, ln := range []int{1, 2, 3} {
		if !got[ln] {
			t.Fatalf("line %d should be commentable: %v", ln, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("CRLF must not add phantom commentable lines: %v", got)
	}
}

// Git's "no newline at end of file" marker must not become a phantom
// commentable line past the hunk it follows.
func TestCommentableLinesIgnoresNoNewlineMarker(t *testing.T) {
	patch := "@@ -1,2 +1,2 @@\n+add\n\\ No newline at end of file"
	got := CommentableLines(map[string]string{"a.go": patch})["a.go"]
	if len(got) != 1 || !got[1] {
		t.Fatalf("only line 1 should be commentable: %v", got)
	}
}

// A posted finding's label names its source: pane-measured or agent review.
func TestFindingLabelNamesTheAgent(t *testing.T) {
	f := findings.Finding{Severity: findings.SeverityWarning, Source: findings.SourceLLM}
	if got := findingLabel(f); got != "Warning · agent" {
		t.Fatalf("got %q", got)
	}
	f.Source = findings.SourceDeterministic
	if got := findingLabel(f); got != "Warning" {
		t.Fatalf("got %q", got)
	}
}

// The reviewer's own doubt keeps a finding off the pull request at info and
// nowhere else. A defect held back is lost and a wrong one costs the author a
// minute reading it, so an unsure warning posts and carries its doubt, while
// an unsure info remark folds the way the report folds it.
func TestBuildPostsAnUnsureWarningAndWithholdsAnUnsureInfo(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityWarning, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceLow, Message: "this might race"},
			{File: "a.go", Line: 4, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityWarning, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceHigh, Message: "this leaks a file handle"},
			{File: "a.go", Line: 5, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityInfo, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceLow, Message: "this name reads oddly"},
		},
	}
	rep.Finalize()

	p := Build(rep, prTarget(), "", nil)

	if len(p.Comments) != 2 {
		t.Fatalf("both warnings post, whatever their confidence: %+v", p.Comments)
	}
	var bodies string
	for _, c := range p.Comments {
		bodies += c.Body
	}
	for _, want := range []string{"might race", "leaks a file handle"} {
		if !strings.Contains(bodies, want) {
			t.Errorf("%q did not reach the diff", want)
		}
	}
	if strings.Contains(p.Body, "reads oddly") {
		t.Error("the withheld info remark must not fall through into the body either")
	}
	// Counted, not hidden. A reader who is not told it exists cannot tell a
	// reviewer that held something back from one that had nothing to say.
	if !strings.Contains(p.Body, "1 further finding(s)") {
		t.Errorf("the body should say what was withheld:\n%s", p.Body)
	}
}

// A ruling that did not keep a finding stops it at every severity. Running the
// verifying pass and then posting what it refused is paying for a check and
// ignoring it, and doubt is a separate question from what the repository said.
func TestBuildWithholdsARefusedFindingAtEverySeverity(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityError, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceHigh, Message: "this drops the second write",
				Ruling: findings.VerifiedWithdrawn},
			{File: "a.go", Line: 4, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityError, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceHigh, Message: "this leaks a file handle",
				Ruling: findings.VerifiedKept},
		},
	}
	rep.Finalize()

	p := Build(rep, prTarget(), "", nil)

	if len(p.Comments) != 1 {
		t.Fatalf("only the kept finding posts: %+v", p.Comments)
	}
	if !strings.Contains(p.Comments[0].Body, "leaks a file handle") {
		t.Fatalf("the wrong finding survived: %q", p.Comments[0].Body)
	}
	if strings.Contains(p.Body, "drops the second write") {
		t.Error("a withdrawn finding must not fall through into the body either")
	}
	// Not even as a count. "1 said they were uncertain" was the note it got,
	// which was wrong about why and gave the reader nothing to act on.
	if strings.Contains(p.Body, "further finding(s)") {
		t.Errorf("a ruled-out finding must leave no note in the body:\n%s", p.Body)
	}
}

// An error remains visible even when the reviewer did not name a way to settle
// it. A suspected defect must not disappear from the pull request.
func TestBuildPostsAnUnfalsifiableError(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityError, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceHigh, Message: "this feels wrong somehow",
				Question: findings.Question{Kind: findings.QuestionNone}},
		},
	}
	rep.Finalize()

	p := Build(rep, prTarget(), "", nil)

	if len(p.Comments) != 1 {
		t.Fatalf("an error must remain visible on the diff: %+v", p.Comments)
	}
	if !strings.Contains(p.Comments[0].Body, "this feels wrong somehow") {
		t.Fatalf("the error did not reach the author: %q", p.Comments[0].Body)
	}
}

// A high-confidence error on a line outside the diff is visible in the review
// body, where GitHub can show it without rejecting the review comment payload.
func TestBuildPostsHighConfidenceErrorOutsideDiffInBody(t *testing.T) {
	rep := &findings.Report{Findings: []findings.Finding{
		{File: "a.go", Line: 3, Rule: "agent-comment", Substrate: "redline/review",
			Severity: findings.SeverityError, Source: findings.SourceLLM,
			Confidence: findings.ConfidenceHigh, Message: "this drops the retry idempotency key",
			Question: findings.Question{Kind: findings.QuestionDiff}},
	}}
	rep.Finalize()

	p := Build(rep, prTarget(), "", map[string]map[int]bool{"a.go": {99: true}})
	if len(p.Comments) != 0 {
		t.Fatalf("an out-of-diff error must not become a line comment: %+v", p.Comments)
	}
	if !strings.Contains(p.Body, "### Findings not shown inline") ||
		!strings.Contains(p.Body, "this drops the retry idempotency key") {
		t.Fatalf("the out-of-diff error must be visible in the review body:\n%s", p.Body)
	}
}

// Confidence is the reviewer's own word about its own finding. A pane's
// finding has none, and Finalize clears any a review file tried to smuggle
// onto one, so this path can never withhold a measurement.
func TestBuildPostsMeasurementsWhateverConfidenceWasWritten(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "migration-modified-after-merge", Substrate: "migrations",
				Severity: findings.SeverityError, Confidence: findings.ConfidenceLow,
				Message: "merged migration edited"},
		},
	}
	rep.Finalize()

	p := Build(rep, prTarget(), "", nil)

	if len(p.Comments) != 1 {
		t.Fatalf("a measurement posts regardless: %+v", p.Comments)
	}
	if strings.Contains(p.Body, "further finding(s)") {
		t.Fatal("nothing was withheld, so the body must not say anything was")
	}
}

// A line comment is an interruption: it opens a thread somebody has to close.
// The second field round posted fifteen comments of which eight were the
// reviewer's info, and every one was a thread the team triaged to learn that
// nothing was wrong. Those ride in the body now.
func TestReviewerInfoRidesInTheBodyNotOnTheDiff(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityInfo, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceHigh, Message: "row_index is zero-based here"},
			{File: "a.go", Line: 4, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityWarning, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceHigh, Message: "this leaks a file handle"},
		},
	}
	rep.Finalize()
	p := Build(rep, prTarget(), "", nil)

	if len(p.Comments) != 1 || !strings.Contains(p.Comments[0].Body, "leaks a file handle") {
		t.Fatalf("only the warning belongs on the diff: %+v", p.Comments)
	}
	// Nothing is lost. It is read once, by whoever is reading the review.
	if !strings.Contains(p.Body, "zero-based") {
		t.Fatalf("the info finding fell off the review entirely:\n%s", p.Body)
	}
}

// A measurement earns a line at any severity: it is a fact about the change
// and the line is where the fact is.
func TestAPanesInfoStillGetsItsLine(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "test-skip-added", Substrate: "redline/test-delta",
				Severity: findings.SeverityInfo, Message: "a skip was added here"},
		},
	}
	rep.Finalize()
	if p := Build(rep, prTarget(), "", nil); len(p.Comments) != 1 {
		t.Fatalf("a measurement is not a reviewer's opinion: %+v", p.Comments)
	}
}

// The prompt forbids hedging and the ruling is meant to catch what is left.
// Both are a model judging its own writing, and the field produced a warning
// that called itself "acceptable but worth noting" and posted anyway.
func TestAHedgedFindingDoesNotPost(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "agent-comment", Substrate: "redline/review",
				Severity: findings.SeverityWarning, Source: findings.SourceLLM,
				Confidence: findings.ConfidenceHigh,
				Message:    "Acceptable but worth noting: the ordinal is second-resolution."},
		},
	}
	rep.Finalize()
	p := Build(rep, prTarget(), "", nil)

	if len(p.Comments) != 0 {
		t.Fatalf("a finding that says it might not matter must not interrupt anyone: %+v", p.Comments)
	}
	if strings.Contains(p.Body, "second-resolution") {
		t.Fatal("a hedge is withheld, not demoted to the body")
	}
	if !strings.Contains(p.Body, "1 hedged") {
		t.Fatalf("what was withheld has to be counted: %s", p.Body)
	}

	// With body_include: low-confidence a hedge folds with the other unsure
	// findings instead, and is then not counted as withheld.
	on := BuildAttest(rep, prTarget(), "", nil, &Profile{BodyInclude: map[string]bool{"low-confidence": true}}, nil)
	if len(on.Comments) != 0 {
		t.Fatalf("a folded hedge is still not a line comment: %+v", on.Comments)
	}
	if !strings.Contains(on.Body, "Low confidence (1)") || !strings.Contains(on.Body, "second-resolution") {
		t.Fatalf("the hedge should fold into the low-confidence section:\n%s", on.Body)
	}
	if strings.Contains(on.Body, "hedged") {
		t.Fatalf("a folded hedge must not also be counted as withheld:\n%s", on.Body)
	}
}

// A pane's message is fixed text written by whoever wrote the pane, so reading
// it for hedging would be reading the wrong author's prose. The test-delta
// pane's own wording says exactly this.
func TestAPanesOwnWordingIsNotReadAsAHedge(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "package-untested", Substrate: "redline/test-delta",
				Severity: findings.SeverityInfo,
				Message:  "Not necessarily a problem: an existing test may already cover the change."},
		},
	}
	rep.Finalize()
	if p := Build(rep, prTarget(), "", nil); len(p.Comments) != 1 {
		t.Fatalf("a pane's finding is not the reviewer hedging: %+v", p.Comments)
	}
}

// The findings that could not be anchored ride in the body, and a
// finding-heavy change can list more of them than GitHub's 65536-character
// body allows. The list must truncate with a visible count rather than take
// the whole review down with it: the evidence table, the report link and the
// gate marker all sit after the list and have to survive.
//
// Not built from lint findings: those are counted and not posted, so a body
// made of them has no list to bound.
func TestBuildBodyBoundsTheNotShownListSoTheReviewPosts(t *testing.T) {
	rep := &findings.Report{
		Substrates: []findings.SubstrateStatus{{Name: "test-delta", State: findings.SubstrateRan}},
	}
	for i := range 400 {
		rep.Findings = append(rep.Findings, findings.Finding{
			Rule: "package-untested", Substrate: "redline/test-delta", Severity: findings.SeverityWarning,
			Message: fmt.Sprintf("finding %03d: %s", i, strings.Repeat("a wordy account of this one line. ", 20)),
		})
	}
	rep.Finalize()
	p := Build(rep, prTarget(), "", nil)
	if len(p.Body) > 65536 {
		t.Fatalf("body is %d characters, which GitHub rejects", len(p.Body))
	}
	if !strings.Contains(p.Body, "truncated to fit") {
		t.Fatalf("a truncated list must say so")
	}
	if !strings.Contains(p.Body, reviewMarkerPrefix+"deadbeef") {
		t.Fatalf("the review marker must survive truncation")
	}
}

// A reviewer's comment on a removed line carries side LEFT. It must reach the
// payload as LEFT: posting it RIGHT lands the thread on the new-file line of
// the same number, which is different code.
func TestBuildCarriesTheCommentSide(t *testing.T) {
	rep := &findings.Report{Findings: []findings.Finding{{
		File: "a.go", Line: 3, Side: "LEFT", Rule: "agent-comment", Substrate: "redline/review",
		Category: findings.CategoryReview, Severity: findings.SeverityWarning,
		Message: "this deleted guard was load-bearing", Source: findings.SourceLLM,
	}}}
	rep.Finalize()
	p := Build(rep, prTarget(), "", map[string]map[int]bool{"a.go": {3: true}})
	if len(p.Comments) != 1 {
		t.Fatalf("want one line comment: %+v", p.Comments)
	}
	if p.Comments[0].Side != "LEFT" {
		t.Fatalf("the LEFT side was dropped: %q", p.Comments[0].Side)
	}
}

// For a verified finding Context is the ruling's quoted repository evidence.
// A hedge word in the code a kept finding quotes must not withhold it: only the
// reviewer's own Message is the reviewer's writing.
func TestAHedgeQuotedInContextDoesNotWithhold(t *testing.T) {
	f := findings.Finding{
		File: "a.go", Line: 3, Rule: "agent-comment", Substrate: "redline/review",
		Category: findings.CategoryReview, Severity: findings.SeverityWarning,
		Message: "this comparison can never be true because the types differ",
		Context: "Checked and kept. (quoted from the diff: // nit: probably fine)",
		Source:  findings.SourceLLM,
	}
	if hedged(f) {
		t.Fatal("a hedge quoted in Context must not read as the reviewer hedging")
	}
	if !Reaches(f) {
		t.Fatal("the kept finding should still reach the author")
	}
}

func walkthroughProfile(include ...string) *Profile {
	inc := map[string]bool{}
	for _, s := range include {
		inc[s] = true
	}
	return &Profile{
		ReviewMarker:  "example-agent-review:v1",
		FindingMarker: "example-agent-finding:v1",
		Blocking:      []findings.Severity{findings.SeverityError, findings.SeverityWarning},
		BodyStyle:     BodyWalkthrough,
		BodyInclude:   inc,
	}
}

// The walkthrough body reads like the author-published reviews: reviewed-by and
// commit, stated intent, what it does, a walkthrough of every changed file
// (agent summary or "No notes."), evidence folded, and the opted-in coverage,
// lint, confirmations and unknowns from the report the run already wrote.
func TestWalkthroughBodyMatchesCopilotOrder(t *testing.T) {
	rep := &findings.Report{
		Substrates: []findings.SubstrateStatus{{Name: "lint", State: findings.SubstrateRan}},
		Findings: []findings.Finding{
			{File: "a.go", Line: 5, Rule: "x", Substrate: "redline/lint",
				Severity: findings.SeverityWarning, Message: "a warning"},
		},
		Confirmations: []findings.Confirmation{
			{Substrate: "redline/sql", Rule: "immutable", Message: "migrations byte-identical"},
		},
		Unknowns: []findings.Unknown{
			{Substrate: "openapi", Message: "spec uses $ref", Reason: "inline only"},
		},
		Coverage: findings.Coverage{CoverableFiles: 1, Diff: &cover.Result{
			Uncovered: []cover.FileGap{{Path: "a.go", Lines: []int{5, 6}}},
		}},
		Agent: &findings.AgentReview{Overview: "Adds a feed.", Files: map[string]string{"a.go": "Staging."}},
	}
	rep.Finalize()
	prof := walkthroughProfile("coverage", "lint", "confirmations", "unknowns", "intent", "evidence", "line-counts")
	p := BuildAttest(rep, prTarget(), "", nil, prof, changedFiles("a.go", "b.go")).
		WithMeta("TICKET-1: do a thing", "bot[bot]")
	for _, want := range []string{
		"### Review findings",
		"**Reviewed by.** bot[bot] on `deadbeef`.",
		"**Stated intent.** TICKET-1: do a thing",
		"**What it does.** Adds a feed.",
		"<summary>Walkthrough</summary>",
		"| `a.go` | +10\u00a0−0 | 2 | 1 warning | Staging. |",
		"| `b.go` | +11\u00a0−1 | — | — | — |\n",
		"| File | Lines | Uncovered | Findings | What changed |",
		"<summary>Evidence</summary>",
		"Checks that passed (1)",
		"Could not determine (1)",
	} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("walkthrough body missing %q:\n%s", want, p.Body)
		}
	}
}

// The extra columns and sections are opt-in: a lean walkthrough is file plus
// summary, and nothing else, so a repository dials in only what it wants.
func TestWalkthroughLeanByDefault(t *testing.T) {
	rep := &findings.Report{
		Confirmations: []findings.Confirmation{{Message: "a passed check"}},
		Agent:         &findings.AgentReview{Files: map[string]string{"a.go": "x"}},
	}
	rep.Finalize()
	p := BuildAttest(rep, prTarget(), "", nil, walkthroughProfile(), changedFiles("a.go"))
	if !strings.Contains(p.Body, "<summary>Walkthrough</summary>") {
		t.Fatal("the walkthrough table is always present in walkthrough mode")
	}
	if strings.Contains(p.Body, "Coverage |") {
		t.Fatalf("coverage is opt-in:\n%s", p.Body)
	}
	if strings.Contains(p.Body, "Checks that passed") {
		t.Fatalf("confirmations are opt-in:\n%s", p.Body)
	}
}

// The default post body (no profile, or body_style evidence) is unchanged.
func TestEvidenceBodyIsUnchangedByWalkthroughCode(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	if !strings.HasPrefix(p.Body, "### ") || strings.Contains(p.Body, "<summary>Walkthrough</summary>") {
		t.Fatalf("evidence body must not render a walkthrough:\n%s", p.Body)
	}
}

// Test files have a section of their own rather than being left out. Grouped
// under a heading they answer the question a reviewer opens the walkthrough
// with, which is how much of the change is test; it was interleaving them with
// the code they test that made them noise.
func TestWalkthroughGroupsTestFilesOfTheirOwn(t *testing.T) {
	rep := &findings.Report{Agent: &findings.AgentReview{Files: map[string]string{"a.go": "real"}}}
	rep.Finalize()
	changed := []change.File{
		{Path: "a.go", Language: "go", Added: 30, Removed: 4},
		{Path: "a_test.go", Language: "go", Added: 80, Removed: 1},
		{Path: "ui/x.test.tsx", Language: "tsx", Added: 12, Removed: 0},
	}
	p := BuildAttest(rep, prTarget(), "", nil, walkthroughProfile("line-counts"), changed)
	for _, want := range []string{
		"**go test** (1 file(s), +80\u00a0−1)",
		"| `a_test.go` | +80\u00a0−1 | — |\n",
		"**go source** (1 file(s), +30\u00a0−4)",
		"| `a.go` | +30\u00a0−4 | real |",
		"**tsx test** (1 file(s), +12\u00a0−0)",
	} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("walkthrough missing %q:\n%s", want, p.Body)
		}
	}
	// The test group's files sit under the test heading, not under the source
	// one, so a reader can stop at the section they care about.
	src := strings.Index(p.Body, "**go source**")
	tst := strings.Index(p.Body, "**go test**")
	if src < 0 || tst < 0 || strings.Index(p.Body, "`a_test.go`") < tst {
		t.Fatalf("a test file belongs under the test heading:\n%s", p.Body)
	}
	// Most added lines first, which is the order the HTML report draws.
	if tst > src {
		t.Fatalf("the dominant group leads:\n%s", p.Body)
	}
}

// A body finding that names a shown file rides under that file inside the
// walkthrough; a fileless one stays in the flat not-shown list, and the file
// finding is not duplicated there.
func TestWalkthroughAttachesFileFindingsToTheFile(t *testing.T) {
	rep := &findings.Report{
		Findings: []findings.Finding{
			{File: "a.go", Rule: "source-without-test", Substrate: "redline/test-delta",
				Severity: findings.SeverityInfo, Message: "complexity 16 here"},
			{Rule: "package-untested", Substrate: "redline/parity",
				Severity: findings.SeverityInfo, Message: "no test in that package"},
		},
		Agent: &findings.AgentReview{Files: map[string]string{"a.go": "did a thing"}},
	}
	rep.Finalize()
	p := BuildAttest(rep, prTarget(), "", nil, walkthroughProfile(), changedFiles("a.go"))
	body := p.Body
	if !strings.Contains(body, "**`a.go`**") || !strings.Contains(body, "complexity 16 here") {
		t.Fatalf("a file finding must ride under its file in the walkthrough:\n%s", body)
	}
	idx := strings.Index(body, "### Findings not shown inline")
	if idx < 0 || !strings.Contains(body[idx:], "no test in that package") {
		t.Fatalf("a fileless finding stays in the not-shown list:\n%s", body)
	}
	if idx >= 0 && strings.Contains(body[idx:], "complexity 16 here") {
		t.Fatalf("a file finding must not also appear in the not-shown list:\n%s", body[idx:])
	}
}

// The lint pane is recorded and not posted. Every violation it finds is one
// the author's own linter reports at the same moment CI does, and on a change
// with ten of them they outnumbered the review and were read as the review.
// The count still has to reach the reader: a pane that ran and said nothing
// must stay distinguishable from one whose findings went elsewhere.
func TestLintFindingsAreCountedRatherThanPosted(t *testing.T) {
	rep := &findings.Report{
		Substrates: []findings.SubstrateStatus{{Name: "lint", State: findings.SubstrateRan}},
		Findings: []findings.Finding{
			{File: "a.go", Line: 3, Rule: "gorefactor/god-object", Substrate: "redline/lint",
				Severity: findings.SeverityWarning, Message: "Struct Options has 27 fields"},
			{File: "a.go", Line: 4, Rule: "agent-comment", Substrate: "redline/review",
				Source: findings.SourceLLM, Confidence: findings.ConfidenceHigh,
				Severity: findings.SeverityError, Message: "failures[0] panics when the slice is empty"},
		},
	}
	rep.Finalize()
	commentable := map[string]map[int]bool{"a.go": {3: true, 4: true}}
	p := Build(rep, prTarget(), "", commentable)

	if len(p.Comments) != 1 {
		t.Fatalf("only the review's own finding opens a thread, got %d: %+v", len(p.Comments), p.Comments)
	}
	if !strings.Contains(p.Comments[0].Body, "failures[0] panics") {
		t.Fatalf("the surviving comment must be the reviewer's: %q", p.Comments[0].Body)
	}
	if strings.Contains(p.Body, "god-object") || strings.Contains(p.Body, "27 fields") {
		t.Fatalf("a lint finding must not be listed in the body either:\n%s", p.Body)
	}
	if !strings.Contains(p.Body, "1 lint finding(s) are on the report") {
		t.Fatalf("the body must say how many went to the report:\n%s", p.Body)
	}
}

// With body_include: low-confidence, a finding the reviewer was unsure of
// folds into a collapsed block in the body instead of being withheld. It is
// still never a line comment, even on a changed line, and it is no longer
// counted as withheld to the report.
func TestLowConfidenceFoldsIntoTheBodyWhenIncluded(t *testing.T) {
	rep := &findings.Report{Findings: []findings.Finding{{
		File: "a.go", Line: 1, Rule: "agent-comment", Substrate: "redline/review",
		Severity: findings.SeverityInfo, Source: findings.SourceLLM,
		Confidence: findings.ConfidenceLow, Message: "nil deref on the returned pointer",
	}}}
	rep.Finalize()
	commentable := map[string]map[int]bool{"a.go": {1: true}}

	off := BuildAttest(rep, prTarget(), "", commentable, &Profile{}, changedFiles("a.go"))
	if len(off.Comments) != 0 {
		t.Fatalf("a low-confidence finding is never a line comment: %+v", off.Comments)
	}
	if strings.Contains(off.Body, "nil deref on the returned pointer") {
		t.Fatalf("without the opt it is withheld, not shown:\n%s", off.Body)
	}
	if !strings.Contains(off.Body, "1 said they were uncertain") {
		t.Fatalf("without the opt the withheld count is noted:\n%s", off.Body)
	}

	on := BuildAttest(rep, prTarget(), "", commentable,
		&Profile{BodyInclude: map[string]bool{"low-confidence": true}}, changedFiles("a.go"))
	if len(on.Comments) != 0 {
		t.Fatalf("folded low-confidence stays body-only, never a comment: %+v", on.Comments)
	}
	if !strings.Contains(on.Body, "<summary>Low confidence (1)</summary>") ||
		!strings.Contains(on.Body, "nil deref on the returned pointer") {
		t.Fatalf("with the opt it folds behind a chevron:\n%s", on.Body)
	}
	if strings.Contains(on.Body, "said they were uncertain") {
		t.Fatalf("a folded finding must not also be reported as withheld:\n%s", on.Body)
	}
}

func TestUnfalsifiableWarningFoldsIntoTheBodyWhenIncluded(t *testing.T) {
	rep := &findings.Report{Findings: []findings.Finding{{
		File: "a.go", Line: 1, Rule: "agent-comment", Substrate: "redline/review",
		Severity: findings.SeverityWarning, Source: findings.SourceLLM,
		Confidence: findings.ConfidenceLow, Message: "the migration may hold a long lock",
		Question: findings.Question{Kind: findings.QuestionNone},
	}}}
	rep.Finalize()

	p := BuildAttest(rep, prTarget(), "", map[string]map[int]bool{"a.go": {1: true}},
		&Profile{BodyInclude: map[string]bool{"low-confidence": true}}, changedFiles("a.go"))
	if len(p.Comments) != 0 {
		t.Fatalf("an unfalsifiable warning must stay out of line comments: %+v", p.Comments)
	}
	if !strings.Contains(p.Body, "<summary>Low confidence (1)</summary>") ||
		!strings.Contains(p.Body, "the migration may hold a long lock") {
		t.Fatalf("an unfalsifiable warning should fold behind a chevron:\n%s", p.Body)
	}
	if strings.Contains(p.Body, "said they were uncertain") {
		t.Fatalf("a folded warning must not also be reported as withheld:\n%s", p.Body)
	}
}

// What the change is made of reaches the pull request, not just the HTML
// report. A reviewer who can see that most of the added lines are tests knows
// what they are about to read before opening the diff, and GitHub's own Files
// tab will not tell them: it lists paths and leaves the adding up to the
// reader.
func TestBodyCarriesTheComposition(t *testing.T) {
	files := []change.File{
		{Path: "internal/feed/feed.go", Language: "go", Added: 120, Removed: 8},
		{Path: "internal/feed/feed_test.go", Language: "go", Added: 200, Removed: 0},
		{Path: "README.md", Language: "markdown", Added: 4, Removed: 2},
	}
	p := BuildAttest(sampleReport(), prTarget(), "", nil, nil, files)
	for _, want := range []string{
		"**Lines by language and type.**",
		"| Language | Type | Files | + | − |",
		"| go | test | 1 | 200 | 0 |",
		"| go | source | 1 | 120 | 8 |",
		"| markdown | docs | 1 | 4 | 2 |",
	} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("composition row %q missing from the body:\n%s", want, p.Body)
		}
	}
	// The walkthrough layout says the same thing in its own headings, so it
	// does not also carry the table.
	w := BuildAttest(sampleReport(), prTarget(), "", nil, walkthroughProfile("line-counts"), files)
	if strings.Contains(w.Body, "**Lines by language and type.**") {
		t.Fatalf("the walkthrough headings replace the table, not sit under it:\n%s", w.Body)
	}
	for _, want := range []string{
		"**go test** (1 file(s), +200\u00a0−0)",
		"**go source** (1 file(s), +120\u00a0−8)",
		"**markdown docs** (1 file(s), +4\u00a0−2)",
	} {
		if !strings.Contains(w.Body, want) {
			t.Fatalf("walkthrough heading %q missing:\n%s", want, w.Body)
		}
	}
}

// A post with no file list renders no table at all, rather than a header with
// nothing under it. That is every offline preview and every caller that still
// uses Build.
func TestCompositionOmittedWithoutFiles(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	if strings.Contains(p.Body, "| Language | Type |") {
		t.Fatalf("no files means no composition table:\n%s", p.Body)
	}
}

// The table is written into the part of the body that is kept whole, so its
// length is capped rather than trusted. A change touching more languages than
// the cap has the rest summed into one row, and the counts still add up.
func TestCompositionCapsItsRows(t *testing.T) {
	var files []change.File
	for i := 0; i < maxCompositionRows+3; i++ {
		files = append(files, change.File{
			Path:     fmt.Sprintf("a%d/x.src", i),
			Language: fmt.Sprintf("lang%02d", i),
			Added:    maxCompositionRows + 3 - i,
			Removed:  1,
		})
	}
	body := compositionSection(files)
	// The capped rows, and one row for the rest.
	if n := strings.Count(body, "\n| "); n != maxCompositionRows+2 {
		t.Fatalf("want %d rows under the header, got %d:\n%s",
			maxCompositionRows+1, n-1, body)
	}
	if !strings.Contains(body, "| 3 more | | 3 | 6 | 3 |") {
		t.Fatalf("the rest must be counted rather than dropped:\n%s", body)
	}
}

// With line-counts, how many lines each file moved rides in the walkthrough
// table, beside the file it is about.
func TestWalkthroughCarriesPerFileLines(t *testing.T) {
	rep := &findings.Report{Agent: &findings.AgentReview{Files: map[string]string{"a.go": "Staging."}}}
	rep.Finalize()
	p := BuildAttest(rep, prTarget(), "", nil, walkthroughProfile("line-counts"),
		[]change.File{{Path: "a.go", Language: "go", Added: 12, Removed: 3}})
	if !strings.Contains(p.Body, "| `a.go` | +12\u00a0−3 | Staging. |") {
		t.Fatalf("the walkthrough item must say how many lines moved:\n%s", p.Body)
	}
	// The two counts are one word, so a narrow window cannot put them on
	// separate lines.
	if strings.Contains(p.Body, "+12 −3") {
		t.Fatalf("the line counts must be joined by a non-breaking space:\n%s", p.Body)
	}
}

// With body_include: diff-links, a walkthrough row links to its file on the
// pull request's Files tab, anchored by the SHA-256 of the path. Test files
// stay unlinked, and without the key no row is linked.
func TestWalkthroughDiffLinks(t *testing.T) {
	rep := &findings.Report{}
	rep.Finalize()
	files := changedFiles("a.go", "a_test.go")

	p := BuildAttest(rep, prTarget(), "", nil, walkthroughProfile("diff-links"), files)
	// The anchor is the hex SHA-256 of "a.go".
	want := "| [`a.go`](https://github.com/o/r/pull/7/files#diff-" +
		"ffc4fd9bc24722ba464194a85b255d4b50945f3e68a120122e11f6cdae4a8c19) |"
	if !strings.Contains(p.Body, want) {
		t.Fatalf("a.go should link to its diff, want %q:\n%s", want, p.Body)
	}
	if !strings.Contains(p.Body, "| `a_test.go` | — |") {
		t.Fatalf("a test file should stay unlinked:\n%s", p.Body)
	}

	plain := BuildAttest(rep, prTarget(), "", nil, walkthroughProfile(), files)
	if strings.Contains(plain.Body, "/files#diff-") {
		t.Fatalf("without diff-links no row should link:\n%s", plain.Body)
	}
}

// A walkthrough with no body_include is the lean one. The stated intent, the
// evidence table and the line counts used to be written every time; the pull
// request already shows its own description, and GitHub's Files tab already
// gives each file's counts, so they are opt-in.
func TestWalkthroughLeavesOutWhatItWasNotAskedFor(t *testing.T) {
	rep := sampleReport()
	rep.Agent = &findings.AgentReview{Overview: "Adds a feed.", Files: map[string]string{"a.go": "Staging."}}
	p := BuildAttest(rep, prTarget(), "", nil, walkthroughProfile(), changedFiles("a.go")).
		WithMeta("TICKET-1: do a thing", "bot[bot]")
	for _, gone := range []string{
		"Stated intent", "TICKET-1",
		"<summary>Evidence</summary>",
		"Lines by language and type",
		"+10",
	} {
		if strings.Contains(p.Body, gone) {
			t.Errorf("the lean walkthrough should not carry %q:\n%s", gone, p.Body)
		}
	}
	for _, want := range []string{"**What it does.** Adds a feed.", "**go source** (1 file(s))", "| `a.go` | Staging. |"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("the lean walkthrough is missing %q:\n%s", want, p.Body)
		}
	}

	// composition puts the evidence body's table into the walkthrough.
	c := BuildAttest(rep, prTarget(), "", nil, walkthroughProfile("composition"), changedFiles("a.go"))
	if !strings.Contains(c.Body, "| go | source | 1 | 10 | 0 |") {
		t.Errorf("composition should add the lines-by-language table:\n%s", c.Body)
	}
}
