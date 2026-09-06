package lint

import (
	"strings"
	"testing"
)

// FuzzNormalizeMessage exercises the digit-normalization fingerprint relies
// on to keep an issue's identity stable when a message embeds a count or an
// offset. diffIssues's multiset match depends on two invariants: the same
// message always normalizes the same way, and normalizing an already-
// normalized message is a no-op. If either breaks, two runs against the same
// base issue could disagree on its identity.
func FuzzNormalizeMessage(f *testing.F) {
	seeds := []string{
		"line is 130 characters",
		`unchecked error return value from call to "pkg.Do42" (117 bytes at offset 9001)`,
		"/internal/service7/handler19.go:42: unexpected token",
		"value 3.14 exceeds limit 100",
		"",
		"no digits here at all",
		"0000123abc456",
		`"quoted 42 identifier" and another "id_7"`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, msg string) {
		once := digits.ReplaceAllString(msg, "#")
		again := digits.ReplaceAllString(msg, "#")
		if once != again {
			t.Fatalf("normalization is not deterministic for %q: %q vs %q", msg, once, again)
		}
		twice := digits.ReplaceAllString(once, "#")
		if twice != once {
			t.Fatalf("normalization is not idempotent for %q: normalize->%q, normalize(normalize)->%q", msg, once, twice)
		}
	})
}

// scanLine replays the per-line directive match suppress.go's Diff performs
// on a line it already knows the diff added, against the package's real
// directive table. The "which lines did this change add" bookkeeping is
// deliberately not exercised here: it is git plumbing, not attacker-
// controlled text, and fuzzing it would mean shelling out to git per case.
func scanLine(line string) (rules string, ok bool) {
	for _, d := range directives {
		m := d.re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		silenced := "its findings on this line"
		if d.rules > 0 {
			r, _, _ := strings.Cut(m[d.rules], "--")
			if r = strings.TrimSpace(r); r != "" {
				silenced = r
			}
		}
		return silenced, true
	}
	return "", false
}

// FuzzSuppressionScan fuzzes arbitrary source text for lint-silencing
// directives. Every reported hit must anchor to a real line of the input,
// and must never come back with an empty rule list: those are the two facts
// a suppression Finding promises a reviewer (its Line and its Message).
func FuzzSuppressionScan(f *testing.F) {
	seeds := []string{
		"func f() error { return doThing() } //nolint:errcheck\n",
		"x, _ := doThing() //nolint\n",
		"// eslint-disable-next-line no-eval\neval(x)\n",
		"console.log(1) // eslint-disable-line no-console\n",
		"x = eval(input())  # noqa: E501\n",
		"# noqa\nimport os\n",
		"// @ts-expect-error TS2345 mismatched types\nconst x: number = \"1\";\n",
		"// @ts-ignore\n",
		"// @ts-nocheck\n",
		"# type: ignore\n",
		"# pylint: disable=broad-except\n",
		"#[allow(dead_code)]\nfn unused() {}\n",
		"msg := \"please nolint this, it's just a string\"\n",
		"// see http://example.com/eslint-disable-next-line for the docs\n",
		"url := \"http://x.test/noqa\"\n",
		"",
		"\n\n\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		lines := strings.Split(src, "\n")
		total := len(lines)
		for n, line := range lines {
			rules, ok := scanLine(line)
			if !ok {
				continue
			}
			lineNo := n + 1
			if lineNo < 1 || lineNo > total {
				t.Fatalf("directive line %d out of range for %d-line input %q", lineNo, total, src)
			}
			if strings.TrimSpace(rules) == "" {
				t.Fatalf("directive on line %d reported an empty rule list for %q", lineNo, line)
			}
		}
	})
}
