// Package dispositions works out what happened to each review finding on a
// pull request: fixed, declined, left open, gone stale, or never posted as a
// thread. It covers Redline's findings, other review bots' comments, and
// people's. It reads GitHub and git and writes nothing anywhere: rows leave
// the package as plain structs, and `redline dispositions` writes them as
// JSONL.
package dispositions

import (
	"strings"

	"github.com/chrisophus/redline/internal/post"
)

// Markers is what a posted Redline comment or review body says about itself.
type Markers struct {
	// HeadSHA is the commit the finding was said for (the head in the fp marker).
	HeadSHA string
	// Fingerprint is the decoded finding fingerprint. It moves when the model rewords a finding.
	Fingerprint string
	// Question is the decoded question key. It is SHA-free and survives rewording, so it is the cross-run key.
	Question string
	// ReviewHead is the commit a summary review was posted for (the redline:review marker).
	ReviewHead string
}

// ParseMarkers reads the Redline markers out of a comment or review body,
// through the readers in internal/post, which is where the markers are
// written. Fields stay empty when absent or when the hex does not decode.
func ParseMarkers(body string) Markers {
	var m Markers
	if fs := post.PostedFindings(body); len(fs) > 0 {
		m.HeadSHA, m.Fingerprint = fs[0].Head, fs[0].Fingerprint
	}
	m.Question = post.QuestionIn(body)
	if heads := post.ReviewedHeads([]string{body}); len(heads) > 0 {
		m.ReviewHead = strings.ToLower(heads[0])
	}
	return m
}

// Evidence is a parsed evidence comment: a PR comment, found by a marker the
// caller names, holding a claim table and a Not verified list.
type Evidence struct {
	Present     bool
	Verified    int
	Refuted     int
	NotVerified int
}

// ParseEvidence reads the claim table and the Not verified list of a comment body. A body without marker,
// or an empty marker, yields the zero value. Counting is deliberately forgiving about wording: it counts table rows whose last
// cell starts with Verified or Refuted, and bullets under a "Not verified" heading.
func ParseEvidence(body, marker string) Evidence {
	if marker == "" || !strings.Contains(body, marker) {
		return Evidence{}
	}
	ev := Evidence{Present: true}
	inNotVerified := false
	for raw := range strings.SplitSeq(body, "\n") {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "**not verified**") || strings.HasPrefix(lower, "### not verified") || strings.HasPrefix(lower, "## not verified"):
			inNotVerified = true
			continue
		case strings.HasPrefix(lower, "**residual risk**") || strings.HasPrefix(lower, "## residual") || strings.HasPrefix(lower, "### residual"):
			inNotVerified = false
			continue
		}
		if inNotVerified {
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
				ev.NotVerified++
			}
			continue
		}
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		result := strings.ToLower(strings.TrimSpace(cells[len(cells)-1]))
		switch {
		case strings.HasPrefix(result, "verified"):
			ev.Verified++
		case strings.HasPrefix(result, "refuted"):
			ev.Refuted++
		case strings.HasPrefix(result, "not verified"):
			ev.NotVerified++
		}
	}
	return ev
}
