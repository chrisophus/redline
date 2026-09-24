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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/pane/lint"
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
// The head SHA is recorded for context, but idempotency is by fingerprint
// alone across the whole pull request: a finding is posted once and never
// repeated on a later commit. GitHub keeps the thread, collapsed as outdated
// once its line moves, and the review body re-states the verdict and evidence
// per commit, so a still-live finding does not read as fixed without being
// said twice.
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

// Comment is one line-anchored review comment. Body already carries the
// fingerprint marker; Fingerprint is kept alongside for filtering.
type Comment struct {
	Path      string
	Line      int
	StartLine int // zero means Line alone; otherwise the range start
	// Side is the diff side the comment anchors to: "LEFT" for a removed line
	// on the old file, "RIGHT" (or empty) for the new file. Carried from the
	// finding so a comment on a deleted line posts LEFT rather than landing on
	// the new-file line of the same number.
	Side        string
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
	rep       *findings.Report
	reportURL string
	// prURL is the pull request's own URL, which the walkthrough's diff links
	// hang off. Empty when the target is not a pull request.
	prURL        string
	bodyFindings []findings.Finding
	// lowConf are the reviewer's low-confidence info findings when a profile
	// asked to see them folded rather than withheld. They render in a
	// collapsed block, never as line comments. A low-confidence warning or
	// error is not here: it posts like any other finding.
	lowConf []findings.Finding
	profile *Profile
	// withheld counts the reviewer's own info findings this payload did not
	// post because they said they were unsure, and hedged those it withheld
	// for hedging. Kept apart because they are different failures and a
	// reader tuning the thing wants to know which one they have.
	withheld int
	hedged   int
	// lint counts the lint pane's findings, which this payload records and
	// does not post. A line comment per lint violation is a comment the
	// author's own linter will make at the same moment CI does, and on a
	// large change they outnumber the review: the evidence table says the
	// pane ran and how many it found, and the report carries them.
	lint int
	// files is every file in the change, with the language and line counts the
	// composition table reads. Kept so the body can list files the agent did
	// not summarize, and so both layouts can say what the change is made of.
	files []change.File
	// intent and reviewedBy are the PR metadata the walkthrough body opens
	// with, set by the command from gh once it is known. Empty renders nothing.
	intent     string
	reviewedBy string
	// recap is the describing call's paragraph on what changed since the
	// previous review, and recapSince the commit it was written against.
	// Set by WithRecap, and both empty on a body that repeats the walkthrough
	// as it always did.
	//
	// When they are set the body opens with that paragraph in place of the
	// overview, and the per-file list shows only recapFiles, because a pull
	// request reviewed four times carried four copies of a walkthrough that
	// had not changed. The report still has all of it.
	recap      string
	recapSince string
	recapFiles []string
	// staleHead is the pull request's head when it is no longer the commit
	// this review is of, and empty otherwise. unattested withholds the gate
	// verdict marker, for a profile that wants its verdict to cover the
	// current head. Both are rendered with the body rather than prepended to
	// it, because Unposted renders the body again and a prefix would be lost.
	staleHead  string
	unattested bool
}

// Stale records that the pull request's head has moved past the commit this
// review is of. The body opens with a notice saying so. When the profile
// requires the head, the review also withholds its gate verdict: it still
// posts, so the findings a paid review found are not thrown away, but a gate
// that wants a verdict on the current commit is not handed one about an older
// commit. Finding markers stay, because they describe the lines they sit on.
func (p Payload) Stale(head string) Payload {
	p.staleHead = head
	if p.profile != nil && p.profile.RequireHead {
		p.unattested = true
	}
	p.Body = p.renderBody()
	return p
}

// Attested reports whether the body carries a gate verdict marker: a profile
// was supplied and the verdict was not withheld for a stale head.
func (p Payload) Attested() bool {
	return p.GateVerdict != "" && !p.unattested
}

// attestedVerdict is the verdict the body's marker carries, empty when none.
func (p Payload) attestedVerdict() string {
	if !p.Attested() {
		return ""
	}
	return p.GateVerdict
}

// staleNotice opens the body of a review whose commit is no longer the head.
func (p Payload) staleNotice() string {
	if p.staleHead == "" {
		return ""
	}
	notice := fmt.Sprintf("> This review is of `%s`, which is no longer the head of this "+
		"pull request (`%s`). Findings below may already be addressed.",
		shortSHA12(p.CommitID), shortSHA12(p.staleHead))
	if p.unattested {
		notice += " It carries no gate verdict; the review of the current head does."
	}
	return notice + "\n\n"
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
	return BuildAttest(rep, tgt, reportURL, commentable, nil, nil)
}

// BuildAttest is Build with a merge-gate profile and the change's files. A nil
// profile is identical to Build. When set, the body and blocking comments carry
// the profile's hidden markers, and GateVerdict is pass or fail. files is what
// the composition table and the walkthrough are drawn from; without it both
// bodies simply leave them out.
func BuildAttest(rep *findings.Report, tgt *target.Target, reportURL string, commentable map[string]map[int]bool, prof *Profile, files []change.File) Payload {
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
		if f.Substrate == lint.DeltaSubstrate {
			// Counted and not posted. The pane's whole output is a rule name
			// and a line, which the author's linter says too and says first;
			// what a review adds is the reading a linter cannot do. Posting
			// both put ten lint comments in front of five findings and made
			// the review look like a lint run.
			p.lint++
			continue
		}
		if refused(f) {
			// The verifying pass ruled it out. It stays on the report with
			// the reason and goes nowhere near the pull request, not even
			// as a count: a note saying something was held back tells the
			// reader nothing they can act on.
			continue
		}
		// An error is a claimed defect, so it must remain visible even when the
		// reviewer's wording also carries uncertainty. Warning and info findings
		// keep the report-only policy for genuinely uncertain claims.
		if f.Severity != findings.SeverityError &&
			(unfalsifiable(f) || withheldForConfidence(f)) {
			if prof.includes("low-confidence") {
				// Shown behind a chevron instead of withheld: a guess the
				// reader can open, never a line comment and never a gate. This
				// also covers findings whose question says nothing would settle
				// them; that is another form of reviewer uncertainty.
				p.lowConf = append(p.lowConf, f)
			} else {
				p.withheld++
			}
			continue
		}
		if f.Severity != findings.SeverityError && hedged(f) {
			if prof.includes("low-confidence") {
				// A hedge is the reviewer saying it is unsure the finding
				// matters, so it folds with the other unsure ones.
				p.lowConf = append(p.lowConf, f)
			} else {
				p.hedged++
			}
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
				Side:        f.Side,
				Body:        commentBody(f, head, prof),
				Fingerprint: f.Fingerprint,
			})
			continue
		}
		inBody = append(inBody, f)
	}

	p.rep = rep
	p.reportURL = reportURL
	if tgt != nil && tgt.PR != nil {
		p.prURL = strings.TrimRight(tgt.PR.URL, "/")
	}
	p.bodyFindings = inBody
	p.files = files
	p.Body = p.renderBody()
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
	return !refused(f) && (f.Severity == findings.SeverityError ||
		(!unfalsifiable(f) && !withheldForConfidence(f) && !hedged(f)))
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
// Only the reviewer's own findings. A pane's finding carries no confidence at
// all, by Finalize, so nothing built on this can withhold a measurement.
func lowConfidence(f findings.Finding) bool {
	return f.Source == findings.SourceLLM && f.Confidence == findings.ConfidenceLow
}

// withheldForConfidence reports whether the reviewer's own doubt keeps a
// finding off the pull request. Only an info finding.
//
// A defect held back is lost and a wrong one costs the author a minute, and at
// warning and error the second price is the smaller one. So a reviewer that
// suspects the change corrupts data or breaks a caller says so even when it is
// unsure, and the finding reaches the author with the doubt attached.
//
// Info is where doubt is worth acting on. An unsure remark about nothing much
// is the comment that teaches a team to stop reading the review.
//
// Doubt only. The two gates that are not about doubt are separate and apply at
// every severity: refused, for a finding the verifying pass did not keep, and
// unfalsifiable, for one whose author said nothing would settle it.
func withheldForConfidence(f findings.Finding) bool {
	return lowConfidence(f) && f.Severity == findings.SeverityInfo
}

// refused reports whether the verifying pass ruled this finding out. Withdrawn,
// justified, unverifiable and already-raised all stay off the pull request and
// on the report with their reason; running the pass and then posting what it
// refused is paying for a check and ignoring it.
func refused(f findings.Finding) bool {
	return f.Source == findings.SourceLLM && f.Ruling != "" &&
		f.Ruling != findings.VerifiedKept
}

// unfalsifiable reports whether the reviewer said nothing would settle its own
// claim. That is its account of the comment as speculation, and it is the one
// thing a reviewer told to report what it is unsure of still may not post.
func unfalsifiable(f findings.Finding) bool {
	return f.Source == findings.SourceLLM && f.Question.Kind == findings.QuestionNone
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
//
// Only the reviewer's own Message is scanned, never Context: for a verified
// finding Context is the ruling's quoted repository evidence, so a hedge word
// in the code that a kept finding quotes ("nit:", "probably fine") is not the
// reviewer hedging and must not withhold the finding.
func hedged(f findings.Finding) bool {
	if f.Source != findings.SourceLLM {
		return false
	}
	body := strings.ToLower(f.Message)
	for _, h := range hedges {
		if strings.Contains(body, h) {
			return true
		}
	}
	return false
}

// verdictFor is the review's headline, derived from the findings that actually
// reach the author. A finding withheld for low confidence or hedging is not
// posted, so counting it here made the headline say "Changes recommended" over
// a body that then reported the same finding as held back.
func verdictFor(rep *findings.Report) string {
	if rep == nil {
		return "No findings"
	}
	posted, changes := 0, false
	for _, f := range rep.Findings {
		if !Reaches(f) {
			continue
		}
		posted++
		if f.Severity == findings.SeverityError || f.Severity == findings.SeverityWarning {
			changes = true
		}
	}
	if posted == 0 {
		return "No findings"
	}
	if changes {
		return "Changes recommended"
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
//
// The whole assembled body is bounded against GitHub's limit, not just the
// agent's prose. The verdict, the evidence table and the file summary are
// written first and kept whole; the not-shown list is what grows without
// bound with the findings, so it is the part that truncates when the body
// would otherwise be rejected.
func buildBody(p Payload) string {
	var head0 strings.Builder
	fmt.Fprintf(&head0, "### %s\n\n", verdictFor(p.rep))
	// The agent's own account of the change, when there is one. Redline never
	// writes prose: an overview is present only because a review was run or a
	// human wrote one into review.json, so it is attributed rather than shown
	// as the tool's own conclusion.
	if s := overviewSection(p.rep); s != "" {
		head0.WriteString(s)
	}
	if table := evidenceTable(p.rep); table != "" {
		head0.WriteString(table)
		head0.WriteString("\n")
	}
	if s := compositionSection(p.files); s != "" {
		head0.WriteString(s)
	}
	// One row per file the agent described. Deliberately not a list of every
	// changed path with its line counts: GitHub's own Files tab already says
	// that, and repeating it would pad the review with what the reader can
	// see. A file reaches this table because someone wrote a sentence about
	// it.
	if s := fileSummarySection(p.rep); s != "" {
		head0.WriteString(s)
	}

	tail := bodyTail(p.reportURL, p.CommitID, p.profile, p.attestedVerdict(), p.withheld, p.hedged, p.lint)
	budget := maxBody - head0.Len() - len(tail)
	middle := notShownSection(p.bodyFindings, p.CommitID, p.profile, budget)
	low := lowConfidenceSection(p.lowConf, p.CommitID, p.profile, budget-len(middle))
	return head0.String() + middle + low + tail
}

// compositionSection is what the change is made of: its lines grouped by
// language and by the role the file plays, so a reviewer can see that a
// thousand added lines are mostly tests before opening the diff. It calls
// change.Composition, which is what the HTML report groups its sections with,
// so the page and the pull request cannot disagree about what counts as a
// test.
//
// GitHub shows none of this. Its Files tab gives paths and line counts and
// leaves the reader to add them up, which is why this is the one thing about
// the change the body does repeat.
//
// Bounded by row count rather than by the body budget: the table is written
// into the part of the body that is kept whole, and a change touching many
// languages must not push a finding off the end. The rest is counted in a
// line under the table.
func compositionSection(files []change.File) string {
	rows := change.Composition(files)
	if len(rows) == 0 {
		return ""
	}
	shown := rows
	var restFiles, restAdded, restRemoved int
	if len(rows) > maxCompositionRows {
		shown = rows[:maxCompositionRows]
		for _, r := range rows[maxCompositionRows:] {
			restFiles += r.Files
			restAdded += r.Added
			restRemoved += r.Removed
		}
	}
	var b strings.Builder
	// Captioned, where the evidence table above it is not: two tables in a row
	// with nothing between them read as one, and "Language" in a header cell
	// does not say whose lines these are.
	b.WriteString("**Lines by language and type.**\n\n")
	b.WriteString("| Language | Type | Files | + | − |\n|---|---|---:|---:|---:|\n")
	for _, r := range shown {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d |\n",
			escapeCell(r.Language), escapeCell(r.Kind), r.Files, r.Added, r.Removed)
	}
	if n := len(rows) - len(shown); n > 0 {
		fmt.Fprintf(&b, "| %d more | | %d | %d | %d |\n", n, restFiles, restAdded, restRemoved)
	}
	b.WriteString("\n")
	return b.String()
}

// maxCompositionRows is how many language and type pairs the table lists
// before the rest is summed into one row. Twelve is past what a normal change
// reaches, so the cap is a guard against a repository-wide diff rather than
// something a reviewer runs into.
const maxCompositionRows = 12

// lowConfidenceSection folds the info findings the reviewer was unsure of into
// a collapsed block, for a profile that would rather see a guess behind a
// chevron than not at all. They never become line comments and never gate:
// shown as guesses, in one place a reader opens on purpose. Enabled by
// body_include: low-confidence; without it they are withheld. Warnings and
// errors do not reach here, whatever their confidence.
func lowConfidenceSection(lowConf []findings.Finding, head string, prof *Profile, budget int) string {
	if len(lowConf) == 0 {
		return ""
	}
	heading := fmt.Sprintf("<details>\n<summary>Low confidence (%d)</summary>\n\n", len(lowConf))
	const closing = "\n</details>\n\n"
	if len(heading)+len(closing) > budget {
		return ""
	}
	var b strings.Builder
	b.WriteString(heading)
	shown := 0
	for _, f := range lowConf {
		entry := bodyFindingLine(f, head, prof)
		if b.Len()+len(entry)+len(closing) > budget {
			break
		}
		b.WriteString(entry)
		shown++
	}
	if shown < len(lowConf) {
		b.WriteString(truncatedNote(len(lowConf) - shown))
	}
	b.WriteString(closing)
	return b.String()
}

// bodyTail is the review's own bookkeeping that must survive however long the
// findings list runs: the withheld note, the report link, and the markers a
// merge gate reads. Shared by both layouts and measured before the not-shown
// list is fitted to what is left of the budget.
func bodyTail(reportURL, head string, prof *Profile, gateVerdict string, withheld, hedged, lint int) string {
	var tail strings.Builder
	if withheld > 0 || hedged > 0 {
		var parts []string
		if withheld > 0 {
			parts = append(parts, fmt.Sprintf("%d said they were uncertain", withheld))
		}
		if hedged > 0 {
			parts = append(parts, fmt.Sprintf("%d hedged", hedged))
		}
		fmt.Fprintf(&tail, "_%d further finding(s) from the reviewer are on the report "+
			"rather than here: %s._\n\n", withheld+hedged, strings.Join(parts, ", "))
	}
	if lint > 0 {
		// Named rather than silent. A reader who knows the pane ran must be
		// able to tell "nothing to say" from "said elsewhere", and the
		// evidence table above already carries the count per pane.
		fmt.Fprintf(&tail, "_%d lint finding(s) are on the report, not posted here._\n\n", lint)
	}
	if reportURL != "" {
		fmt.Fprintf(&tail, "[Full report](%s)\n\n", reportURL)
	}
	tail.WriteString(marker(reviewMarkerPrefix + head))
	if m := reviewAttestMarker(prof, gateVerdict, head); m != "" {
		tail.WriteByte('\n')
		tail.WriteString(m)
	}
	return tail.String()
}

// renderBody assembles the review body in whichever layout the profile asked
// for. Evidence is the default one-line-per-pane body; walkthrough is the
// Copilot-style body a profile opts into with body_style.
func (p Payload) renderBody() string {
	if p.profile != nil && p.profile.BodyStyle == BodyWalkthrough {
		return p.staleNotice() + buildBodyWalkthrough(p)
	}
	return p.staleNotice() + buildBody(p)
}

// WithRecap puts the describing call's account of what is new in place of
// the walkthrough, for a post to a pull request Redline has reviewed before.
//
// Both arguments are required: a paragraph with no commit beside it cannot be
// read, because "since the last review" means nothing without saying which
// one. Given neither, the body is unchanged.
//
// files are the paths that moved since that commit. The per-file list shows
// only those; nil shows none, since the list cannot say which files the recap
// covers.
func (p Payload) WithRecap(recap, since string, files []string) Payload {
	if strings.TrimSpace(recap) == "" || strings.TrimSpace(since) == "" {
		return p
	}
	p.recap, p.recapSince = strings.TrimSpace(recap), since
	p.recapFiles = files
	p.Body = p.renderBody()
	return p
}

// WithMeta stamps the PR metadata the walkthrough body opens with and
// re-renders. The command fills these from gh once the login and the pull
// request title are known; an evidence body ignores them.
func (p Payload) WithMeta(intent, reviewedBy string) Payload {
	p.intent = intent
	p.reviewedBy = reviewedBy
	p.Body = p.renderBody()
	return p
}

// buildBodyWalkthrough is the Copilot-style body: who reviewed and which
// commit, what the change does or what moved since the last review, a
// collapsible walkthrough of the changed files grouped by language and role,
// then the findings that could not be anchored to a line. body_include adds
// the author's stated intent, the lines-by-language table, the evidence table
// and the rest, and every part of it comes from the session the run already
// wrote, so this observes nothing.
func buildBodyWalkthrough(p Payload) string {
	var head0 strings.Builder
	fmt.Fprintf(&head0, "### %s\n\n", walkthroughHeading(p.GateVerdict))
	if p.reviewedBy != "" {
		fmt.Fprintf(&head0, "**Reviewed by.** %s on `%s`.\n\n", p.reviewedBy, shortSHA12(p.CommitID))
	}
	if p.intent != "" && p.profile.includes("intent") {
		fmt.Fprintf(&head0, "**Stated intent.** %s\n\n", p.intent)
	}
	// The recap replaces the overview rather than joining it, and the file
	// list under it is only the files that moved since that review. Repeating
	// a walkthrough that has not changed is what it exists to stop.
	files := p.files
	title := "Walkthrough"
	if p.recap != "" {
		recap := p.recap
		if len(recap) > maxNarrative {
			recap = recap[:maxNarrative] + "\n\n_(truncated; the full walkthrough is on the report)_"
		}
		// A heading of its own, the same level as the evidence body's "What
		// this change does", because it is the part a reader came for.
		fmt.Fprintf(&head0, "### Since the last review (`%s`)\n\n%s\n\n", shortSHA12(p.recapSince), recap)
		files = filesIn(p.files, p.recapFiles)
		title = fmt.Sprintf("Files changed since `%s`", shortSHA12(p.recapSince))
	} else if p.rep != nil && p.rep.Agent != nil {
		if ov := strings.TrimSpace(p.rep.Agent.Overview); ov != "" {
			if len(ov) > maxNarrative {
				ov = ov[:maxNarrative] + "\n\n_(truncated; the full overview is on the report)_"
			}
			fmt.Fprintf(&head0, "### What this change does\n\n%s\n\n", ov)
		}
	}
	if p.profile.includes("composition") {
		head0.WriteString(compositionSection(p.files))
	}
	// A finding on a listed file rides under it; the rest, including one on
	// a file a recap leaves out of the list, go in the flat list after.
	perFile, leftover := splitBodyFindingsByFile(files, p.bodyFindings)
	if s := walkthroughSection(p, files, title, perFile, maxNarrative); s != "" {
		head0.WriteString(s)
	}
	if p.profile.includes("evidence") {
		if table := evidenceTable(p.rep); table != "" {
			head0.WriteString("<details>\n<summary>Evidence</summary>\n\n")
			head0.WriteString(table)
			head0.WriteString("\n</details>\n\n")
		}
	}
	if p.profile.includes("confirmations") {
		head0.WriteString(confirmationsSection(p.rep))
	}
	if p.profile.includes("unknowns") {
		head0.WriteString(unknownsSection(p.rep))
	}
	tail := bodyTail(p.reportURL, p.CommitID, p.profile, p.attestedVerdict(), p.withheld, p.hedged, p.lint)
	budget := maxBody - head0.Len() - len(tail)
	middle := notShownSection(leftover, p.CommitID, p.profile, budget)
	low := lowConfidenceSection(p.lowConf, p.CommitID, p.profile, budget-len(middle))
	return head0.String() + middle + low + tail
}

// filesIn keeps the files whose path is in paths, in their original order.
func filesIn(files []change.File, paths []string) []change.File {
	keep := make(map[string]bool, len(paths))
	for _, path := range paths {
		keep[path] = true
	}
	var out []change.File
	for _, f := range files {
		if keep[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// splitBodyFindingsByFile groups the body findings that name a file the
// walkthrough shows, so they render with that file instead of in a flat list
// after it, and returns the rest. A finding with no file, or one on a file
// the walkthrough does not list, has no row to ride with and stays in the list.
func splitBodyFindingsByFile(files []change.File, bodyFindings []findings.Finding) (map[string][]findings.Finding, []findings.Finding) {
	shown := map[string]bool{}
	for _, f := range files {
		shown[f.Path] = true
	}
	perFile := map[string][]findings.Finding{}
	var leftover []findings.Finding
	for _, f := range bodyFindings {
		if f.File != "" && shown[f.File] {
			perFile[f.File] = append(perFile[f.File], f)
		} else {
			leftover = append(leftover, f)
		}
	}
	return perFile, leftover
}

// walkthroughHeading matches the author-published reviews: a failing gate reads
// "Review findings", anything else "Review complete".
func walkthroughHeading(gateVerdict string) string {
	if gateVerdict == "fail" {
		return "Review findings"
	}
	return "Review complete"
}

// walkthroughSection is the collapsible account of every changed file, in the
// same sections the HTML report drills into: one heading per language and role
// pair, and a table of that group's files under it. Grouping is
// change.CompositionGroups, which is what the report groups by, and the
// dominant part of the change comes first.
//
// Each row is the file and the agent's one-line summary, plus what
// body_include turns on: line-counts adds the lines moved (and the group's
// totals to its heading), coverage the added lines a profile shows
// unexecuted, lint what landed on the file by severity. All of it reads the
// report the run wrote.
//
// Test files are in their own group rather than left out. They used to be
// dropped, because a flat table interleaved them with the code they test and
// the rows read as noise; under a heading of their own they are the thing a
// reviewer wanted to see, which is how much of the change is test.
func walkthroughSection(p Payload, files []change.File, title string, perFile map[string][]findings.Finding, budget int) string {
	if len(files) == 0 {
		return ""
	}
	withCoverage := p.profile.includes("coverage") && p.rep != nil && p.rep.Coverage.Diff != nil
	withLint := p.profile.includes("lint")
	withLinks := p.profile.includes("diff-links") && p.prURL != ""
	withCounts := p.profile.includes("line-counts")
	var uncovered map[string]int
	if withCoverage {
		uncovered = uncoveredByFile(p.rep)
	}
	var counts map[string]string
	if withLint {
		counts = findingCountsByFile(p.rep)
	}
	summaries := map[string]string{}
	if p.rep != nil && p.rep.Agent != nil {
		summaries = p.rep.Agent.Files
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<details>\n<summary>%s</summary>\n\n", title)
	var paths []string
	var omitted int
	for _, g := range change.CompositionGroups(files) {
		group := make([]change.File, len(g.Files))
		copy(group, g.Files)
		sort.Slice(group, func(i, j int) bool { return group[i].Path < group[j].Path })

		var sec strings.Builder
		if withCounts {
			fmt.Fprintf(&sec, "**%s %s** (%d file(s), +%d%s−%d)\n\n",
				escapeLine(g.Language), escapeLine(g.Kind), len(g.Files), g.Added, nbsp, g.Removed)
		} else {
			fmt.Fprintf(&sec, "**%s %s** (%d file(s))\n\n",
				escapeLine(g.Language), escapeLine(g.Kind), len(g.Files))
		}
		// One table per group. The columns other than the file and its
		// summary are only there when the profile asks for them, which keeps
		// the prose column wide enough that a path does not wrap in the
		// middle; that wrapping is why this was once a list instead.
		header, rule := "| File |", "|---|"
		if withCounts {
			header, rule = header+" Lines |", rule+"---:|"
		}
		if withCoverage {
			header, rule = header+" Uncovered |", rule+"---:|"
		}
		if withLint {
			header, rule = header+" Findings |", rule+"---|"
		}
		header, rule = header+" What changed |\n", rule+"---|\n"
		sec.WriteString(header)
		sec.WriteString(rule)
		shown := 0
		for _, f := range group {
			name := fmt.Sprintf("`%s`", escapeCell(f.Path))
			// Tests are left unlinked: the reader goes to the code under
			// review, and a link on every row costs walkthrough budget.
			// Generated files never get here, run drops them first.
			if withLinks && g.Kind != change.KindTest {
				name = fmt.Sprintf("[%s](%s)", name, diffLink(p.prURL, f.Path))
			}
			item := "| " + name + " |"
			if withCounts {
				// Joined by a non-breaking space so a narrow column cannot
				// put the two counts on separate lines.
				item += fmt.Sprintf(" +%d%s−%d |", f.Added, nbsp, f.Removed)
			}
			if withCoverage {
				item += " " + orDash(fmt.Sprint(uncovered[f.Path]), uncovered[f.Path] > 0) + " |"
			}
			if withLint {
				item += " " + orDash(escapeCell(counts[f.Path]), counts[f.Path] != "") + " |"
			}
			summary := strings.TrimSpace(summaries[f.Path])
			item += " " + orDash(escapeCell(summary), summary != "") + " |\n"
			if b.Len()+sec.Len()+len(item) > budget {
				omitted++
				continue
			}
			sec.WriteString(item)
			shown++
			paths = append(paths, f.Path)
		}
		if shown == 0 {
			// A heading over an empty table says less than nothing. The files
			// it would have listed are already counted as omitted.
			continue
		}
		sec.WriteString("\n")
		b.WriteString(sec.String())
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "_%d more file(s) on the full report._\n", omitted)
	}
	grpOmitted := 0
	for _, path := range paths {
		group := perFile[path]
		if len(group) == 0 {
			continue
		}
		// The file's own findings, each still carrying its fingerprint marker
		// so a re-post skips it, listed under the file rather than in the flat
		// section after the walkthrough.
		var block strings.Builder
		fmt.Fprintf(&block, "\n**`%s`**\n", path)
		for _, f := range group {
			block.WriteString(bodyFindingLine(f, p.CommitID, p.profile))
		}
		if b.Len()+block.Len() > budget {
			grpOmitted += len(group)
			continue
		}
		b.WriteString(block.String())
	}
	if grpOmitted > 0 {
		fmt.Fprintf(&b, "\n_%d more finding(s) on the full report._\n", grpOmitted)
	}
	// Each section ends with a blank line, and so does whatever followed it, so
	// the trailing ones are trimmed rather than left to stack up before the
	// closing tag.
	return strings.TrimRight(b.String(), "\n") + "\n\n</details>\n\n"
}

// uncoveredByFile counts, per file, the added lines a coverage profile shows
// unexecuted, from the run's own diff-coverage result.
func uncoveredByFile(rep *findings.Report) map[string]int {
	out := map[string]int{}
	if rep == nil || rep.Coverage.Diff == nil {
		return out
	}
	for _, g := range rep.Coverage.Diff.Uncovered {
		out[g.Path] = len(g.Lines)
	}
	return out
}

// findingCountsByFile summarises, per file, how many findings landed on it by
// severity, so the walkthrough shows where the issues are without repeating the
// message each inline comment already carries.
func findingCountsByFile(rep *findings.Report) map[string]string {
	if rep == nil {
		return nil
	}
	type counts struct{ err, warn, info int }
	by := map[string]*counts{}
	for _, f := range rep.Findings {
		if f.File == "" {
			continue
		}
		c := by[f.File]
		if c == nil {
			c = &counts{}
			by[f.File] = c
		}
		switch f.Severity {
		case findings.SeverityError:
			c.err++
		case findings.SeverityWarning:
			c.warn++
		default:
			c.info++
		}
	}
	out := map[string]string{}
	for file, c := range by {
		var parts []string
		if c.err > 0 {
			parts = append(parts, fmt.Sprintf("%d error", c.err))
		}
		if c.warn > 0 {
			parts = append(parts, fmt.Sprintf("%d warning", c.warn))
		}
		if c.info > 0 {
			parts = append(parts, fmt.Sprintf("%d info", c.info))
		}
		out[file] = strings.Join(parts, ", ")
	}
	return out
}

// confirmationsSection folds the checks that ran clean into the body. They are
// the report's deliverable, each a question the reviewer no longer has to ask,
// and a repository that wants them on the pull request opts in.
func confirmationsSection(rep *findings.Report) string {
	if rep == nil || len(rep.Confirmations) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<details>\n<summary>Checks that passed (%d)</summary>\n\n", len(rep.Confirmations))
	for _, c := range rep.Confirmations {
		fmt.Fprintf(&b, "- %s\n", escapeCell(c.Message))
	}
	b.WriteString("\n</details>\n\n")
	return b.String()
}

// unknownsSection folds what could not be determined into the body. A gap
// nobody names reads exactly like a gap that is not there, which is the one
// thing this tool refuses to let a review do.
func unknownsSection(rep *findings.Report) string {
	if rep == nil || len(rep.Unknowns) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<details>\n<summary>Could not determine (%d)</summary>\n\n", len(rep.Unknowns))
	for _, u := range rep.Unknowns {
		line := u.Message
		if u.Reason != "" {
			line += " — " + u.Reason
		}
		fmt.Fprintf(&b, "- %s\n", escapeCell(line))
	}
	b.WriteString("\n</details>\n\n")
	return b.String()
}

// shortSHA12 is the twelve-character commit the author-published reviews name,
// so a reader comparing the two sees the same identifier.
func shortSHA12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// maxBody is GitHub's hard limit on a review body. The whole assembled body,
// not just the agent's prose, must stay under it: the not-shown list grows
// with the findings and, left unbounded, a lint-heavy change would 422 the
// entire review, comments and gate marker included.
const maxBody = 65536

// notShownSection renders the findings that could not be anchored to a line,
// bounded to what is left of the body budget. Entries that do not fit are
// dropped and replaced by a visible count, so the review always posts rather
// than the whole submission being rejected for length.
func notShownSection(inBody []findings.Finding, head string, prof *Profile, budget int) string {
	if len(inBody) == 0 {
		return ""
	}
	const heading = "### Findings not shown inline\n\n"
	// Room kept for the truncation note (worst case: every finding dropped)
	// and the trailing blank line, so appending them can never push the body
	// back over the limit.
	reserve := len(truncatedNote(len(inBody))) + len("\n")
	if len(heading)+reserve > budget {
		return ""
	}
	var b strings.Builder
	b.WriteString(heading)
	shown := 0
	for _, f := range inBody {
		entry := bodyFindingLine(f, head, prof)
		if b.Len()+len(entry)+reserve > budget {
			break
		}
		b.WriteString(entry)
		shown++
	}
	if shown < len(inBody) {
		b.WriteString(truncatedNote(len(inBody) - shown))
	}
	b.WriteString("\n")
	return b.String()
}

// bodyFindingLine renders one not-shown finding: its optional attest marker,
// the finding itself, where it sits, and the hidden fingerprint marker a
// re-post skips it by.
func bodyFindingLine(f findings.Finding, head string, prof *Profile) string {
	var b strings.Builder
	if m := findingAttestMarker(prof, f); m != "" {
		fmt.Fprintf(&b, "%s\n", m)
	}
	fmt.Fprintf(&b, "- **%s** — %s", findingLabel(f), f.Message)
	if loc := bodyLocation(f); loc != "" {
		fmt.Fprintf(&b, " _(%s)_", loc)
	}
	fmt.Fprintf(&b, " %s\n", fpMarker(head, f.Fingerprint))
	return b.String()
}

// truncatedNote is the visible marker left where the not-shown list was cut
// for length, so a reader knows findings were dropped and where to read them.
func truncatedNote(n int) string {
	return fmt.Sprintf("_%d more finding(s) truncated to fit GitHub's review size limit; see the full report._\n", n)
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

// escapeLine flattens a value onto one line, for a list item, where a pipe
// needs no escaping and a newline would end the item early.
func escapeLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// nbsp sits between a file's two line counts. A plain space there lets GitHub
// wrap the added count and the removed count onto separate lines when the
// window is narrow, and the two read as one number, so they are written as one
// word.
const nbsp = "\u00a0"

// Unposted drops every finding Redline has already said on this pull request,
// both the line comments and the findings that ride in the body. posted comes
// from Fingerprints and is keyed on the fingerprint alone, so a finding said
// against any earlier commit is not said again.
//
// The body is rendered again afterwards. A body finding that was already
// reported would otherwise reappear in full on every re-post, which is the same
// duplication the fingerprint markers exist to prevent for line comments.
func (p Payload) Unposted(posted map[string]bool) Payload {
	out := p
	out.Comments = nil
	for _, c := range p.Comments {
		if posted[c.Fingerprint] {
			continue
		}
		out.Comments = append(out.Comments, c)
	}
	out.bodyFindings = nil
	for _, f := range p.bodyFindings {
		if posted[f.Fingerprint] {
			continue
		}
		out.bodyFindings = append(out.bodyFindings, f)
	}
	out.Body = out.renderBody()
	return out
}

// Fingerprints extracts what Redline has already said from a set of existing
// comment and review bodies fetched from GitHub. Keys are the finding
// fingerprints, head-independent, so feed the result to Unposted.
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
			out[string(raw)] = true
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

// ReviewedHeads is every commit Redline has already posted a review body for
// on this pull request, in the order the bodies were given.
//
// The head is in the marker each body already carries, so what the previous
// review ran against does not have to be supplied from outside. `redline post
// --recap` uses the last one that is not the current head, which is the
// commit a reader of that review last saw.
func ReviewedHeads(bodies []string) []string {
	var out []string
	for _, body := range bodies {
		for _, m := range reviewMarkerRe.FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
	}
	return out
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

// diffLink points at one file on the pull request's Files tab. GitHub anchors
// each file there as diff- followed by the hex SHA-256 of its path. The tab
// shows the pull request's current diff, so after a later push the link opens
// a newer diff than the one this review read.
func diffLink(prURL, path string) string {
	sum := sha256.Sum256([]byte(path))
	return prURL + "/files#diff-" + hex.EncodeToString(sum[:])
}

// orDash is s when ok, and a dash otherwise, so an empty table cell reads as
// "nothing here" rather than as a cell that failed to render.
func orDash(s string, ok bool) string {
	if ok {
		return s
	}
	return "—"
}
