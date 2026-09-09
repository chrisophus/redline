package scout

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
)

var files = map[string]string{
	"internal/store/user.go": "package store\n\ntype Store struct{}\n\n// Insert writes a row.\nfunc (s *Store) Insert(email string) error {\n\treturn nil\n}\n",
	"db/0007_email.sql":      "ALTER TABLE users\n\tADD COLUMN email TEXT NOT NULL;\n",
}

func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The rule the whole design rests on: the scout says where, this program says
// what. A record whose symbol claims one thing and whose lines hold another
// still sends the lines, because the model never gets to write the content.
func TestContentComesFromTheTreeNotTheModel(t *testing.T) {
	root := tree(t)
	r := newResolver(root, Limits{})
	x, ok := r.resolve(record{
		Role:      envelope.RoleType,
		File:      "internal/store/user.go",
		StartLine: 6,
		EndLine:   8,
		Symbol:    "Insert, which validates the address and retries",
	}, 90)
	if !ok {
		t.Fatal("a valid record did not resolve")
	}
	if !strings.Contains(x.Content, "func (s *Store) Insert(email string) error {") {
		t.Errorf("content is not the file's own bytes:\n%s", x.Content)
	}
	for _, invented := range []string{"validates", "retries"} {
		if strings.Contains(x.Content, invented) {
			t.Errorf("the scout's description reached the content: %q", invented)
		}
	}
}

func TestRecordsOutsideTheTreeAreRefused(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	for _, path := range []string{"../../etc/passwd", "/etc/passwd"} {
		err := r.validate(record{Role: envelope.RoleType, File: path, StartLine: 1, EndLine: 2})
		if err == nil {
			t.Errorf("%s was accepted; a path the model names must not escape the tree", path)
		}
	}
}

func TestInventedRolesAreRefused(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	err := r.validate(record{Role: "background", File: "internal/store/user.go", StartLine: 1, EndLine: 2})
	if err == nil {
		t.Fatal("an invented role was accepted; a provider must not invent roles")
	}
	if !strings.Contains(err.Error(), "enclosing") {
		t.Errorf("the refusal does not say what the roles are, so the scout cannot correct itself: %v", err)
	}
	// Test is deliberately not offered: Redline holds test context back, so a
	// scout turn spent on one is spent for nothing.
	if err := r.validate(record{Role: envelope.RoleTest, File: "internal/store/user.go", StartLine: 1, EndLine: 2}); err == nil {
		t.Error("a test expansion was accepted; Redline holds those back anyway")
	}
}

func TestMisrememberedLocationsAreRefusedWithTheReason(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	err := r.validate(record{Role: envelope.RoleType, File: "internal/store/absent.go", StartLine: 1, EndLine: 2})
	if err == nil || !strings.Contains(err.Error(), "absent.go") {
		t.Errorf("a path that is not in the tree must be refused by name: %v", err)
	}
	err = r.validate(record{Role: envelope.RoleType, File: "internal/store/user.go", StartLine: 900, EndLine: 950})
	if err == nil || !strings.Contains(err.Error(), "past the end") {
		t.Errorf("a line past the end must say so, so the scout can look again: %v", err)
	}
}

// A span that runs off the end is clamped rather than refused: the intent is
// clear and a refusal would cost a turn.
func TestOverlongSpanIsClamped(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	x, ok := r.resolve(record{Role: envelope.RoleEnclosing, File: "internal/store/user.go", StartLine: 6, EndLine: 4000}, 90)
	if !ok {
		t.Fatal("did not resolve")
	}
	if x.EndLine != 8 {
		t.Errorf("endLine = %d, want the last line of the file", x.EndLine)
	}
}

func TestLineBoundIsMarkedWhenItBinds(t *testing.T) {
	r := newResolver(tree(t), Limits{MaxLines: 2})
	x, _ := r.resolve(record{Role: envelope.RoleEnclosing, File: "internal/store/user.go", StartLine: 1, EndLine: 8}, 90)
	if x.EndLine != 2 {
		t.Errorf("span = %d-%d, want two lines", x.StartLine, x.EndLine)
	}
	if x.Details["span"] != "truncated" {
		t.Error("a bounded span did not say it was bounded")
	}
}

func TestGraphFoundContextSaysItWasResolvedByName(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	x, _ := r.resolve(record{
		Role: RoleNeighbor, File: "db/0007_email.sql", StartLine: 1, EndLine: 2,
		Symbol: "users", FoundVia: "graph",
	}, 90)
	if x.Details["resolution"] != "name" {
		t.Error("a graph-sourced expansion must not read as a resolved fact")
	}
	if x.Details["foundVia"] != "graph" {
		t.Errorf("foundVia = %q", x.Details["foundVia"])
	}
}

func TestFoundViaIsAClosedSet(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	err := r.validate(record{
		Role: envelope.RoleType, File: "internal/store/user.go", StartLine: 1, EndLine: 2,
		FoundVia: "I reasoned about it and concluded it mattered",
	})
	if err == nil {
		t.Fatal("free text was accepted as provenance; it is a closed set so the reader can check it")
	}
}

func TestExpansionsDedupeAndRankByRole(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	got := r.Expansions([]record{
		{Role: RoleNeighbor, File: "db/0007_email.sql", StartLine: 1, EndLine: 2, Symbol: "users"},
		{Role: envelope.RoleEnclosing, File: "internal/store/user.go", StartLine: 6, EndLine: 8, Symbol: "Insert"},
		{Role: envelope.RoleEnclosing, File: "internal/store/user.go", StartLine: 6, EndLine: 8, Symbol: "Insert again"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d expansions, want 2 after the duplicate is dropped", len(got))
	}
	if got[0].Role != envelope.RoleEnclosing {
		t.Errorf("first role = %q; Redline's rank decides the order, not the scout's", got[0].Role)
	}
}

// History content is git's, not the file's: the lines a change deleted are
// not in the working tree to read.
func TestHistoryContentComesFromGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := tree(t)
	for _, args := range [][]string{
		{"init", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
		{"add", "-A"}, {"commit", "-m", "a guard nobody should remove"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	r := newResolver(root, Limits{})
	x, ok := r.resolve(record{
		Role: envelope.RoleHistory, File: "internal/store/user.go", StartLine: 6, EndLine: 8, Symbol: "Insert",
	}, 90)
	if !ok {
		t.Fatalf("history did not resolve: %v", r.Notes())
	}
	if !strings.Contains(x.Content, "a guard nobody should remove") {
		t.Errorf("history content is not git's log:\n%s", x.Content)
	}
}

func TestUnreadableRecordBecomesANoteNotSilence(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	got := r.Expansions([]record{{Role: envelope.RoleType, File: "gone.go", StartLine: 1, EndLine: 2}})
	if len(got) != 0 {
		t.Fatal("a file that is not there produced an expansion")
	}
	if len(r.Notes()) == 0 {
		t.Error("it produced no note either, so the gap is invisible")
	}
}

func TestClassOf(t *testing.T) {
	for path, want := range map[string]envelope.Class{
		"internal/store/user.go":      envelope.ClassSource,
		"internal/store/user_test.go": envelope.ClassTest,
		"db/migrations/0007.sql":      envelope.ClassMigration,
		"go.sum":                      envelope.ClassLockfile,
		"vendor/x/y.go":               envelope.ClassVendored,
	} {
		if got := classOf(path); got != want {
			t.Errorf("classOf(%q) = %q, want %q", path, got, want)
		}
	}
}
