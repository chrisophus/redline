package post

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// withoutDetails is the body as a reader sees it before opening anything: the
// collapsed Details block cut out, the markers after it kept.
func withoutDetails(body string) string {
	const open = "<details>\n<summary>Details</summary>"
	at := strings.Index(body, open)
	if at < 0 {
		return body
	}
	end := strings.Index(body[at:], "</details>\n\n</details>\n\n")
	if end < 0 {
		end = strings.Index(body[at:], "</details>\n\n")
		return body[:at] + body[at+end+len("</details>\n\n"):]
	}
	return body[:at] + body[at+end+len("</details>\n\n</details>\n\n"):]
}

func shapeReport(overview string, fs ...findings.Finding) *findings.Report {
	rep := &findings.Report{
		Findings:      fs,
		Confirmations: []findings.Confirmation{{Message: "a passed check"}},
		Agent:         &findings.AgentReview{Overview: overview, Files: map[string]string{"a.go": "did a thing"}},
	}
	rep.Finalize()
	return rep
}

func shapeProfile() *Profile {
	return walkthroughProfile("intent", "evidence", "confirmations", "unknowns")
}

func warning(msg string) findings.Finding {
	return findings.Finding{File: "a.go", Line: 5, Rule: "x", Substrate: "redline/sql",
		Severity: findings.SeverityWarning, Message: msg}
}

func TestFirstReviewIsShortAndFoldsTheRest(t *testing.T) {
	overview := strings.Repeat("The change moves the retry loop into the client and adds a backoff. ", 30)
	rep := shapeReport(overview, warning("one"))
	intent := "Add backoff\n\n" + strings.Repeat("A long description the pull request already shows. ", 40)
	p := BuildAttest(rep, prTarget(), "", map[string]map[int]bool{"a.go": {5: true}}, shapeProfile(), changedFiles("a.go")).
		WithMeta(intent, "bot[bot]")

	visible := withoutDetails(p.Body)
	if len(visible) > 1500 {
		t.Errorf("a first review without its details is %d characters, want 1500 or fewer:\n%s", len(visible), visible)
	}
	if strings.Count(p.Body, "<summary>Details</summary>") != 1 {
		t.Errorf("want one Details block:\n%s", p.Body)
	}
	for _, want := range []string{"### Review findings", "Reviewed by bot[bot]"} {
		if !strings.Contains(visible, want) {
			t.Errorf("the summary is missing %q:\n%s", want, visible)
		}
	}
	if strings.Contains(p.Body, "A long description") {
		t.Errorf("the pull request description must not be quoted:\n%s", p.Body)
	}
	// Nothing is dropped, only folded: the whole overview, the intent title,
	// the walkthrough, the evidence and the checks are all in the details.
	_, inside, found := strings.Cut(p.Body, "<summary>Details</summary>")
	if !found {
		t.Fatalf("no Details block:\n%s", p.Body)
	}
	for _, want := range []string{overview[:200], "**Stated intent.** Add backoff", "<summary>Walkthrough</summary>", "Checks that passed (1)"} {
		if !strings.Contains(inside, want) {
			t.Errorf("the details are missing %q", want)
		}
	}
}

func TestLaterHeadReportsOnlyTheDelta(t *testing.T) {
	rep := shapeReport("Overview that was already said.", warning("still"), warning("fresh"))
	still, fresh := rep.Findings[0], rep.Findings[1]
	commentable := map[string]map[int]bool{"a.go": {5: true}}
	p := BuildAttest(rep, prTarget(), "", commentable, shapeProfile(), changedFiles("a.go"))
	posted := map[string]bool{still.Fingerprint: true, "fp-gone-1": true, "fp-gone-2": true, "fp-old": true}
	// "fp-old" was fixed at an earlier head and "conversation:x" is not a finding.
	previous := map[string]bool{still.Fingerprint: true, "fp-gone-1": true, "fp-gone-2": true, "conversation:x:open": true}
	p = p.WithDelta(posted, previous).Unposted(posted)

	if !strings.Contains(p.Body, "Reviewed `deadbeef`: 2 finding(s) fixed, 1 new, 1 still open.") {
		t.Errorf("the body should carry the delta:\n%s", p.Body)
	}
	// The gate here fails, so the heading stays above the delta line.
	if p.GateVerdict != "fail" || !strings.HasPrefix(p.Body, "### Review findings\n\nReviewed ") {
		t.Errorf("a failing gate keeps its heading on a later head:\n%s", p.Body)
	}
	visible := withoutDetails(p.Body)
	if len(visible) > 800 {
		t.Errorf("a delta without its details is %d characters, want 800 or fewer:\n%s", len(visible), visible)
	}
	if strings.Contains(visible, "Overview that was already said") || strings.Contains(visible, "### Review complete") {
		t.Errorf("a later head must not repeat the summary:\n%s", visible)
	}
	if len(p.Comments) != 1 || p.Comments[0].Fingerprint != fresh.Fingerprint {
		t.Errorf("only the new finding posts inline: %+v", p.Comments)
	}
}

func TestReviewWithNoFindingsIsOneLine(t *testing.T) {
	rep := shapeReport("Adds a feed.")
	p := BuildAttest(rep, prTarget(), "", nil, shapeProfile(), changedFiles("a.go"))
	visible := withoutDetails(p.Body)
	if !strings.HasPrefix(visible, "Reviewed `deadbeef`: no findings.\n\n") {
		t.Errorf("want the one-line review:\n%s", visible)
	}
	if len(visible) > 300 {
		t.Errorf("a review with no findings is %d characters without its details:\n%s", len(visible), visible)
	}
	if !strings.Contains(p.Body, "<summary>Details</summary>") {
		t.Errorf("the details should still be there:\n%s", p.Body)
	}

	// Findings already on the pull request are not "no findings".
	rep = shapeReport("Adds a feed.", warning("old"))
	p = BuildAttest(rep, prTarget(), "", map[string]map[int]bool{"a.go": {5: true}}, shapeProfile(), changedFiles("a.go"))
	p = p.Unposted(map[string]bool{rep.Findings[0].Fingerprint: true})
	if strings.Contains(p.Body, "no findings") {
		t.Errorf("a re-post with everything already said is not a review with no findings:\n%s", p.Body)
	}
}

func TestLongFindingIsCutIntoSummaryAndWhy(t *testing.T) {
	sentence := "The retry loop never resets its counter after a success. "
	long := strings.Repeat(sentence, 20)
	f := warning(strings.TrimSpace(long))
	f.Context = "Seen in the second call path."
	body := commentBody(f, "deadbeef", nil)

	at := strings.Index(body, "<details>\n<summary>Why</summary>")
	if at < 0 {
		t.Fatalf("a long finding needs a Why block:\n%s", body)
	}
	if got := len(strings.TrimSpace(body[:at])); got > 400 {
		t.Errorf("the summary is %d characters, want 400 or fewer", got)
	}
	why := body[at:]
	if !strings.Contains(why, "Seen in the second call path.") {
		t.Errorf("the context belongs in the Why block:\n%s", why)
	}
	if !strings.Contains(body, "redline:fp:deadbeef:") {
		t.Errorf("the fingerprint marker must stay:\n%s", body)
	}
	// Cut at a sentence end, with the rest of the finding in the block.
	if !strings.HasSuffix(strings.TrimSpace(body[:at]), "success.") {
		t.Errorf("the summary should end on a sentence:\n%s", body[:at])
	}
	if strings.Count(body, sentence[:30]) < 19 {
		t.Errorf("nothing may be lost, only folded away")
	}

	short := commentBody(warning("A short one."), "deadbeef", nil)
	if strings.Contains(short, "<details>") {
		t.Errorf("a short finding has no Why block:\n%s", short)
	}
}

func TestLongFindingWithNoSentenceEndStillCuts(t *testing.T) {
	head, rest := splitAt(strings.Repeat("word ", 200), 100)
	if len(head) > 100 || !strings.HasSuffix(head, "…") || !strings.HasPrefix(rest, "…word") || len(rest) < 880 {
		t.Errorf("head %d bytes %q, rest %d bytes", len(head), head, len(rest))
	}
}

func TestMarkersAreUnchangedByTheNewShapes(t *testing.T) {
	prof := shapeProfile()
	prof.ReviewMarker = "redline-review:v1"
	prof.FindingMarker = "redline-finding:v1"
	rep := shapeReport("Adds a feed.", warning("one"))
	first := BuildAttest(rep, prTarget(), "", map[string]map[int]bool{"a.go": {5: true}}, prof, changedFiles("a.go"))
	later := first.WithDelta(map[string]bool{}, nil)
	clean := BuildAttest(shapeReport("Adds a feed."), prTarget(), "", nil, prof, changedFiles("a.go"))
	for name, p := range map[string]Payload{"first": first, "later": later, "clean": clean} {
		for _, want := range []string{
			"<!-- redline:review:deadbeef -->",
			"<!-- redline-review:v1 verdict=" + p.GateVerdict + " head=deadbeef -->",
		} {
			if !strings.Contains(p.Body, want) {
				t.Errorf("%s review is missing %q:\n%s", name, want, p.Body)
			}
		}
	}
	if !strings.Contains(first.Comments[0].Body, "redline:fp:deadbeef:") ||
		!strings.Contains(first.Comments[0].Body, "<!-- redline-finding:v1") {
		t.Errorf("the finding markers moved:\n%s", first.Comments[0].Body)
	}
}

func TestBodyFindingWhyKeepsWordBreaks(t *testing.T) {
	msg := strings.Repeat("Filler sentence here. ", 30) + "foo\nbar\n\nbaz"
	line := bodyFindingLine(warning(msg), "deadbeef", nil)
	if !strings.Contains(line, "foo bar baz") {
		t.Errorf("line breaks must become spaces:\n%s", line)
	}
}

func TestBodyFindingWhyFollowsTheItemLine(t *testing.T) {
	f := warning(strings.Repeat("Filler sentence here. ", 30))
	f.Rule = "r"
	line := bodyFindingLine(f, "deadbeef", nil)
	first, after, _ := strings.Cut(line, "\n")
	if !strings.Contains(first, "redline:fp:deadbeef:") || strings.Contains(first, "<details>") {
		t.Errorf("the item line must hold the location and marker and no HTML block:\n%s", line)
	}
	if !strings.HasPrefix(after, "  <details>\n  <summary>Why</summary>\n\n  ") || !strings.Contains(after, "  </details>") {
		t.Errorf("the Why block must be indented under the item:\n%s", after)
	}
}

func TestDeltaDoesNotCountConversationExchanges(t *testing.T) {
	rep := shapeReport("Adds a feed.", warning("one"))
	p := BuildAttest(rep, prTarget(), "", map[string]map[int]bool{"a.go": {5: true}}, shapeProfile(), changedFiles("a.go"))
	p.Comments = append(p.Comments, Comment{Path: "a.go", Line: 5, Fingerprint: "conversation:q1:open"},
		Comment{Path: "a.go", Line: 5, Fingerprint: "conversation:q2:replied"})
	p = p.WithDelta(map[string]bool{"conversation:q2:replied": true}, nil)
	if p.delta.New != 1 || p.delta.Open != 0 {
		t.Errorf("only the finding counts: %+v", *p.delta)
	}
}

func TestMidSentenceCutDoesNotRepeatTheHead(t *testing.T) {
	msg := strings.Repeat("alpha beta gamma ", 60)
	head, rest := splitAt(msg, 100)
	words := func(s string) int { return len(strings.Fields(strings.ReplaceAll(s, "…", " "))) }
	if words(head)+words(rest) != words(msg) {
		t.Errorf("head and rest must add up to the message, not repeat it:\n%q\n%q", head, rest)
	}
}
