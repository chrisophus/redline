package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSamePathRequiresSegmentBoundary(t *testing.T) {
	if !samePath("cmd/redline/main.go", "cmd/redline/main.go") {
		t.Fatal("exact match")
	}
	if !samePath("sandbox/redline/cmd/redline/main.go", "cmd/redline/main.go") {
		t.Fatal("suffix with slash boundary should match")
	}
	if samePath("cmd/other/main.go", "cmd/redline/main.go") {
		t.Fatal("different paths sharing a basename must not match")
	}
	if samePath("go", "cmd/redline/main.go") {
		t.Fatal(`node file "go" must not match every *.go path`)
	}
}

func TestNodesForIgnoresBasenameCollisions(t *testing.T) {
	g := &Graph{Nodes: []Node{
		{ID: "a", File: "cmd/redline/main.go"},
		{ID: "b", File: "cmd/other/main.go"},
		{ID: "c", File: "go"},
	}}
	got := g.nodesFor([]string{"cmd/redline/main.go"})
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("got %v, want [a]", got)
	}
}

func TestLocatePlanningGraphs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".planning", "graphs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(path, []byte(`{"nodes":[],"edges":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Locate(root); got != path {
		t.Fatalf("Locate = %q, want %q", got, path)
	}
}
