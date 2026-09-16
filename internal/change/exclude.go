package change

import (
	"regexp"
	"strings"
)

// ExcludeReason names the pattern from .redline.yml that drops a path, or
// returns empty when none does.
//
// The name rules and the header sniff beside this file cover generators that
// say what they are. A repository whose generator does neither, writing
// ordinary filenames with no marker, has no way to tell Redline that without
// this. The cost of getting it wrong is the same asymmetric one: a hand-written
// file dropped from the change is a file the reviewer never learns about, so an
// excluded path is reported exactly like a generated one, named and counted,
// and the reason carries the pattern that did it.
func ExcludeReason(path string, patterns []string) string {
	for _, pattern := range patterns {
		if matchExclude(path, pattern) {
			return "excluded by .redline.yml: " + pattern
		}
	}
	return ""
}

// matchExclude reports whether a repository-relative path matches one pattern.
//
// The rules are the ones a .gitignore reader already knows, minus the parts
// that need a filesystem walk:
//
//   - A pattern with no slash matches the file's name in any directory, so
//     *.snap covers every snapshot in the tree.
//   - A pattern ending in / matches everything under that directory.
//   - ** matches any number of path segments, including none.
//   - * and ? match within one segment and never cross a slash.
//
// Matching is case-sensitive, because git is: a pattern that works on a Linux
// runner has to mean the same thing on the macOS checkout beside it.
func matchExclude(path, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	path = strings.TrimPrefix(strings.TrimSpace(path), "./")
	if pattern == "" || path == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	if !strings.Contains(pattern, "/") {
		// A bare name is about the file, wherever it sits.
		if i := strings.LastIndex(path, "/"); i >= 0 {
			path = path[i+1:]
		}
	}
	re, err := excludeRegexp(pattern)
	if err != nil {
		// Every character outside the glob syntax is quoted, so this is close
		// to unreachable. It stays because the alternative to returning false
		// is panicking on a config file, and a pattern that silently matches
		// nothing is the safe direction: the files stay in the review.
		return false
	}
	return re.MatchString(path)
}

// excludeRegexp translates one glob into an anchored expression. Cached
// nowhere: a change carries tens of paths and a config a handful of patterns,
// so the compile cost is noise next to reading the diffs.
func excludeRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				// A /**/ in the middle also matches nothing, so api/**/gen
				// covers api/gen. Trailing ** takes the rest of the path.
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
					continue
				}
				b.WriteString(".*")
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
