package lint

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chrisophus/redline/internal/globmatch"
)

// jsonPathGet walks a dotted, optionally array-indexed path
// ("range.start.line", "locations[0].uri") through a value decoded from
// JSON into any (map[string]any / []any / scalar) and returns what it finds.
// ok is false when a segment is missing or the value has the wrong shape. An
// empty path returns v unchanged.
func jsonPathGet(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	for _, seg := range splitPath(path) {
		if seg.isIndex {
			arr, ok := v.([]any)
			if !ok || seg.index < 0 || seg.index >= len(arr) {
				return nil, false
			}
			v = arr[seg.index]
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[seg.key]
		if !ok {
			return nil, false
		}
	}
	return v, true
}

type pathSeg struct {
	key     string
	index   int
	isIndex bool
}

// splitPath turns "a.b[0].c" into key(a), key(b), index(0), key(c).
func splitPath(path string) []pathSeg {
	var segs []pathSeg
	for _, part := range strings.Split(path, ".") {
		for part != "" {
			if part[0] == '[' {
				end := strings.IndexByte(part, ']')
				if end < 0 {
					// Malformed; treat the remainder as a literal key so a
					// typo surfaces as a missing field, not a panic.
					segs = append(segs, pathSeg{key: part})
					part = ""
					continue
				}
				n, _ := strconv.Atoi(part[1:end])
				segs = append(segs, pathSeg{index: n, isIndex: true})
				part = part[end+1:]
				continue
			}
			if br := strings.IndexByte(part, '['); br >= 0 {
				segs = append(segs, pathSeg{key: part[:br]})
				part = part[br:]
			} else {
				segs = append(segs, pathSeg{key: part})
				part = ""
			}
		}
	}
	return segs
}

// asString coerces a JSON-decoded value to the string Issue fields need,
// tolerating the number-is-float64 fact and a stringly-typed number.
func asString(v any, ok bool) string {
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

// asInt coerces a JSON-decoded value to an int line number, tolerating both
// the float64 JSON numbers decode to and a number written as a string.
func asInt(v any, ok bool) int {
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

// matchesAnyGlob reports whether path matches any of the patterns, where a
// pattern uses "*" for one path segment, "**" across segments, and "?" for a
// single character.
func matchesAnyGlob(patterns []string, path string) bool {
	return globmatch.MatchesAny(patterns, path)
}
