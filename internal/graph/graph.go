// Package graph reads a graphify knowledge graph and derives narrative threads
// through the code the change touches.
//
// The graph answers the question a diff cannot: what does this code connect
// to. A reviewer looking at a changed function wants to know what reaches it
// and what it reaches, and following that by hand is most of the work of
// reviewing.
package graph

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ccason/redline/internal/packet"
)

// Graph is the subset of graphify's graph.json Redline reads.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Node is one entity in the graph.
type Node struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Type  string `json:"type,omitempty"`
	File  string `json:"file,omitempty"`
	Path  string `json:"path,omitempty"`
}

// Edge is one relationship.
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label,omitempty"`
	Type   string `json:"type,omitempty"`
}

// Locate finds a graphify graph for a directory. Redline reads an existing
// graph; it does not build one, because extraction is an expensive LLM pass
// the user should choose to run.
func Locate(dir string) string {
	for _, candidate := range []string{
		filepath.Join(dir, "graphify-out", "graph.json"),
		filepath.Join(dir, "graph.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// Load reads a graph from disk.
func Load(path string) (*Graph, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g Graph
	if err := json.Unmarshal(buf, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// maxThreads caps how many threads reach the packet. The graph of a large
// repository will happily produce hundreds; a review can use a handful.
const maxThreads = 12

// Threads returns paths through the graph anchored on the changed files:
// for each node the change touches, its immediate neighbourhood, rendered as
// a thread the reviewer can follow.
func (g *Graph) Threads(changed []string) []packet.Thread {
	touched := g.nodesFor(changed)
	if len(touched) == 0 {
		return nil
	}
	adjacency := map[string][]Edge{}
	for _, e := range g.Edges {
		adjacency[e.Source] = append(adjacency[e.Source], e)
	}
	label := map[string]string{}
	for _, n := range g.Nodes {
		label[n.ID] = firstNonEmpty(n.Label, n.ID)
	}

	var threads []packet.Thread
	for _, id := range touched {
		for _, e := range adjacency[id] {
			if len(threads) >= maxThreads {
				sort.Slice(threads, func(i, j int) bool { return threads[i].From < threads[j].From })
				return threads
			}
			threads = append(threads, packet.Thread{
				From:        label[id],
				To:          firstNonEmpty(label[e.Target], e.Target),
				Nodes:       []string{label[id], firstNonEmpty(label[e.Target], e.Target)},
				Explanation: e.Label,
			})
		}
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].From < threads[j].From })
	return threads
}

// nodesFor returns the IDs of nodes whose file matches a changed path.
func (g *Graph) nodesFor(changed []string) []string {
	want := map[string]bool{}
	for _, c := range changed {
		want[c] = true
		want[filepath.Base(c)] = true
	}
	var ids []string
	for _, n := range g.Nodes {
		file := firstNonEmpty(n.File, n.Path)
		if file == "" {
			continue
		}
		if want[file] || want[filepath.Base(file)] || matchesAny(file, changed) {
			ids = append(ids, n.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func matchesAny(file string, changed []string) bool {
	for _, c := range changed {
		if strings.HasSuffix(c, file) || strings.HasSuffix(file, c) {
			return true
		}
	}
	return false
}

// Explain shells out to graphify for a plain-language account of a node. Best
// effort: an absent graphify is not an error, it is one fewer thread.
func Explain(graphPath, node string) string {
	if _, err := exec.LookPath("graphify"); err != nil {
		return ""
	}
	out, err := exec.Command("graphify", "explain", node, "--graph", graphPath).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
