package graphify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/envelope"
)

// The fixture is one change with the shape the graph is here for: a Go
// method that a handler calls, an interface a second type also implements,
// and a SQL migration the type checker will never see.
const fixtureGraph = `{
  "built_at_commit": "1111111111111111111111111111111111111111",
  "nodes": [
    {"id": "user_go", "label": "user.go", "file_type": "code",
     "source_file": "internal/store/user.go", "source_location": "L1", "community": 1},
    {"id": "store_insert", "label": "Insert()", "file_type": "code",
     "source_file": "internal/store/user.go", "source_location": "L8", "community": 1},
    {"id": "handler_handle", "label": "Handle()", "file_type": "code",
     "source_file": "internal/api/handler.go", "source_location": "L3", "community": 2},
    {"id": "store_iface", "label": "Store", "file_type": "code",
     "source_file": "internal/store/types.go", "source_location": "L3", "community": 1},
    {"id": "memory_insert", "label": "Insert()", "file_type": "code",
     "source_file": "internal/store/memory.go", "source_location": "L5", "community": 1},
    {"id": "users_table", "label": "users", "file_type": "code",
     "source_file": "db/migrations/0007_add_email.sql", "source_location": "L3", "community": 1},
    {"id": "notes_doc", "label": "notes.md", "file_type": "document",
     "source_file": "docs/notes.md", "source_location": "L1"},
    {"id": "similar_code", "label": "Upsert()", "file_type": "code",
     "source_file": "internal/other/x.go", "source_location": "L2", "community": 7},
    {"id": "client", "label": "Client", "file_type": "code", "source_location": ""}
  ],
  "links": [
    {"source": "user_go", "target": "store_insert", "relation": "contains", "confidence": "EXTRACTED"},
    {"source": "handler_handle", "target": "store_insert", "relation": "calls", "confidence": "EXTRACTED"},
    {"source": "store_insert", "target": "store_iface", "relation": "implements", "confidence": "EXTRACTED"},
    {"source": "memory_insert", "target": "store_iface", "relation": "implements", "confidence": "EXTRACTED"},
    {"source": "store_insert", "target": "users_table", "relation": "references", "confidence": "INFERRED"},
    {"source": "store_insert", "target": "notes_doc", "relation": "rationale_for", "confidence": "INFERRED"},
    {"source": "store_insert", "target": "similar_code", "relation": "semantically_similar_to", "confidence": "INFERRED"},
    {"source": "store_insert", "target": "client", "relation": "calls", "confidence": "INFERRED"},
    {"source": "store_insert", "target": "handler_handle", "relation": "shares_data_with", "confidence": "INFERRED"}
  ]
}`

var fixtureFiles = map[string]string{
	"internal/store/user.go":           "package store\n\nimport \"context\"\n\ntype Store struct{}\n\n// Insert writes a row.\nfunc (s *Store) Insert(ctx context.Context, email string) error {\n\treturn nil\n}\n",
	"internal/api/handler.go":          "package api\n\nfunc Handle() error {\n\treturn nil\n}\n",
	"internal/store/types.go":          "package store\n\ntype Inserter interface {\n\tInsert() error\n}\n",
	"internal/store/memory.go":         "package store\n\ntype Memory struct{}\n\nfunc (m *Memory) Insert() error {\n\treturn nil\n}\n",
	"db/migrations/0007_add_email.sql": "-- add the column\n\nALTER TABLE users\n\tADD COLUMN email TEXT NOT NULL;\n",
	"internal/other/x.go":              "package other\n\nfunc Upsert() error { return nil }\n",
	"docs/notes.md":                    "# notes\n",
}

func fixture(t *testing.T) (*Graph, string) {
	t.Helper()
	root := t.TempDir()
	for path, body := range fixtureFiles {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, []byte(fixtureGraph), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Load(graphPath)
	if err != nil {
		t.Fatal(err)
	}
	return g, root
}

func expand(t *testing.T, opts Options) *envelope.Envelope {
	t.Helper()
	g, root := fixture(t)
	opts.Root = root
	if opts.Changed == nil {
		opts.Changed = []string{"internal/store/user.go"}
	}
	if opts.HeadSHA == "" {
		opts.HeadSHA = "1111111111111111111111111111111111111111"
	}
	return g.Envelope(opts)
}

func find(env *envelope.Envelope, role envelope.Role, symbol string) *envelope.Expansion {
	for i, x := range env.Expansions {
		if x.Role == role && x.Symbol == symbol {
			return &env.Expansions[i]
		}
	}
	return nil
}

func TestEnvelopeIsValidAndNamesTheGraphRevision(t *testing.T) {
	env := expand(t, Options{BaseSHA: "abc"})
	if err := env.Validate(); err != nil {
		t.Fatalf("envelope does not satisfy the contract: %v", err)
	}
	if env.Provider.Name != "graphify" {
		t.Errorf("provider name = %q", env.Provider.Name)
	}
	// The graph's build revision is the input a replayed review has to be
	// able to name, so it is what provider.version carries.
	if want := "graph@111111111111"; env.Provider.Version != want {
		t.Errorf("provider version = %q, want %q", env.Provider.Version, want)
	}
	if env.BaseSHA != "abc" {
		t.Errorf("baseSHA = %q", env.BaseSHA)
	}
}

// The semantic half of the graph is written by a model, so two fresh rebuilds
// of the same commit need not agree on it. An envelope built from those edges
// is not reproducible, and an eval that cannot reproduce its input measures
// noise. Nothing from that half may appear here.
func TestModelWrittenEdgesNeverReachTheEnvelope(t *testing.T) {
	env := expand(t, Options{})
	for _, x := range env.Expansions {
		switch x.Symbol {
		case "notes.md":
			t.Error("a document node reached the envelope; only the tree-sitter half may")
		case "Upsert()":
			t.Error("a semantically_similar_to edge reached the envelope, and it is not reproducible")
		}
		if rel := x.Details["relation"]; rel == "semantically_similar_to" || rel == "rationale_for" {
			t.Errorf("expansion %q carries model-written relation %q", x.Symbol, rel)
		}
	}
}

func TestSameRevisionProducesTheSameBytes(t *testing.T) {
	// Two loads of the same graph, expanded twice: the walk goes through
	// several maps, and a single unordered range in it would show up here.
	var out [2][]byte
	for i := range out {
		env := expand(t, Options{BaseSHA: "abc"})
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	if string(out[0]) != string(out[1]) {
		t.Error("the same revision produced two different envelopes")
	}
}

func TestCrossKindNeighborCarriesTheMigration(t *testing.T) {
	env := expand(t, Options{})
	x := find(env, RoleNeighbor, "users")
	if x == nil {
		t.Fatal("the migration next to the changed method was not sent; that correlation is the reason the graph is here")
	}
	if x.Details["kind"] != "cross" {
		t.Errorf("details[kind] = %q, want cross", x.Details["kind"])
	}
	if x.Details["adjacent"] != "sql" {
		t.Errorf("details[adjacent] = %q, want sql", x.Details["adjacent"])
	}
	if x.Details["confidence"] != "INFERRED" {
		t.Errorf("details[confidence] = %q; an inferred edge must not read as a resolved fact", x.Details["confidence"])
	}
	if !strings.Contains(x.Content, "ADD COLUMN email TEXT NOT NULL") {
		t.Errorf("content does not carry the migration:\n%s", x.Content)
	}
}

// The neighbor role is not in Redline's vocabulary yet, which is deliberate:
// the contract already ranks an unknown role last and reports it, and that is
// the cheapest way to find out whether the role earns a place.
func TestNeighborShipsAsARoleRedlineReports(t *testing.T) {
	env := expand(t, Options{})
	unknown := env.UnknownRoles()
	if len(unknown) != 1 || unknown[0] != "neighbor" {
		t.Fatalf("unknown roles = %v, want [neighbor]", unknown)
	}
	if _, known := RoleNeighbor.Rank(); known {
		t.Error("neighbor is in the vocabulary; the measurement that would justify it has not run")
	}
}

func TestCallerIsNameResolvedAndSaysSo(t *testing.T) {
	env := expand(t, Options{})
	x := find(env, envelope.RoleCaller, "Handle()")
	if x == nil {
		t.Fatal("the call site was not sent")
	}
	if x.Details["resolution"] != "name" {
		t.Errorf("details[resolution] = %q; a tree-sitter caller matches by name and the reviewer has no other way to know",
			x.Details["resolution"])
	}
}

func TestCallersAreLeftToTheExactProvider(t *testing.T) {
	env := expand(t, Options{DeferCallers: []string{"**/*.go"}})
	if x := find(env, envelope.RoleCaller, "Handle()"); x != nil {
		t.Error("a name-resolved caller was sent for a file an exact resolver covers")
	}
	// Everything else still comes through: deferring callers is not deferring
	// the provider.
	if find(env, RoleNeighbor, "users") == nil {
		t.Error("deferring callers dropped the cross-kind neighbor too")
	}
}

func TestInterfaceGivesTypeAndSibling(t *testing.T) {
	env := expand(t, Options{})
	if x := find(env, envelope.RoleType, "Store"); x == nil {
		t.Error("the interface the changed method implements was not sent as a type")
	}
	x := find(env, envelope.RoleSibling, "Insert()")
	if x == nil {
		t.Fatal("the other implementation of the interface was not sent as a sibling")
	}
	if x.Details["basis"] != "Store" {
		t.Errorf("details[basis] = %q, want the interface both implement", x.Details["basis"])
	}
	if x.File != "internal/store/memory.go" {
		t.Errorf("sibling file = %q", x.File)
	}
}

func TestChangedFilesAreNotResentAsContext(t *testing.T) {
	env := expand(t, Options{Changed: []string{"internal/store/user.go", "internal/api/handler.go"}})
	for _, x := range env.Expansions {
		if x.File == "internal/api/handler.go" {
			t.Error("a changed file came back as an expansion; the diff already carries it")
		}
	}
}

// A hop into a node with no source file is what an incremental `graphify
// update` leaves behind when the real definition sits outside the batch it
// re-extracted. Following it lands nowhere, and silence there reads as
// "nothing to say" rather than "not resolved".
func TestBareNodeIsReportedRatherThanFollowed(t *testing.T) {
	env := expand(t, Options{})
	for _, x := range env.Expansions {
		if x.Symbol == "Client" {
			t.Error("a node with no source file was sent as context")
		}
	}
	if !hasNote(env, "no source file") {
		t.Errorf("the unresolved hop was not reported:\n%s", strings.Join(env.Notes, "\n"))
	}
}

// A file whose grammar Graphify cannot load extracts to nothing, and the
// build says so nowhere: a missing grammar and a file with nothing structural
// in it look identical in the graph. This is the layer where they can still
// be told apart, because the adapter knows it claimed the file.
func TestFileTheGraphHoldsNothingForIsReported(t *testing.T) {
	env := expand(t, Options{Changed: []string{"internal/store/user.go", "config/app.toml"}})
	if !hasNote(env, "config/app.toml") {
		t.Errorf("a claimed file with no nodes was not reported:\n%s", strings.Join(env.Notes, "\n"))
	}
}

func TestRelationsTheAdapterDoesNotKnowAreNamed(t *testing.T) {
	env := expand(t, Options{})
	if !hasNote(env, "shares_data_with") {
		t.Errorf("a skipped relation was not named:\n%s", strings.Join(env.Notes, "\n"))
	}
}

func TestStaleGraphIsSaidOutLoud(t *testing.T) {
	env := expand(t, Options{HeadSHA: "2222222222222222222222222222222222222222"})
	if !hasNote(env, "graphify update") {
		t.Errorf("a graph built from another revision was spent silently:\n%s", strings.Join(env.Notes, "\n"))
	}
	fresh := expand(t, Options{})
	if hasNote(fresh, "graphify update") {
		t.Error("a current graph was reported as stale")
	}
}

// `graphify update` writes a graph with no build revision in it, which is
// the common case rather than the exotic one, so the fallback check has to
// work: a graph older than the files under review cannot be describing them.
func TestGraphWithNoRevisionFallsBackToTimestamps(t *testing.T) {
	g, root := fixture(t)
	g.BuiltAtCommit = ""
	// Touch a changed file so it is newer than the graph.
	changed := filepath.Join(root, "internal", "store", "user.go")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(changed, future, future); err != nil {
		t.Fatal(err)
	}
	env := g.Envelope(Options{
		Root:      root,
		GraphPath: "graph.json",
		Changed:   []string{"internal/store/user.go"},
	})
	if !hasNote(env, "older than files this change touches") {
		t.Errorf("a graph older than the change was spent silently:\n%s", strings.Join(env.Notes, "\n"))
	}
}

func TestGraphTheBuildCouldNotExtractIsReported(t *testing.T) {
	g, root := fixture(t)
	g.FailedSources = []string{"internal/store/user.go"}
	env := g.Envelope(Options{Root: root, Changed: []string{"internal/store/user.go"}})
	if !hasNote(env, "failing to extract") {
		t.Errorf("the graph's own record of a failed file was not passed on:\n%s",
			strings.Join(env.Notes, "\n"))
	}
}

func TestSpanEndsAtTheNextNode(t *testing.T) {
	env := expand(t, Options{})
	x := find(env, envelope.RoleSibling, "Insert()")
	if x == nil {
		t.Fatal("no sibling expansion")
	}
	// memory.go has one node at L5 and seven lines, so the span runs to the
	// end of the file.
	if x.StartLine != 5 || x.EndLine != 7 {
		t.Errorf("span = %d-%d, want 5-7", x.StartLine, x.EndLine)
	}
	if !strings.HasPrefix(x.Content, "func (m *Memory) Insert()") {
		t.Errorf("content starts in the wrong place:\n%s", x.Content)
	}
}

func TestMaxLinesBoundsOneExpansion(t *testing.T) {
	env := expand(t, Options{MaxLines: 1})
	x := find(env, envelope.RoleSibling, "Insert()")
	if x == nil {
		t.Fatal("no sibling expansion")
	}
	if x.StartLine != x.EndLine {
		t.Errorf("span = %d-%d, want one line", x.StartLine, x.EndLine)
	}
	if x.Details["span"] != "truncated" {
		t.Error("a bounded span did not say it was bounded")
	}
}

func TestManifestClassifiesEveryChangedFile(t *testing.T) {
	env := expand(t, Options{Changed: []string{
		"internal/store/user.go",
		"db/migrations/0007_add_email.sql",
		"go.sum",
		"internal/store/user_test.go",
		"vendor/x/y.go",
		"README.md",
	}})
	want := map[string]envelope.Class{
		"internal/store/user.go":           envelope.ClassSource,
		"db/migrations/0007_add_email.sql": envelope.ClassMigration,
		"go.sum":                           envelope.ClassLockfile,
		"internal/store/user_test.go":      envelope.ClassTest,
		"vendor/x/y.go":                    envelope.ClassVendored,
		"README.md":                        envelope.ClassOther,
	}
	if len(env.Files) != len(want) {
		t.Fatalf("manifest has %d files, want %d", len(env.Files), len(want))
	}
	for _, f := range env.Files {
		if want[f.Path] != f.Class {
			t.Errorf("%s classified %q, want %q", f.Path, f.Class, want[f.Path])
		}
	}
	for _, f := range env.Files {
		if f.Path == "internal/store/user.go" && len(f.Symbols) == 0 {
			t.Error("the manifest names no symbols for a file the graph has nodes for")
		}
	}
}

func TestNoTestExpansions(t *testing.T) {
	// Redline holds test expansions back per review, so adapter work there is
	// wasted. Nothing here should be inventing them either.
	env := expand(t, Options{})
	for _, x := range env.Expansions {
		if x.Role == envelope.RoleTest {
			t.Error("the adapter emitted a test expansion")
		}
	}
}

func TestPromptFragmentSaysWhatKindOfEvidenceThisIs(t *testing.T) {
	env := expand(t, Options{})
	for _, want := range []string{"name", "EXTRACTED", "neighbor"} {
		if !strings.Contains(env.PromptFragment, want) {
			t.Errorf("prompt fragment does not mention %q", want)
		}
	}
}

func hasNote(env *envelope.Envelope, substr string) bool {
	for _, n := range env.Notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}
