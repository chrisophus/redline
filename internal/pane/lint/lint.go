// Package lint is the lint half of the evidence: what this change does to the
// repository's own linters. Three panes, all deterministic.
//
//   - Delta runs the configured linters at the base revision and at head and
//     reports only what the change introduces, plus what it resolves.
//   - Suppressions collects every silencing directive the diff itself adds:
//     the author told the linter to be quiet, and the reviewer should see
//     where.
//   - Config reports lint-config drift: a rule disabled, downgraded, or newly
//     excluded by this change, so a clean delta earned by turning a rule off
//     reads as what it is.
//
// CI gates lint errors already; re-reporting what CI reports is noise. The
// signal is the movement: new violations before CI sees them, silencings, and
// config edits that change what "clean" means.
package lint

import (
	"regexp"
	"strings"
)

// Issue is one linter finding, normalized across tools.
type Issue struct {
	Tool     string `json:"tool"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // "warning" | "info"
}

// digits collapses number runs so a message that embeds a count or a position
// does not change identity when the count does.
var digits = regexp.MustCompile(`\d+`)

// fingerprint identifies an issue across the two runs: file, rule, and
// digit-normalized message, with no line number. Code that merely moved keeps
// its findings' identity; the line is presentation, not identity.
func fingerprint(i Issue) string {
	msg := digits.ReplaceAllString(i.Message, "#")
	return i.Tool + "\x00" + i.File + "\x00" + i.Rule + "\x00" + msg
}

func hasSuffixAny(path string, suffixes ...string) bool {
	lower := strings.ToLower(path)
	for _, s := range suffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}
