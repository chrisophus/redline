package scout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/review"
)

// The Looker satisfies the contract the judging pass calls through. Written as
// an assignment so the two cannot drift apart silently: review declares the
// interface and cannot import this package to check anything implements it.
var _ review.Looker = (*Looker)(nil)

// The cap is in the tool's description, which is a promise to the model, and
// enforced here. A cap that moved on one side would leave the model told one
// thing and given another.
func TestTheReadCapMatchesWhatTheToolPromises(t *testing.T) {
	if review.MaxReadLines != maxReadLines {
		t.Fatalf("read_lines promises %d lines and the scout caps at %d",
			review.MaxReadLines, maxReadLines)
	}
}

func lookerTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("store.go", "package store\n\nfunc Insert() error {\n\treturn nil\n}\n")
	write("other/use.go", "package other\n\nfunc Use() { _ = Insert }\n")
	return dir
}

func TestLookerGrepFindsAndReports(t *testing.T) {
	l := NewLooker(lookerTree(t), "")
	out, err := l.Grep("func Insert", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "store.go") {
		t.Errorf("grep did not name the file:\n%s", out)
	}
	// A glob narrows by path substring, which is what the description says.
	out, err = l.Grep("Insert", "other/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "store.go") {
		t.Errorf("the glob did not narrow the search:\n%s", out)
	}
	if _, err := l.Grep("(", ""); err == nil {
		t.Error("a bad pattern must come back as an error the pass can fix")
	}
	if _, err := l.Grep("  ", ""); err == nil {
		t.Error("an empty pattern must be refused rather than matching everything")
	}
}

func TestLookerReadsASpanAndRefusesEscapes(t *testing.T) {
	l := NewLooker(lookerTree(t), "")
	out, err := l.ReadLines("store.go", 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "store.go:3-5") || !strings.Contains(out, "func Insert") {
		t.Errorf("read did not return the span with its location:\n%s", out)
	}
	// The hardened paths are the reason this delegates rather than reimplements.
	if _, err := l.ReadLines("../../etc/passwd", 1, 2); err == nil {
		t.Error("a path climbing out of the tree must be refused")
	}
	if _, err := l.ReadLines("store.go", 9000, 9001); err == nil {
		t.Error("a start past the end of the file must be an error, not an empty result")
	}
}

// Every lookup returns about as much as every other. A cap is a judgement
// about what one answer may take of the conversation, and that judgement has
// to be made in tokens: three of these count lines, matches or documents, and
// two count bytes, so the same number means different amounts in each.
//
// The trap is the ratio. Prose runs near four characters per token and the
// familiar figure is that one; envelope measures Redline's payloads at 2.33,
// because they are code, diffs and JSON. Costed at four, 64 KiB reads as
// sixteen thousand tokens and looks level with the rest. Costed at the real
// ratio it is twenty-eight thousand, twice read_lines and four times grep,
// and one lookup can take the conversation on its own.
func TestTheLookupCapsReturnComparableAmounts(t *testing.T) {
	rep := func(s string, n int) int { return envelope.EstimateTokens(strings.Repeat(s, n)) }
	sizes := map[string]int{
		"read_lines":     rep("  1234\tif err := doTheThing(ctx, arg); err != nil {\n", maxReadLines),
		"grep":           rep("internal/review/prompt.go:412: func (in Input) coverageSection() string {\n", maxGrepMatches),
		"list_docs":      rep("docs/adr/0042-use-advisory-locks.md (120 lines): Use advisory locks for the lease\n", maxLookDocs),
		"symbol_context": envelope.EstimateTokensLen(maxSymbolContextBytes),
		"line_history":   envelope.EstimateTokensLen(maxLineHistoryBytes),
	}
	lo, hi := "", ""
	for name, n := range sizes {
		if lo == "" || n < sizes[lo] {
			lo = name
		}
		if hi == "" || n > sizes[hi] {
			hi = name
		}
	}
	// Three is room to differ for a reason and not room for one lookup to be
	// in a different class from the others.
	const maxSpread = 3.0
	if got := float64(sizes[hi]) / float64(sizes[lo]); got > maxSpread {
		t.Errorf("%s returns %d tokens and %s returns %d, a spread of %.1fx (max %.1f). Sizes: %v",
			hi, sizes[hi], lo, sizes[lo], got, maxSpread, sizes)
	}
	// And the bound that the sizes above cannot check. Those are priced off a
	// representative line, and the failure this catches is content that is not
	// representative: a search of this repository for BaseSHA came back at
	// 220,095 bytes inside a cap of 200 matches, because one matched line was
	// 19,391 characters. A cap counted in matches, lines or documents does not
	// bound bytes, so every lookup answers within one byte bound as well.
	if maxLookupBytes <= 0 {
		t.Fatal("every lookup needs a byte bound; a unit count does not give one")
	}
	if tok := envelope.EstimateTokensLen(maxLookupBytes); tok > sizes[lo]*int(maxSpread) {
		t.Errorf("the byte bound is %d tokens, more than %.0fx the smallest lookup (%s at %d)",
			tok, maxSpread, lo, sizes[lo])
	}
}

// A single matched line is clipped. The byte bound alone would let one line
// take the whole answer, which is a search that returns one result.
func TestALongMatchIsClipped(t *testing.T) {
	long := strings.Repeat("x", maxMatchLine*3)
	got := clipLine(long)
	if len(got) > maxMatchLine+40 {
		t.Errorf("a %d character line came back as %d", len(long), len(got))
	}
	if !strings.Contains(got, "line continues") {
		t.Errorf("a clipped line must say it was clipped: %q", got[len(got)-40:])
	}
	if short := clipLine("if err != nil {"); short != "if err != nil {" {
		t.Errorf("an ordinary line must pass through: %q", short)
	}
}

// In a pull request worktree HEAD is the change under review, so a history
// walk that keeps its commits answers "why is this line here" with the diff
// the reviewer is already reading. gorefactor's own walk made this mistake and
// measured it: fifteen of twenty-one history expansions came back carrying
// exactly one commit, the commit being reviewed.
func TestLineHistorySkipsTheChangeUnderReview(t *testing.T) {
	const older = "commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"Author: A <a@example.com>\nDate:   2026-01-01\n\n    add the guard\n\n@@ -1 +1 @@\n+guard\n"
	const mine = "commit bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"Author: B <b@example.com>\nDate:   2026-09-20\n\n    the change under review\n\n@@ -1 +1 @@\n-guard\n"

	got, dropped := withoutChangeCommits(mine+older, map[string]bool{
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": true,
	})
	if dropped != 1 {
		t.Errorf("dropped = %d, want the one commit of the change", dropped)
	}
	if strings.Contains(got, "the change under review") {
		t.Errorf("the change's own commit must not be its own explanation:\n%s", got)
	}
	if !strings.Contains(got, "add the guard") || !strings.Contains(got, "+guard") {
		t.Errorf("the prior history must survive whole:\n%s", got)
	}
	// No base known means nothing is filtered, rather than guessing.
	if out, n := withoutChangeCommits(mine+older, nil); out != mine+older || n != 0 {
		t.Error("without a base the history is left as git printed it")
	}
}
