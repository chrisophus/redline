// Package post turns a finished Redline session into one GitHub pull request
// review and submits it. It is the deliberate, explicit counterpart to the
// rest of Redline, which only ever reads from GitHub: nothing here runs unless
// the operator typed `redline post`, and `run`, `review`, and `ingest` never
// reach it.
//
// The review a reviewer reads has two parts. Findings that carry a file and a
// line become line-anchored comments; the rest, and the coverage preamble that
// says what was and was not checked, go in the review body. The body is the
// part no free reviewer provides — a Copilot comment tells you what it found,
// never what it looked at.
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

// Build assembles the review from a finished report. reportURL is a link to the
// full HTML report (a CI artifact URL when the caller has one); it is omitted
// from the body when empty rather than rendered as a dead link.
func Build(rep *findings.Report, tgt *target.Target, reportURL string) Payload {
	head := ""
	if tgt != nil {
		head = tgt.Head
	}
	p := Payload{CommitID: head}

	var located, unlocated []findings.Finding
	for _, f := range rep.Findings {
		if f.File != "" && f.Line > 0 {
			located = append(located, f)
		} else {
			unlocated = append(unlocated, f)
		}
	}

	for _, f := range located {
		p.Comments = append(p.Comments, Comment{
			Path:        f.File,
			Line:        f.Line,
			Body:        commentBody(f),
			Fingerprint: f.Fingerprint,
		})
	}

	p.Body = buildBody(rep, head, reportURL, unlocated)
	return p
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

// buildBody is the review body: the coverage preamble that is the whole reason
// this beats a bare Copilot review, then any findings with no file:line to
// anchor to, then the report link and the head-SHA marker.
func buildBody(rep *findings.Report, head, reportURL string, unlocated []findings.Finding) string {
	var b strings.Builder
	b.WriteString("## Redline review\n\n")
	b.WriteString(preamble(rep))

	if len(unlocated) > 0 {
		b.WriteString("\n\n### Findings without a line to anchor to\n\n")
		for _, f := range unlocated {
			fmt.Fprintf(&b, "- **%s** — %s", severityLabel(f.Severity), f.Message)
			if f.Substrate != "" {
				fmt.Fprintf(&b, " _(%s)_", f.Substrate)
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

// preamble states who reviewed, what was checked, and what was not. It reads
// this off the report's own substrate and coverage records rather than a fixed
// catalog, so it can never claim a check ran that did not.
func preamble(rep *findings.Report) string {
	var reviewers, checked, notChecked []string
	for _, s := range rep.Substrates {
		name := humanSubstrate(s.Name)
		if r, ok := strings.CutPrefix(s.Name, "reviewer:"); ok {
			if s.State == findings.SubstrateRan {
				reviewers = append(reviewers, r)
			}
		}
		switch s.State {
		case findings.SubstrateRan:
			checked = append(checked, name)
		case findings.SubstrateSkipped, findings.SubstrateFailed:
			notChecked = append(notChecked, name)
		}
	}
	sort.Strings(reviewers)
	checked = dedupeSorted(checked)
	notChecked = dedupeSorted(notChecked)

	var b strings.Builder
	if len(reviewers) > 0 {
		fmt.Fprintf(&b, "Reviewed by %s.", oxford(reviewers))
	} else {
		b.WriteString("Reviewed with Redline's observed checks.")
	}
	b.WriteString(" This review reports; it does not gate.\n")

	if len(checked) > 0 {
		fmt.Fprintf(&b, "\n**Checked:** %s.\n", strings.Join(checked, ", "))
	}
	if len(notChecked) > 0 {
		fmt.Fprintf(&b, "\n**Not checked:** %s.\n", strings.Join(notChecked, ", "))
	}

	cov := rep.Coverage
	switch {
	case cov.ChangedFiles == 0:
		b.WriteString("\n**Coverage:** no files changed.\n")
	case cov.ExaminedFiles == 0:
		fmt.Fprintf(&b, "\n**Coverage:** none of the %d changed file(s) were examined — this review is the agent's read, not observed evidence.\n", cov.ChangedFiles)
	default:
		fmt.Fprintf(&b, "\n**Coverage:** %d of %d changed file(s) examined.\n", cov.ExaminedFiles, cov.ChangedFiles)
	}

	n := rep.NewCount
	fmt.Fprintf(&b, "\n**Findings:** %d error, %d warning, %d info.\n",
		n[findings.SeverityError], n[findings.SeverityWarning], n[findings.SeverityInfo])
	return b.String()
}

// Unposted drops the comments whose fingerprint is already present on the PR,
// leaving only what this post would newly add. The body is left intact: it
// carries the current coverage summary, which is worth restating.
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

// humanSubstrate turns an internal substrate name into review prose:
// "reviewer:claude" reads as "claude review", "redline/review" as the agent's
// review, and a pane name is left as it is.
func humanSubstrate(name string) string {
	if r, ok := strings.CutPrefix(name, "reviewer:"); ok {
		return r + " review"
	}
	switch name {
	case "redline/review":
		return "agent review"
	default:
		return name
	}
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func oxford(in []string) string {
	switch len(in) {
	case 0:
		return ""
	case 1:
		return in[0]
	case 2:
		return in[0] + " and " + in[1]
	default:
		return strings.Join(in[:len(in)-1], ", ") + ", and " + in[len(in)-1]
	}
}
