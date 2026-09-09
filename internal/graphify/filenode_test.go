package graphify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file node records no line on some extractors, and span refused any node
// with no line. That dropped, in silence, the one case file nodes are
// admitted for: a migration whose whole content is the thing worth reading.
func TestACrossKindFileNodeWithNoLineIsStillSent(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		"internal/store/user.go": "package store\n\nfunc Insert() error { return nil }\n",
		"db/0007_email.sql":      "ALTER TABLE users\n\tADD COLUMN email TEXT NOT NULL;\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	graph := filepath.Join(root, "graph.json")
	// The migration is a file node with no source_location, which is what
	// Graphify writes when the grammar produced nothing below the file.
	if err := os.WriteFile(graph, []byte(`{
	  "nodes": [
	    {"id": "insert", "label": "Insert()", "file_type": "code",
	     "source_file": "internal/store/user.go", "source_location": "L3"},
	    {"id": "mig", "label": "0007_email.sql", "file_type": "code",
	     "source_file": "db/0007_email.sql", "source_location": ""}
	  ],
	  "links": [
	    {"source": "insert", "target": "mig", "relation": "references", "confidence": "INFERRED"}
	  ]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Load(graph)
	if err != nil {
		t.Fatal(err)
	}
	env := g.Envelope(Options{Root: root, Changed: []string{"internal/store/user.go"}})
	x := find(env, RoleNeighbor, "0007_email.sql")
	if x == nil {
		t.Fatalf("the migration was dropped; expansions = %+v, notes = %v", env.Expansions, env.Notes)
	}
	if !strings.Contains(x.Content, "ADD COLUMN email TEXT NOT NULL") {
		t.Errorf("content is not the migration:\n%s", x.Content)
	}
	if x.StartLine != 1 {
		t.Errorf("startLine = %d, want the top of the file", x.StartLine)
	}
}

// source_file comes from a file another program wrote, so a path that climbs
// out of the tree must not be read into the envelope as this repository's
// source.
func TestAGraphPathCannotEscapeTheTree(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("not this repository's source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	s := &sourceCache{root: root}
	if _, _, ok := s.read("../" + filepath.Base(outside)); ok {
		t.Error("a path climbing out of the tree was read")
	}
	if _, _, ok := s.read(outside); ok {
		t.Error("an absolute path outside the tree was read")
	}
}
