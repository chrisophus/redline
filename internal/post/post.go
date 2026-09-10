// Package post turns a finished Redline session into one GitHub pull request
// review and submits it. It is the deliberate, explicit counterpart to the
// rest of Redline, which only ever reads from GitHub: nothing here runs unless
// the operator typed `redline post`, and `run` never reaches it.
//
// The review a reviewer reads has two parts. Findings that carry a file and a
// line become line-anchored comments; the body opens with a one-line-per-pane
// account of the evidence — what ran, what it found, what did not run — then
// any finding that could not be anchored to a line, and a link to the full
// report.
//
// This file is the payload: pure functions from a Report to what should be
// posted, with no knowledge of gh or the network. The command layer does the
// I/O so this stays testable without a GitHub token.
package post

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/target"
)

// Event is the GitHub review event. Redline posts COMMENT: it reports and does
// not gate, so it never requests changes or approves on its own.
const Event = "COMMENT"

// fpMarkerPrefix tags every comment with the head SHA it was posted for and its
// finding fingerprint, invisibly (an HTML comment), so a re-post can see what it
// already said and not say it twice. The rendered form is
// "redline:fp:<head sha>:<hex fingerprint>".
//
// The SHA is part of the key because findings.Fingerprint is file, rule and
// normalized message with no commit in it, while GitHub keeps every comment ever
// left on the pull request. Keyed on the fingerprint alone, a finding that is
// still live after a new push looks already-posted against a comment attached to
// a superseded commit, so it gets no comment on the new head and reads as fixed.
//
// The fingerprint itself is a human-readable string with NUL separators, so it
// is hex-encoded rather than embedded raw, where a "-->" in a message could
// close the comment early.
const fpMarkerPrefix = "redline:fp:"

// qMarkerPrefix tags a comment with the question its finding asked, so a later
// review can recognise the same claim in different words.
//
// Deliberately without the head SHA that fpMarkerPrefix carries, and the
// difference is the point. Idempotency needs the SHA, because a finding still
// live after a push has to be said again for the new commit. Recognising that
// a claim has already been made and answered must not reset on a push, and
// must not reset on a re-review of the same commit either: the second field
// round posted seven fresh comments on an unchanged head, two of them
// rewordings of threads the author had already replied to.
//
// The question is what survives rewording. A fingerprint is file plus
// normalised message, and the message is the thing that moves; "checks before
// the transaction" and "CopyFrom cannot ON CONFLICT" are one claim about one
// race with no words in common. The question both would ask is the same.
const qMarkerPrefix = "redline:q:"

// reviewMarkerPrefix tags the review body with the head SHA it was posted for,
// so a re-post against the same commit can tell "already reviewed this commit"
// from "reviewing a new push."
const reviewMarkerPrefix = "redline:review:"

var (
	// Two hex runs separated by a colon: the head SHA, then the encoded
	// fingerprint. A marker written before the SHA was part of the key has only
	// one run and deliberately does not match, so those findings post once more
	// against the current head rather than being read as posted for it.
	fpMarkerRe     = regexp.MustCompile(regexp.QuoteMeta(fpMarkerPrefix) + `([0-9a-fA-F]+):([0-9a-fA-F]+)`)
	reviewMarkerRe = regexp.MustCompile(regexp.QuoteMeta(reviewMarkerPrefix) + `([0-9a-fA-F]+)`)
	qMarkerRe      = regexp.MustCompile(regexp.QuoteMeta(qMarkerPrefix) + `([0-9a-fA-F]+)`)
)

// postedKey is the identity a re-post checks against: a finding is already said
// for this commit, or it is not. Keys from Fingerprints and lookups from
// Unposted must be built the same way, so both go through here.
func postedKey(head, fingerprint string) string {
	return head + "\x00" + fingerprint
}

// Comment is one line-anchored review comment. Body already carries the
// fingerprint marker; Fingerprint is kept alongside for filtering.
type Comment struct {
	Path        string
	Line        int
	StartLine   int // zero means Line alone; otherwise the range start
	Body        string
	Fingerprint string
}

// Payload is one PR review: a body and the line-anchored comments. CommitID is
// the head SHA the review is anchored to, the same SHA idempotency keys on.
type Payload struct {
	CommitID string
	Body     string
	Comments []Comment
	// GateVerdict is pass or fail when a Profile was supplied, else empty.
	GateVerdict string

	// What the body was rendered from, kept so Unposted can drop already-posted
	// body findings and render it again. A body finding is tracked the same way
	// a line comment is, and filtering it means rebuilding the string it sits in.
	rep          *findings.Report
	reportURL    string
	bodyFindings []findings.Finding
	profile      *Profile
	// withheld counts the reviewer's own findings this payload did not post
	// because they said they were unsure, and hedged those it withheld for
	// hedging. Kept apart because they are different failures and a reader
	// tuning the thing wants to know which one they have.
	withheld int
	hedged   int
}

// NothingNew reports that this payload has no finding Redline has not already
// said for this commit, counting the ones that ride in the body. The caller
// still has to post when the commit has never been reviewed, because that is
// how the evidence summary arrives.
func (p Payload) NothingNew() bool {
	return len(p.Comments) == 0 && len(p.bodyFindings) == 0
}

// Build assembles the review from a finished report. reportURL is a link to the
// full HTML report (a CI artifact URL when the caller has one); it is omitted
// from the body when empty rather than rendered as a dead link.
//
// commentable is the set of line numbers per file that a review comment may
// anchor to — GitHub's diff, from CommentableLines. A finding becomes a line
// comment only when its line is in that set; every other finding rides in the
// body, so one finding pointing off the diff can never 422 the whole review. A
// nil commentable means "do not filter" — used offline, where there is no diff
// to check against and the payload is only being previewed.
func Build(rep *findings.Report, tgt *target.Target, reportURL string, commentable map[string]map[int]bool) Payload {
	return BuildAttest(rep, tgt, reportURL, commentable, nil)
}

// BuildAttest is Build with a merge-gate profile. A nil profile is identical
// to Build. When set, the body and blocking comments carry the profile's
// hidden markers, and GateVerdict is pass or fail.
func BuildAttest(rep *findings.Report, tgt *target.Target, reportURL string, commentable map[string]map[int]bool, prof *Profile) Payload {
	head := ""
	if tgt != nil {
		head = tgt.Head
	}

	p := Payload{CommitID: head, GateVerdict: GateVerdict(rep, prof), profile: prof}

	var inBody []findings.Finding
	findingsList := []findings.Finding(nil)
	if rep != nil {
		findingsList = rep.Findings
	}
	for _, f := range findingsList {
		if lowConfidence(f) {
			p.withheld++
			continue
		}
		if hedged(f) {
			p.hedged++
			continue
		}
		if f.File != "" && f.Line > 0 && lineOnDiff(f) &&
			lineCommentable(commentable, f.File, f.Line) {
			start := f.StartLine
			if start > 0 && (start > f.Line || !lineCommentable(commentable, f.File, start)) {
				// A start line outside the diff, or past the end line, would
				// 422 the whole review. Fall back to the end line alone.
				start = 0
			}
			p.Comments = append(p.Comments, Comment{
				Path:        f.File,
				Line:        f.Line,
				StartLine:   start,
				Body:        commentBody(f, head, prof),
				Fingerprint: f.Fingerprint,
			})
			continue
		}
		inBody = append(inBody, f)
	}

	p.rep = rep
	p.reportURL = reportURL
	p.bodyFindings = inBody
	p.Body = buildBody(rep, head, reportURL, inBody, prof, p.GateVerdict, p.withheld, p.hedged)
	return p
}

// Reaches reports whether a finding would reach the author at all: as a line
// comment, or in the review body.
//
// The three gates below decide that between them, and every one of them is a
// rule about the reviewer's own findings rather than about measurements. It is
// exported because the eval has to score the review a reader receives rather
// than the one a model wrote. Those were the same thing until this pass
// existed; now a configuration can look worse on paper and be better in an
// inbox, and a scoring function with its own copy of these rules would report
// whichever of the two it happened to implement.
func Reaches(f findings.Finding) bool {
	return !lowConfidence(f) && !hedged(f)
}

// Interrupts reports whether a finding would open a thread on the diff. A
// finding can reach the author without interrupting them: the reviewer's info
// rides in the body, read once by whoever is reading the review.
func Interrupts(f findings.Finding) bool {
	return Reaches(f) && lineOnDiff(f) && f.File != "" && f.Line > 0
}

// lowConfidence reports whether a finding is one the reviewer itself said it
// was unsure of.
//
// The report folds these away and the pull request did not, so a guess the
// page hid arrived on the change with the weight of a measurement. The two
// readers now agree: what the report will not show without being asked is not
// worth a reviewer's inbox.
//
// Only the reviewer's own findings. A pane's finding carries no confidence at
// all, by Finalize, so this can never withhold a measurement.
func lowConfidence(f findings.Finding) bool {
	return f.Source == findings.SourceLLM && f.Confidence == findings.ConfidenceLow
}

// lineOnDiff reports whether a finding has earned a line comment.
//
// A line comment is an interruption. It lands in an inbox, it opens a thread
// somebody has to close, and it sits on the code until they do. A measurement
// earns that at any severity, because it is a fact about the change and the
// line is where the fact is. A reviewer's info does not: the second field
// round posted fifteen comments of which eight were info, and every one of
// them was a thread the team had to triage to learn that nothing was wrong.
//
// Nothing is lost. An info finding rides in the review body, where it is read
// once by whoever is reading the review, and it is on the report in full.
func lineOnDiff(f findings.Finding) bool {
	return f.Source != findings.SourceLLM || f.Severity != findings.SeverityInfo
}

// hedges are the phrases a finding uses when it has nothing to say.
//
// The prompt already forbids raising anything qualified with "may", "might" or
// "could potentially" and not followed by a consequence, and the ruling stage
// is supposed to mark what is left unverifiable. Both are a model's judgement
// about its own writing, and the field round produced a warning that called
// itself "acceptable but worth noting" and posted anyway. This is the backstop
// that costs nothing: a finding that says out loud it is not sure it matters
// is a finding whose author has told you not to interrupt anyone with it.
//
// Whole phrases rather than words. "Consider" alone appears in real findings
// about code that considers something, and a list that fires on it would
// withhold them.
var hedges = []string{
	"acceptable but",
	"but worth noting",
	"worth noting that",
	"not necessarily a problem",
	"probably fine",
	"may want to consider",
	"might want to consider",
	"you may wish to",
	"could potentially",
	"is fine, but",
	"is not a problem, but",
	"nit:",
}

// hedged reports whether a reviewer's finding disqualified itself.
//
// Its own findings only. A pane's message is fixed text written by whoever
// wrote the pane, and a rule that read it for hedging would be reading the
// wrong author's prose.
func hedged(f findings.Finding) bool {
	if f.Source != findings.SourceLLM {
		return false
	}
	body := strings.ToLower(f.Message + " " + f.Context)
	for _, h := range hedges {
		if strings.Contains(body, h) {
			return true
		}
	}
	return false
}

// verdictFor is the review's headline, derived from finding severities alone.
func verdictFor(rep *findings.Report) string {
	if rep == nil || len(rep.Findings) == 0 {
		return "No findings"
	}
	for _, f := range rep.Findings {
		if f.Severity == findings.SeverityError || f.Severity == findings.SeverityWarning {
			return "Changes recommended"
		}
	}
	return "Comments"
}

// lineCommentable reports whether a finding's line is one GitHub will accept a
// comment on. A nil map means no diff was fetched (offline preview), so nothing
// is filtered.
func lineCommentable(m map[string]map[int]bool, file string, line int) bool {
	if m == nil {
		return true
	}
	return m[file][line]
}

// commentBody renders one finding as a line comment, ending in its hidden
// fingerprint marker so a re-post against the same head can skip it.
func commentBody(f findings.Finding, head string, prof *Profile) string {
	var b strings.Builder
	if m := findingAttestMarker(prof, f); m != "" {
		b.WriteString(m)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "**%s** — %s", findingLabel(f), f.Message)
	if ctx := strings.TrimSpace(f.Context); ctx != "" {
		b.WriteString("\n\n")
		b.WriteString(ctx)
	}
	if sug := strings.TrimSpace(f.Suggestion); sug != "" {
		b.WriteString("\n\n```suggestion\n")
		b.WriteString(sug)
		if !strings.HasSuffix(sug, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n")
	}
	b.WriteString("\n")
	b.WriteString(fpMarker(head, f.Fingerprint))
	if m := qMarker(f); m != "" {
		b.WriteString(m)
	}
	return b.String()
}

// qMarker renders the question a finding asked, when it asked one. Hex-encoded
// for the reason the fingerprint is: a subject containing "-->" would close
// the HTML comment early and put the rest of it on the page.
func qMarker(f findings.Finding) string {
	key := f.Question.Key()
	if key == "" {
		return ""
	}
	return marker(qMarkerPrefix + hex.EncodeToString([]byte(key)))
}

// fpMarker renders the hidden marker that identifies one finding as said for one
// commit.
func fpMarker(head, fingerprint string) string {
	return marker(fpMarkerPrefix + head + ":" + hex.EncodeToString([]byte(fingerprint)))
}

// buildBody is the review body a reviewer reads first: the verdict, the
// evidence table — one line per pane, plus the coverage rows — then any finding
// that could not be anchored to a line, and the report link.
func buildBody(rep *findings.Report, head, reportURL string, inBody []findings.Finding, prof *Profile, gateVerdict string, withheld, hedged int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n", verdictFor(rep))
	// The agent's own account of the change, when there is one. Redline never
	// writes prose: an overview is present only because a review was run or a
	// human wrote one into review.json, so it is attributed rather than shown
	// as the tool's own conclusion.
	if s := overviewSection(rep); s != "" {
		b.WriteString(s)
	}
	if table := evidenceTable(rep); table != "" {
		b.WriteString(table)
		b.WriteString("\n")
	}
	// One row per file the agent described. Deliberately not a list of every
	// changed path with its line counts: GitHub's own Files tab already says
	// that, and repeating it would pad the review with what the reader can
	// see. A file reaches this table because someone wrote a sentence about
	// it.
	if s := fileSummarySection(rep); s != "" {
		b.WriteString(s)
	}
	if len(inBody) > 0 {
		b.WriteString("### Findings not shown inline\n\n")
		for _, f := range inBody {
			if m := findingAttestMarker(prof, f); m != "" {
				fmt.Fprintf(&b, "%s\n", m)
			}
			fmt.Fprintf(&b, "- **%s** — %s", findingLabel(f), f.Message)
			if loc := bodyLocation(f); loc != "" {
				fmt.Fprintf(&b, " _(%s)_", loc)
			}
			fmt.Fprintf(&b, " %s\n", fpMarker(head, f.Fingerprint))
		}
		b.WriteString("\n")
	}
	// Named rather than dropped silently, the bargain generated files and test
	// bodies already get on the review request. A reader who is not told these
	// exist cannot tell a reviewer that held something back from one that had
	// nothing to say.
	if withheld > 0 || hedged > 0 {
		var parts []string
		if withheld > 0 {
			parts = append(parts, fmt.Sprintf("%d said they were uncertain", withheld))
		}
		if hedged > 0 {
			parts = append(parts, fmt.Sprintf("%d hedged", hedged))
		}
		fmt.Fprintf(&b, "_%d further finding(s) from the reviewer are on the report "+
			"rather than here: %s._\n\n", withheld+hedged, strings.Join(parts, ", "))
	}
	if reportURL != "" {
		fmt.Fprintf(&b, "[Full report](%s)\n\n", reportURL)
	}
	b.WriteString(marker(reviewMarkerPrefix + head))
	if m := reviewAttestMarker(prof, gateVerdict, head); m != "" {
		b.WriteByte('\n')
		b.WriteString(m)
	}
	return b.String()
}

// maxNarrative bounds what the agent's prose may take of the review body.
// GitHub rejects a body over 65536 characters, and the parts that carry the
// tool's own evidence -- the verdict, the pane table, the findings that could
// not be anchored, the markers a merge gate reads -- are written after this
// one and must not be the thing that gets cut. A change with a hundred file
// summaries is exactly when the findings matter most.
const maxNarrative = 20_000

// overviewSection is the agent's account of what the change does, attributed
// to it. Redline writes no prose of its own, so a run with no review has no
// section here rather than a heading with nothing under it.
func overviewSection(rep *findings.Report) string {
	if rep == nil || rep.Agent == nil {
		return ""
	}
	overview := strings.TrimSpace(rep.Agent.Overview)
	if overview == "" {
		return ""
	}
	if len(overview) > maxNarrative {
		overview = overview[:maxNarrative] + "\n\n_(truncated; the full overview is on the report)_"
	}
	return "### What this change does\n\n" + overview + "\n\n_Written by the reviewing agent, not measured._\n\n"
}

// fileSummarySection is one row per file the agent described. It is collapsed
// because it is orientation rather than evidence: a reader who wants it opens
// it, and a reader chasing a finding is not made to scroll past it.
func fileSummarySection(rep *findings.Report) string {
	if rep == nil || rep.Agent == nil || len(rep.Agent.Files) == 0 {
		return ""
	}
	paths := make([]string, 0, len(rep.Agent.Files))
	for path, summary := range rep.Agent.Files {
		if strings.TrimSpace(summary) != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)
	var b strings.Builder
	fmt.Fprintf(&b, "<details>\n<summary>Summary per file (%d), written by the agent</summary>\n\n", len(paths))
	b.WriteString("| File | What changed |\n|---|---|\n")
	var omitted int
	for _, path := range paths {
		row := fmt.Sprintf("| `%s` | %s |\n", path, escapeCell(rep.Agent.Files[path]))
		if b.Len()+len(row) > maxNarrative {
			omitted++
			continue
		}
		b.WriteString(row)
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n_%d more file(s) summarised on the full report._\n", omitted)
	}
	b.WriteString("\n</details>\n\n")
	return b.String()
}

// evidenceTable is the one-line-per-pane account of what was checked. It is
// what makes the posted review verifiable in scope: a pane that did not run is
// named here, where a reader of the pull request will actually see it.
func evidenceTable(rep *findings.Report) string {
	if rep == nil {
		return ""
	}
	perSubstrate := map[string]int{}
	for _, f := range rep.Findings {
		perSubstrate[f.Substrate]++
	}
	var b strings.Builder
	b.WriteString("| Evidence | Result |\n|---|---|\n")
	for _, s := range rep.Substrates {
		var result string
		switch s.State {
		case findings.SubstrateRan:
			if s.Detail != "" {
				result = "ran — " + escapeCell(s.Detail)
			} else {
				result = fmt.Sprintf("ran — %d finding(s)", perSubstrate[s.Name])
			}
		case findings.SubstrateSkipped:
			result = "did not apply"
		case findings.SubstrateNotApplicable:
			// The repository has no such files; the row would only say so.
			continue
		default:
			result = fmt.Sprintf("**did not run** — %s", escapeCell(s.Detail))
		}
		fmt.Fprintf(&b, "| %s | %s |\n", escapeCell(s.Name), result)
	}
	if c := rep.Coverage.Diff; c != nil {
		switch {
		case c.Percent < 0:
			fmt.Fprintf(&b, "| diff coverage | no coverable added line (`%s`) |\n", c.Profile)
		case c.Stale:
			fmt.Fprintf(&b, "| diff coverage | %.0f%% of %d added line(s), from a stale `%s` |\n", c.Percent, c.Lines, c.Profile)
		default:
			fmt.Fprintf(&b, "| diff coverage | %.0f%% of %d added line(s) (`%s`) |\n", c.Percent, c.Lines, c.Profile)
		}
	} else if rep.Coverage.CoverableFiles > 0 {
		b.WriteString("| diff coverage | not measured — no profile found |\n")
	}
	fmt.Fprintf(&b, "| files examined | %d/%d |\n", rep.Coverage.ExaminedFiles, rep.Coverage.ChangedFiles)
	return b.String()
}

// escapeCell keeps a value containing a pipe or a newline from breaking the
// evidence table out of its row.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.Join(strings.Fields(s), " ")
}

// Unposted drops everything Redline has already said for this payload's commit,
// both the line comments and the findings that ride in the body. posted comes
// from Fingerprints and is keyed on (head SHA, fingerprint), so a finding said
// against an earlier commit does not suppress what this commit needs.
//
// The body is rendered again afterwards. A body finding that was already
// reported would otherwise reappear in full on every re-post, which is the same
// duplication the fingerprint markers exist to prevent for line comments.
func (p Payload) Unposted(posted map[string]bool) Payload {
	out := p
	out.Comments = nil
	for _, c := range p.Comments {
		if posted[postedKey(p.CommitID, c.Fingerprint)] {
			continue
		}
		out.Comments = append(out.Comments, c)
	}
	out.bodyFindings = nil
	for _, f := range p.bodyFindings {
		if posted[postedKey(p.CommitID, f.Fingerprint)] {
			continue
		}
		out.bodyFindings = append(out.bodyFindings, f)
	}
	out.Body = buildBody(out.rep, out.CommitID, out.reportURL, out.bodyFindings, out.profile, out.GateVerdict, out.withheld, out.hedged)
	return out
}

// Fingerprints extracts what Redline has already said from a set of existing
// comment and review bodies fetched from GitHub. Keys are (head SHA, finding
// fingerprint) as built by postedKey; feed the result to Unposted.
//
// Review bodies matter as much as comment bodies: a finding that could not be
// anchored to a line is marked in the body it rode in, and reading only the
// comments would offer it again on every re-post.
func Fingerprints(bodies []string) map[string]bool {
	out := map[string]bool{}
	for _, body := range bodies {
		for _, m := range fpMarkerRe.FindAllStringSubmatch(body, -1) {
			raw, err := hex.DecodeString(m[2])
			if err != nil {
				continue
			}
			out[postedKey(m[1], string(raw))] = true
		}
	}
	return out
}

// ReviewedAt reports whether a Redline summary review has already been posted
// for the given head SHA, by scanning existing review bodies for its marker.
func ReviewedAt(bodies []string, head string) bool {
	if head == "" {
		return false
	}
	for _, body := range bodies {
		for _, m := range reviewMarkerRe.FindAllStringSubmatch(body, -1) {
			if m[1] == head {
				return true
			}
		}
	}
	return false
}

// bodyLocation explains why a finding is in the body rather than on a line: it
// names the changed-file line that is not in the diff, or falls back to the
// substrate for a finding that never had a line.
func bodyLocation(f findings.Finding) string {
	if f.File != "" && f.Line > 0 {
		return fmt.Sprintf("%s:%d — not on a changed line in this PR", f.File, f.Line)
	}
	return f.Substrate
}

func marker(s string) string { return "<!-- " + s + " -->" }

// findingLabel is the bold lead of a posted finding, carrying its source: a
// pane measured it, or the agent's review said it, and the posted comment
// names which.
func findingLabel(f findings.Finding) string {
	if f.Source == findings.SourceLLM {
		return severityLabel(f.Severity) + " · agent"
	}
	return severityLabel(f.Severity)
}

func severityLabel(s findings.Severity) string {
	switch s {
	case findings.SeverityError:
		return "Error"
	case findings.SeverityWarning:
		return "Warning"
	case findings.SeverityInfo:
		return "Info"
	default:
		return string(s)
	}
}

// FindingFingerprint reads the finding a posted comment was about, from the
// hidden marker commentBody wrote into it.
//
// The marker is how anything downstream knows which finding a thread is about,
// and the format belongs here because this is where it is written. A second
// reader with its own copy of the regex is a reader that goes stale the first
// time the marker changes.
//
// The head SHA in the marker is deliberately ignored. Idempotency needs it,
// because a finding still live after a push must be said again for the new
// commit; reading a conversation back does not, because what an author said
// about a finding is still what they think of it after they push.
func FindingFingerprint(body string) (string, bool) {
	m := fpMarkerRe.FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	raw, err := hex.DecodeString(m[2])
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// QuestionIn reads the question a posted comment's finding asked, from the
// marker commentBody wrote. Empty when the comment predates the marker, or
// when the finding asked nothing a lookup could settle.
func QuestionIn(body string) string {
	m := qMarkerRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	raw, err := hex.DecodeString(m[1])
	if err != nil {
		return ""
	}
	return string(raw)
}

// StripMarkers removes the hidden HTML comments from a body, leaving the prose
// a person would have read. Nothing downstream should be shown a marker: it is
// bookkeeping between two runs of this tool, and putting it in front of a
// model invites the model to write one.
func StripMarkers(body string) string {
	var b strings.Builder
	rest := body
	for {
		i := strings.Index(rest, "<!--")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		j := strings.Index(rest[i:], "-->")
		if j < 0 {
			// An unterminated comment is the rest of the body. Keeping it
			// would put a half-marker in front of a reader.
			break
		}
		rest = rest[i+j+len("-->"):]
	}
	return strings.TrimSpace(b.String())
}
