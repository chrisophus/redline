package post

import (
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/target"
)

func sampleReport() *findings.Report {
	rep := &findings.Report{
		Coverage: findings.Coverage{ChangedFiles: 5, ExaminedFiles: 3},
		Substrates: []findings.SubstrateStatus{
			{Name: "migrations", State: findings.SubstrateRan},
			{Name: "openapi", State: findings.SubstrateSkipped, Detail: "no files in scope for this pane"},
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
	if !Fingerprints([]string{c.Body})[postedKey("deadbeef", c.Fingerprint)] {
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

// A pane that applied and did not run is named in the body, loudly. That row is
// what makes the posted review's scope verifiable from the pull request alone.
func TestBuildBodyNamesADarkPane(t *testing.T) {
	rep := sampleReport()
	rep.Substrates = append(rep.Substrates, findings.SubstrateStatus{
		Name: "openapi-diff", State: findings.SubstrateFailed, Detail: "spec parse error",
	})
	p := Build(rep, prTarget(), "", nil)
	if !strings.Contains(p.Body, "| openapi-diff | **did not run** — spec parse error |") {
		t.Fatalf("a dark pane must be named in the body:\n%s", p.Body)
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
	got := p.Unposted(map[string]bool{postedKey("deadbeef", fp): true})
	if len(got.Comments) != 0 {
		t.Fatalf("already-posted comment must be dropped: %+v", got.Comments)
	}
	// The body is intact; the evidence table is worth restating.
	if got.Body != p.Body {
		t.Fatal("Unposted must not alter the body")
	}
}

// The posted set is keyed on the commit as well as the finding. A comment left
// on an earlier commit must not suppress the one this commit needs: the finding
// is still live, GitHub collapses the old comment as outdated, and the reviewer
// would see nothing.
func TestUnpostedKeepsCommentWhenOnlyAnEarlierCommitHasIt(t *testing.T) {
	p := Build(sampleReport(), prTarget(), "", nil)
	fp := p.Comments[0].Fingerprint

	posted := map[string]bool{postedKey("0ldc0mm1t", fp): true}
	if got := p.Unposted(posted); len(got.Comments) != 1 {
		t.Fatalf("a comment on a superseded commit must not suppress this one: %+v", got.Comments)
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
	if !Fingerprints([]string{p.Body})[postedKey("deadbeef", bodyFP)] {
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
	if !got[postedKey("deadbeef", p.Comments[0].Fingerprint)] {
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
		ReviewMarker:  "mct-agent-review:v1",
		FindingMarker: "mct-agent-finding:v1",
		Blocking:      []findings.Severity{findings.SeverityError, findings.SeverityWarning},
	}
	p := BuildAttest(sampleReport(), prTarget(), "", nil, prof)
	if p.GateVerdict != "fail" {
		t.Fatalf("error and warning should fail, got %q", p.GateVerdict)
	}
	if !strings.Contains(p.Comments[0].Body, "mct-agent-finding:v1 severity=high") {
		t.Fatalf("error comment needs finding marker:\n%s", p.Comments[0].Body)
	}
	if !strings.Contains(p.Body, "mct-agent-finding:v1 severity=medium") {
		t.Fatalf("warning in the body needs finding marker:\n%s", p.Body)
	}
	if !strings.Contains(p.Body, "mct-agent-review:v1 verdict=fail head=deadbeef") {
		t.Fatalf("review body needs fail marker:\n%s", p.Body)
	}
}

func TestBuildAttestPassHasNoFindingMarkers(t *testing.T) {
	prof := &Profile{
		ReviewMarker:  "mct-agent-review:v1",
		FindingMarker: "mct-agent-finding:v1",
		Blocking:      []findings.Severity{findings.SeverityError, findings.SeverityWarning},
	}
	rep := &findings.Report{Findings: []findings.Finding{{
		File: "a.go", Line: 1, Rule: "note", Severity: findings.SeverityInfo, Message: "coverage unknown",
	}}}
	rep.Finalize()
	p := BuildAttest(rep, prTarget(), "", nil, prof)
	if p.GateVerdict != "pass" {
		t.Fatalf("info-only should pass, got %q", p.GateVerdict)
	}
	if strings.Contains(p.Comments[0].Body, "mct-agent-finding:v1") {
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
