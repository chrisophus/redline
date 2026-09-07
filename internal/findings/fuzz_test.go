package findings_test

import (
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// FuzzFingerprint asserts Fingerprint's two load-bearing properties: it is
// stable for a given input (it is the join key for human comment state
// across runs, so the same finding must always fingerprint the same way),
// and it never panics on arbitrary bytes, including invalid UTF-8 — a
// Message field is untrusted text that started life in a diff or a tool's
// stdout.
//
// It also checks NormalizeMessage's structural contract: after collapsing
// digit runs to a single '#', re-normalizing must be a no-op, since no
// digits remain to collapse. That is the property Fingerprint relies on to
// treat "line 12" and "line 947" as the same fingerprint.
func FuzzFingerprint(f *testing.F) {
	seeds := []struct{ file, rule, message string }{
		{"internal/report/html.go", "migration-modified-after-merge", "column orders.total dropped at line 42"},
		{"", "endpoint-removed", "DELETE /v1/users/123 removed without deprecation"},
		{"web/src/App.tsx", "component-prop-removed", "prop onClose removed from Button (used 7 times)"},
		{"internal/findings/findings.go", "suppression-added", "nolint:errcheck added without justification"},
		{"", "diff-coverage-below-threshold", "42% of added lines are covered, threshold is 80%"},
	}
	for _, s := range seeds {
		f.Add(s.file, s.rule, s.message)
	}
	// A lone UTF-8 continuation byte: not valid UTF-8, must not panic.
	f.Add("a.go", "rule", string([]byte{0xff, 0xfe, 'x'}))

	f.Fuzz(func(t *testing.T, file, rule, message string) {
		fnd := findings.Finding{File: file, Rule: rule, Message: message}
		got := findings.Fingerprint(fnd)
		again := findings.Fingerprint(fnd)
		if got != again {
			t.Fatalf("Fingerprint is not stable for the same input: %q vs %q", got, again)
		}

		norm1 := findings.NormalizeMessage(message)
		norm2 := findings.NormalizeMessage(norm1)
		if norm1 != norm2 {
			t.Fatalf("NormalizeMessage is not idempotent: %q -> %q -> %q", message, norm1, norm2)
		}
	})
}
