package findings

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ReviewKey returns the identity of a DEFECT, independent of who reported it.
// Two findings with the same ReviewKey are considered the same defect reported
// by different reviewers.
//
// Identity is derived from: normalized file path, the line number, and a normalized
// form of the message/title. The message is normalized by lowercasing, stripping
// punctuation, collapsing whitespace, then keeping only the first 12 words. This
// normalization is necessary because two independent reviewers may describe the
// same defect in different prose; a whole-string match would never collapse them.
// A normalized prefix over the same file:line provides a defensible join key that
// captures the essential defect while being forgiving of wording differences.
//
// Returns a short stable hex hash.
func ReviewKey(f Finding) string {
	// Normalize file path (lowercase, trim)
	file := strings.ToLower(strings.TrimSpace(f.File))

	// Normalize message: lowercase, strip punctuation, collapse whitespace, take first 12 words
	msg := normalizeMessage(f.Message)

	// Build identity string
	identity := fmt.Sprintf("%s:%d:%s", file, f.Line, msg)

	// Hash to short hex
	h := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", h[:8]) // First 8 bytes = 16 hex chars
}

// normalizeMessage normalizes a message for ReviewKey comparison.
// It lowercases, strips punctuation, collapses whitespace, and keeps
// only the first 12 words.
func normalizeMessage(msg string) string {
	// Lowercase
	msg = strings.ToLower(msg)

	// Remove punctuation; keep alphanumeric, whitespace, and hyphen for compound words.
	// Go regexp doesn't support \w and \s, so we explicitly match non-alphanumeric,
	// non-whitespace, non-hyphen characters.
	reg := regexp.MustCompile(`[^a-z0-9\s\-]`)
	msg = reg.ReplaceAllString(msg, "")

	// Collapse multiple whitespace into single spaces
	msg = strings.Join(strings.Fields(msg), " ")

	// Keep only first 12 words
	words := strings.Fields(msg)
	if len(words) > 12 {
		words = words[:12]
	}
	return strings.Join(words, " ")
}

// Merge collapses findings sharing a ReviewKey into one. The survivor keeps
// the highest severity seen (error > warning > info), the longest Message
// (the most explanatory wording wins), and records every contributing reviewer
// in the Reporters field.
//
// Merge accepts any number of Finding slices as variadic arguments, allowing
// flexible grouping of findings from different sources.
//
// Output ordering is deterministic (required for the same tree -> same output
// guarantee): sorted by severity (error first), then file, then line, then
// ReviewKey. Reporters are sorted and deduplicated.
func Merge(groups ...[]Finding) []Finding {
	// Collect all findings and group by ReviewKey
	byKey := make(map[string]*Finding)
	var keyOrder []string // Track insertion order of first appearance

	for _, group := range groups {
		for _, f := range group {
			key := ReviewKey(f)

			if existing, ok := byKey[key]; !ok {
				// First time seeing this key
				keyOrder = append(keyOrder, key)
				// Make a copy to avoid mutation of input
				f := f
				// Initialize Reporters with this finding's reviewer
				if f.Reviewer != "" {
					f.Reporters = []string{f.Reviewer}
				}
				byKey[key] = &f
			} else {
				// Merge into existing

				// Promote severity: error > warning > info
				if severityRank(f.Severity) < severityRank(existing.Severity) {
					existing.Severity = f.Severity
				}

				// Keep longer message (more explanatory)
				if len(f.Message) > len(existing.Message) {
					existing.Message = f.Message
				}

				// Add reviewer to the list (will deduplicate later)
				if f.Reviewer != "" {
					existing.Reporters = append(existing.Reporters, f.Reviewer)
				}
			}
		}
	}

	// Build result, deduplicate and sort Reporters for each finding
	var result []Finding
	for _, key := range keyOrder {
		f := byKey[key]

		// Deduplicate and sort Reporters
		if len(f.Reporters) > 0 {
			reporters := make(map[string]bool)
			for _, r := range f.Reporters {
				reporters[r] = true
			}
			f.Reporters = make([]string, 0, len(reporters))
			for r := range reporters {
				f.Reporters = append(f.Reporters, r)
			}
			sort.Strings(f.Reporters)
		}

		result = append(result, *f)
	}

	// Sort result deterministically: severity, file, line, ReviewKey
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]

		// Severity: error < warning < info (lower rank = higher priority)
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}

		// File
		if a.File != b.File {
			return a.File < b.File
		}

		// Line
		if a.Line != b.Line {
			return a.Line < b.Line
		}

		// ReviewKey (as final tiebreaker)
		return ReviewKey(a) < ReviewKey(b)
	})

	return result
}

// severityRank returns a numeric rank for sorting (lower = higher priority).
func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	case SeverityInfo:
		return 2
	default:
		return 3
	}
}

// confidenceRank returns a numeric rank for sorting (lower = higher priority).
// High > Medium > Low > Empty.
func confidenceRank(c string) int {
	switch strings.ToLower(c) {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3 // empty or unknown
	}
}

// Rank returns a copy of the findings sorted for human reading.
// Priority order: more reporters first, then severity (error first),
// then confidence (high > medium > low > empty), then file/line.
// Stable and deterministic.
func Rank(fs []Finding) []Finding {
	result := make([]Finding, len(fs))
	copy(result, fs)

	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]

		// More reporters first (descending)
		if len(a.Reporters) != len(b.Reporters) {
			return len(a.Reporters) > len(b.Reporters)
		}

		// Severity: error < warning < info
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}

		// Confidence: high > medium > low > empty
		if confidenceRank(a.Confidence) != confidenceRank(b.Confidence) {
			return confidenceRank(a.Confidence) < confidenceRank(b.Confidence)
		}

		// File
		if a.File != b.File {
			return a.File < b.File
		}

		// Line
		if a.Line != b.Line {
			return a.Line < b.Line
		}

		// Final determinism tiebreaker
		return ReviewKey(a) < ReviewKey(b)
	})

	return result
}
