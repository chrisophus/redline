// Package post turns a finished Redline session into one GitHub pull request
// review and submits it. It is the deliberate, explicit counterpart to the
// rest of Redline, which only ever reads from GitHub: nothing here runs unless
// the operator typed `redline post`, and `run`, `review`, and `ingest` never
// reach it.
//
// The review a reviewer reads has two parts. Findings that carry a file and a
// line become line-anchored comments; the body opens with stated intent (from
// the PR), what the change actually does, and any discrepancies between them,
// then the walkthrough and any finding that could not be anchored to a line.
//
// This file is the payload: pure functions from a Report to what should be
// posted, with no knowledge of gh or the network. The command layer does the
// I/O so this stays testable without a GitHub token.
package post

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/target"
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

// reviewMarkerPrefix tags the review body with the head SHA it was posted for,
// so a re-post against the same commit can tell "already reviewed this commit"
// from "reviewing a new push."
const reviewMarkerPrefix = "redline:review:"

// threadMarkerPrefix marks a spot in the body that should become a permalink to
// an inline comment once the review exists. Comment IDs are not known until
// after the POST, so the body is written with these placeholders and patched.
const threadMarkerPrefix = "redline:thread:"

var (
	// Two hex runs separated by a colon: the head SHA, then the encoded
	// fingerprint. A marker written before the SHA was part of the key has only
	// one run and deliberately does not match, so those findings post once more
	// against the current head rather than being read as posted for it.
	fpMarkerRe     = regexp.MustCompile(regexp.QuoteMeta(fpMarkerPrefix) + `([0-9a-fA-F]+):([0-9a-fA-F]+)`)
	reviewMarkerRe = regexp.MustCompile(regexp.QuoteMeta(reviewMarkerPrefix) + `([0-9a-fA-F]+)`)
	threadMarkerRe = regexp.MustCompile(`<!-- ` + regexp.QuoteMeta(threadMarkerPrefix) + `([0-9a-fA-F]+) -->`)
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
	nar          Narrative
	reportURL    string
	bodyFindings []findings.Finding
	profile      *Profile
}

// NothingNew reports that this payload has no finding Redline has not already
// said for this commit, counting the ones that ride in the body. The caller
// still has to post when the commit has never been reviewed, because that is
// how the summary and walkthrough arrive.
func (p Payload) NothingNew() bool {
	return len(p.Comments) == 0 && len(p.bodyFindings) == 0
}

// Blank reports that a reader would see nothing in this review: no summary, no
// actual, no walkthrough, no findings, and no report link. It happens when post
// runs on a session that was never ingested, where the body renders as the
// head-SHA marker and nothing else.
func (p Payload) Blank() bool {
	return len(p.Comments) == 0 &&
		len(p.bodyFindings) == 0 &&
		strings.TrimSpace(p.nar.Summary) == "" &&
		strings.TrimSpace(p.nar.Actual) == "" &&
		len(p.nar.Files) == 0 &&
		len(p.nar.Discrepancies) == 0 &&
		p.reportURL == ""
}

// FileNote is one line of the walkthrough: what a changed file does in this
// change. Redline never derives these; they come from the agent.
type FileNote struct {
	Path    string
	Summary string
}

// DiscrepancyNote is one intent-versus-actual gap for the overview. Fingerprint
// joins it to the inline comment after the review is created.
type DiscrepancyNote struct {
	Claim       string
	Actual      string
	Fingerprint string
}

// CoverageNote is the provenance footer: how much of the change was examined.
type CoverageNote struct {
	ChangedFiles  int
	ExaminedFiles int
	BriefFiles    int // files the context brief opened outside the diff
}

// Narrative is the agent's prose half of the review — the part a reviewer
// reads before any individual comment. Stated intent is not agent prose: it
// comes from the pull request Redline already fetched, so the comparison
// stays a check rather than a paraphrase.
type Narrative struct {
	Summary       string
	Actual        string
	StatedTitle   string
	StatedBody    string
	Files         []FileNote
	Discrepancies []DiscrepancyNote
	Coverage      CoverageNote
	Verdict       string // set by Build from finding severities when empty
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
func Build(rep *findings.Report, tgt *target.Target, nar Narrative, reportURL string, commentable map[string]map[int]bool) Payload {
	return BuildAttest(rep, tgt, nar, reportURL, commentable, nil)
}

// BuildAttest is Build with a merge-gate profile. A nil profile is identical
// to Build. When set, the body and blocking comments carry the profile's
// hidden markers, and GateVerdict is pass or fail.
func BuildAttest(rep *findings.Report, tgt *target.Target, nar Narrative, reportURL string, commentable map[string]map[int]bool, prof *Profile) Payload {
	head := ""
	if tgt != nil {
		head = tgt.Head
	}
	if tgt != nil && tgt.PR != nil {
		if nar.StatedTitle == "" {
			nar.StatedTitle = tgt.PR.Title
		}
		if nar.StatedBody == "" {
			nar.StatedBody = tgt.PR.Body
		}
	}
	if nar.Verdict == "" {
		nar.Verdict = verdictFor(rep)
	}
	if rep != nil && nar.Coverage.ChangedFiles == 0 && nar.Coverage.ExaminedFiles == 0 {
		nar.Coverage.ChangedFiles = rep.Coverage.ChangedFiles
		nar.Coverage.ExaminedFiles = rep.Coverage.ExaminedFiles
	}

	p := Payload{CommitID: head, GateVerdict: GateVerdict(rep, prof), profile: prof}

	var inBody []findings.Finding
	findingsList := []findings.Finding(nil)
	if rep != nil {
		findingsList = rep.Findings
	}
	for _, f := range findingsList {
		if f.File != "" && f.Line > 0 && lineCommentable(commentable, f.File, f.Line) {
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

	// Attach fingerprints to discrepancy notes that share a rule with a finding,
	// so the overview can thread them after the review is created.
	nar.Discrepancies = bindDiscrepancyFingerprints(nar.Discrepancies, findingsList)

	p.nar = nar
	p.reportURL = reportURL
	p.bodyFindings = inBody
	p.Body = buildBody(nar, head, reportURL, inBody, prof, p.GateVerdict)
	return p
}

func verdictFor(rep *findings.Report) string {
	if rep == nil || len(rep.Findings) == 0 {
		return "No findings"
	}
	for _, f := range rep.Findings {
		if f.Severity == findings.SeverityError {
			return "Changes recommended"
		}
	}
	for _, f := range rep.Findings {
		if f.Severity == findings.SeverityWarning {
			return "Changes recommended"
		}
	}
	return "Comments"
}

func bindDiscrepancyFingerprints(notes []DiscrepancyNote, fs []findings.Finding) []DiscrepancyNote {
	if len(notes) == 0 {
		return notes
	}
	byMsg := map[string]string{}
	for _, f := range fs {
		if f.Category == findings.CategoryIntent || f.Rule == "intent-drift" || strings.HasPrefix(f.Rule, "intent") {
			byMsg[f.Message] = f.Fingerprint
		}
		if f.Fingerprint != "" {
			byMsg[f.Rule+"\x00"+f.Message] = f.Fingerprint
		}
	}
	out := make([]DiscrepancyNote, len(notes))
	for i, n := range notes {
		out[i] = n
		if n.Fingerprint != "" {
			continue
		}
		if fp := byMsg[n.Claim]; fp != "" {
			out[i].Fingerprint = fp
		}
	}
	return out
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
	if m := findingAttestMarker(prof, f.Severity); m != "" {
		b.WriteString(m)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "**%s** — %s", severityLabel(f.Severity), f.Message)
	if f.Reviewer != "" {
		fmt.Fprintf(&b, " _(%s", f.Reviewer)
		if f.Confidence != "" {
			fmt.Fprintf(&b, ", %s confidence", f.Confidence)
		}
		b.WriteString(")_")
	} else if f.Source == findings.SourceDeterministic {
		b.WriteString(" _(observed)_")
	}
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
	return b.String()
}

// fpMarker renders the hidden marker that identifies one finding as said for one
// commit.
func fpMarker(head, fingerprint string) string {
	return marker(fpMarkerPrefix + head + ":" + hex.EncodeToString([]byte(fingerprint)))
}

func threadMarker(fingerprint string) string {
	return marker(threadMarkerPrefix + hex.EncodeToString([]byte(fingerprint)))
}

// buildBody is the review body a reviewer reads first: verdict, stated intent
// from the PR, what the change actually does, discrepancies (threaded to their
// inline comments after POST), the walkthrough, unanchored findings, then a
// coverage footer and the report link.
func buildBody(nar Narrative, head, reportURL string, inBody []findings.Finding, prof *Profile, gateVerdict string) string {
	var b strings.Builder
	if v := strings.TrimSpace(nar.Verdict); v != "" {
		fmt.Fprintf(&b, "### %s\n\n", v)
	}
	if title := strings.TrimSpace(nar.StatedTitle); title != "" || strings.TrimSpace(nar.StatedBody) != "" {
		b.WriteString("**Stated intent.** ")
		if title != "" {
			b.WriteString(title)
			if strings.TrimSpace(nar.StatedBody) != "" {
				b.WriteString(". ")
			}
		}
		if body := compactIntent(nar.StatedBody); body != "" {
			b.WriteString(body)
		}
		b.WriteString("\n\n")
	}
	if actual := strings.TrimSpace(nar.Actual); actual != "" {
		b.WriteString("**What it does.** ")
		b.WriteString(actual)
		b.WriteString("\n\n")
	} else if summary := strings.TrimSpace(nar.Summary); summary != "" {
		// A review that only has Summary still leads with it: older ingests
		// and dry-runs without Actual keep working.
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	if len(nar.Discrepancies) > 0 {
		b.WriteString("**Discrepancies.**\n")
		for i, d := range nar.Discrepancies {
			fmt.Fprintf(&b, "%d. %s", i+1, strings.TrimSpace(d.Claim))
			if a := strings.TrimSpace(d.Actual); a != "" {
				fmt.Fprintf(&b, " — %s", a)
			}
			if d.Fingerprint != "" {
				fmt.Fprintf(&b, " %s", threadMarker(d.Fingerprint))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(nar.Files) > 0 {
		b.WriteString("<details>\n<summary>Walkthrough</summary>\n\n")
		b.WriteString("| File | What changed |\n|---|---|\n")
		for _, f := range nar.Files {
			fmt.Fprintf(&b, "| `%s` | %s |\n", escapeCell(f.Path), escapeCell(f.Summary))
		}
		b.WriteString("\n</details>\n\n")
	}
	if len(inBody) > 0 {
		b.WriteString("### Findings not shown inline\n\n")
		for _, f := range inBody {
			if m := findingAttestMarker(prof, f.Severity); m != "" {
				fmt.Fprintf(&b, "%s\n", m)
			}
			fmt.Fprintf(&b, "- **%s** — %s", severityLabel(f.Severity), f.Message)
			if loc := bodyLocation(f); loc != "" {
				fmt.Fprintf(&b, " _(%s)_", loc)
			}
			fmt.Fprintf(&b, " %s\n", fpMarker(head, f.Fingerprint))
		}
		b.WriteString("\n")
	}
	if foot := coverageFooter(nar.Coverage); foot != "" {
		b.WriteString(foot)
		b.WriteString("\n")
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

// compactIntent collapses a PR body to a short prose claim for the overview.
// It keeps the first non-empty paragraph and flattens whitespace so a markdown
// PR description does not explode the review body.
func compactIntent(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	parts := strings.Split(body, "\n\n")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		// Skip pure list / checkbox lead-ins that are not the claim.
		if strings.HasPrefix(p, "- [") || strings.HasPrefix(p, "* [") {
			continue
		}
		return strings.Join(strings.Fields(p), " ")
	}
	return strings.Join(strings.Fields(body), " ")
}

func coverageFooter(c CoverageNote) string {
	if c.ChangedFiles == 0 && c.ExaminedFiles == 0 && c.BriefFiles == 0 {
		return ""
	}
	var b strings.Builder
	if c.ChangedFiles > 0 {
		fmt.Fprintf(&b, "Files reviewed %d/%d", c.ExaminedFiles, c.ChangedFiles)
	}
	if c.BriefFiles > 0 {
		if b.Len() > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(&b, "brief read %d file(s) outside the diff", c.BriefFiles)
	}
	return b.String()
}

// ThreadBody replaces discrepancy placeholders with permalinks to the inline
// comments created by the review. ids maps finding fingerprint to the GitHub
// pull-request review comment id. Placeholders with no matching id are left
// alone so a partial match still improves the body.
func ThreadBody(body string, ids map[string]int64) string {
	if len(ids) == 0 {
		return body
	}
	return threadMarkerRe.ReplaceAllStringFunc(body, func(m string) string {
		sub := threadMarkerRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		raw, err := hex.DecodeString(sub[1])
		if err != nil {
			return m
		}
		id, ok := ids[string(raw)]
		if !ok {
			return m
		}
		return fmt.Sprintf("→ [#](#discussion_r%d)", id)
	})
}

// escapeCell keeps a summary containing a pipe or a newline from breaking the
// walkthrough table out of its row.
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
	out.Body = buildBody(out.nar, out.CommitID, out.reportURL, out.bodyFindings, out.profile, out.GateVerdict)
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
