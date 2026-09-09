package graphify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, body string) *Graph {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// NetworkX names the edge list "links" and Graphify's own validator accepts
// "edges" as well, so a graph written by either path has to load.
func TestLoadAcceptsEitherEdgeKey(t *testing.T) {
	const nodes = `"nodes": [
		{"id": "a", "label": "A", "file_type": "code", "source_file": "a.go", "source_location": "L1"},
		{"id": "b", "label": "B", "file_type": "code", "source_file": "b.go", "source_location": "L1"}]`
	const edge = `{"source": "a", "target": "b", "relation": "calls", "confidence": "EXTRACTED"}`
	for _, key := range []string{"links", "edges"} {
		g := load(t, "{"+nodes+", \""+key+"\": ["+edge+"]}")
		if got := len(g.neighbors("a")); got != 1 {
			t.Errorf("%q: walked %d edges, want 1", key, got)
		}
	}
}

func TestLoadReportsAFileThatIsNotAGraph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("a file that is not a graph loaded without complaint")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("a missing graph loaded without complaint")
	}
}

func TestNodeLine(t *testing.T) {
	for _, tc := range []struct {
		loc  string
		want int
	}{
		{"L42", 42},
		{"L42-L58", 42},
		{"", 0},
		{"line 42", 0},
		{"L", 0},
	} {
		if got := (Node{SourceLocation: tc.loc}).Line(); got != tc.want {
			t.Errorf("Line(%q) = %d, want %d", tc.loc, got, tc.want)
		}
	}
}

// Graphify's semantic pass may write `calls`, `implements` and `references`
// between two code nodes, so the relation and the node types cannot tell the
// two halves apart on their own. Where the graph marks each edge with the
// half that wrote it, that marker decides.
const originGraph = `{
  "built_at_commit": "1111111111111111111111111111111111111111",
  "nodes": [
    {"id": "insert", "label": "Insert()", "file_type": "code",
     "source_file": "internal/store/user.go", "source_location": "L8", "_origin": "ast"},
    {"id": "real_caller", "label": "Handle()", "file_type": "code",
     "source_file": "internal/api/handler.go", "source_location": "L3", "_origin": "ast"},
    {"id": "guessed", "label": "Upsert()", "file_type": "code",
     "source_file": "internal/other/x.go", "source_location": "L2", "_origin": "semantic"}
  ],
  "links": [
    {"source": "real_caller", "target": "insert", "relation": "calls",
     "confidence": "EXTRACTED", "_origin": "ast"},
    {"source": "insert", "target": "guessed", "relation": "references",
     "confidence": "INFERRED", "_origin": "semantic"}
  ]
}`

func TestModelWrittenEdgeIsExcludedByItsOriginMarker(t *testing.T) {
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
	g := load(t, originGraph)
	if !g.TagsOrigin {
		t.Fatal("the graph marks its edges and the loader did not notice")
	}
	env := g.Envelope(Options{
		Root:    root,
		Changed: []string{"internal/store/user.go"},
		HeadSHA: "1111111111111111111111111111111111111111",
	})
	for _, x := range env.Expansions {
		if x.Symbol == "Upsert()" {
			t.Error("a model-written edge between two code nodes reached the envelope; the relation allowlist alone cannot catch that one")
		}
	}
	if find(env, "caller", "Handle()") == nil {
		t.Error("the tree-sitter caller was dropped along with it")
	}
	if !hasNote(env, "a model wrote them") {
		t.Errorf("the exclusion was silent:\n%s", strings.Join(env.Notes, "\n"))
	}
}

// A graph built before Graphify carried that marker gets the weaker check,
// and the difference is stated rather than papered over.
func TestGraphWithoutOriginMarkersSaysTheCheckIsWeaker(t *testing.T) {
	env := expand(t, Options{})
	if !hasNote(env, "does not record which of its two halves") {
		t.Errorf("a graph with no provenance markers was spent as if it had them:\n%s",
			strings.Join(env.Notes, "\n"))
	}
	marked := load(t, originGraph)
	if marked.TagsOrigin != true {
		t.Fatal("fixture does not mark origins")
	}
}
