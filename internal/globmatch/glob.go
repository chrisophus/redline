// Package globmatch matches repository paths against scope globs. Patterns use
// "/" as the separator, "*" for one segment, "**" across segments, and "?" for
// one character.
package globmatch

import (
	"regexp"
	"strings"
)

// MatchesAny reports whether path matches any of the patterns.
func MatchesAny(patterns []string, path string) bool {
	for _, p := range patterns {
		if toRegexp(p).MatchString(path) {
			return true
		}
	}
	return false
}

func toRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pattern[i] == '*':
			b.WriteString("[^/]*")
			i++
		case pattern[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
