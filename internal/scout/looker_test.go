package scout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	l := NewLooker(lookerTree(t))
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
	l := NewLooker(lookerTree(t))
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
