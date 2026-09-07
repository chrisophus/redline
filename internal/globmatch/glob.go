// Package globmatch matches repository paths against scope globs. Patterns use
// "/" as the separator, "*" for one segment, "**" across segments, and "?" for
// one character.
package globmatch

import (
	"regexp"
	"strings"
	"sync"
)

// MatchesAny reports whether path matches any of the patterns.
func MatchesAny(patterns []string, path string) bool {
	for _, p := range patterns {
		if compiled(p).MatchString(path) {
			return true
		}
	}
	return false
}

// compiled memoizes the regexp for a pattern. MatchesAny runs once per changed
// path against the same scope patterns, so compiling inside the loop rebuilt the
// same handful of patterns across the whole diff.
var (
	cacheMu sync.Mutex
	cache   = map[string]*regexp.Regexp{}
)

func compiled(pattern string) *regexp.Regexp {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if re, ok := cache[pattern]; ok {
		return re
	}
	re := toRegexp(pattern)
	cache[pattern] = re
	return re
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
