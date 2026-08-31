// Package post turns a finished Redline session into one GitHub pull request
// review and submits it. It is the deliberate, explicit counterpart to the
// rest of Redline, which only ever reads from GitHub: nothing here runs unless
// the operator typed `redline post`, and `run`, `review`, and `ingest` never
// reach it.
//
// The review a reviewer reads has two parts. Findings that carry a file and a
// line become line-anchored comments; the body carries the agent's summary of
// the change and any finding that could not be anchored to a line in the diff.
//
// The body says nothing about Redline itself. An earlier version led with a
// preamble naming the panes that ran and the share of files they examined;
// on a change with no migrations and no spec it rendered as "none of the 16
// changed file(s) were examined", which reads as an apology and buries the
// review under it. What a reviewer wants at the top of a review is the
// change, not the reviewer's own coverage.
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

// fpMarkerPrefix tags every comment with its finding fingerprint, invisibly (an
// HTML comment), so a re-post can see what it already said and not say it twice.
// The fingerprint is stable across runs, which is what makes idempotency per
// (PR, head SHA) possible without a server to remember state. The fingerprint
// itself is a human-readable string with NUL separators, so it is hex-encoded
// in the marker — never embedded raw, where a "-->" in a message could close
// the comment early.
const fpMarkerPrefix = "redline:fp:"

// reviewMarkerPrefix tags the review body with the head SHA it was posted for,
// so a re-post against the same commit can tell "already reviewed this commit"
// from "reviewing a new push."
const reviewMarkerPrefix = "redline:review:"

var (
	fpMarkerRe     = regexp.MustCompile(regexp.QuoteMeta(fpMarkerPrefix) + `([0-9a-fA-F]+)`)
	reviewMarkerRe = regexp.MustCompile(regexp.QuoteMeta(reviewMarkerPrefix) + `([0-9a-fA-F]+)`)
)

// Comment is one line-anchored review comment. Body already carries the
// fingerprint marker; Fingerprint is kept alongside for filtering.
type Comment struct {
	Path        string
	Line        int
	Body        string
	Fingerprint string
}

// Payload is one PR review: a body and the line-anchored comments. CommitID is
// the head SHA the review is anchored to — the same SHA idempotency keys on.
type Payload struct {
	CommitID string
	Body     string
	Comments []Comment
}

// FileNote is one line of the walkthrough: what a changed file does in this
// change. Redline never derives these; they come from the agent.
type FileNote struct {
	Path    string
	Summary string
}

// Narrative is the agent's prose half of the review — the part a reviewer
// reads before any individual comment. It is what a Copilot review put at the
// top of a PR, and the reason this is a replacement for one rather than a
// findings dump beside it.
type Narrative struct {
	Summary string
	Files   []FileNote
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
	head := ""
	if tgt != nil {
		head = tgt.Head
	}
	p := Payload{CommitID: head}

	var inBody []findings.Finding
	for _, f := range rep.Findings {
		if f.File != "" && f.Line > 0 && lineCommentable(commentable, f.File, f.Line) {
			p.Comments = append(p.Comments, Comment{
				Path:        f.File,
				Line:        f.Line,
				Body:        commentBody(f),
				Fingerprint: f.Fingerprint,
			})
			continue
		}
		inBody = append(inBody, f)
	}

	p.Body = buildBody(nar, head, reportURL, inBody)
	return p
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
// fingerprint marker so a re-post can skip it.
func commentBody(f findings.Finding) string {
	var b strings.Builder
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
	b.WriteString("\n\n")
	b.WriteString(marker(fpMarkerPrefix + hex.EncodeToString([]byte(f.Fingerprint))))
	return b.String()
}

// buildBody is the review body a reviewer reads first: the agent's summary of
// the change, the file-by-file walkthrough, then any finding that could not be
// anchored to a changed line (no file:line, or a line outside the PR's diff),
// then the report link.
//
// Summary and walkthrough together are the shape of the review this replaces.
// A findings list alone is not a review: it tells a reviewer what is wrong
// without telling them what arrived.
func buildBody(nar Narrative, head, reportURL string, inBody []findings.Finding) string {
	var b strings.Builder
	if summary := strings.TrimSpace(nar.Summary); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	}
	if len(nar.Files) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("<details>\n<summary>Walkthrough</summary>\n\n")
		b.WriteString("| File | What changed |\n|---|---|\n")
		for _, f := range nar.Files {
			fmt.Fprintf(&b, "| `%s` | %s |\n", f.Path, escapeCell(f.Summary))
		}
		b.WriteString("\n</details>\n")
	}
	if len(inBody) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("### Findings not shown inline\n\n")
		for _, f := range inBody {
			fmt.Fprintf(&b, "- **%s** — %s", severityLabel(f.Severity), f.Message)
			if loc := bodyLocation(f); loc != "" {
				fmt.Fprintf(&b, " _(%s)_", loc)
			}
			b.WriteString("\n")
		}
	}
	if reportURL != "" {
		fmt.Fprintf(&b, "\n[Full report](%s)\n", reportURL)
	}
	b.WriteString("\n")
	b.WriteString(marker(reviewMarkerPrefix + head))
	return b.String()
}

// escapeCell keeps a summary containing a pipe or a newline from breaking the
// walkthrough table out of its row.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.Join(strings.Fields(s), " ")
}

func (p Payload) Unposted(posted map[string]bool) Payload {
	out := p
	out.Comments = nil
	for _, c := range p.Comments {
		if posted[c.Fingerprint] {
			continue
		}
		out.Comments = append(out.Comments, c)
	}
	return out
}

// Fingerprints extracts the finding fingerprints embedded in a set of existing
// comment or review bodies fetched from GitHub.
func Fingerprints(bodies []string) map[string]bool {
	out := map[string]bool{}
	for _, body := range bodies {
		for _, m := range fpMarkerRe.FindAllStringSubmatch(body, -1) {
			raw, err := hex.DecodeString(m[1])
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
