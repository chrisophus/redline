// Package graphify reads a Graphify graph from disk and turns the part of it
// a change touches into a context envelope.
//
// Graphify (github.com/Graphify-Labs/graphify) parses a repository with
// tree-sitter into graphify-out/graph.json and keeps it current with
// `graphify update`. That file is an index built once and queried, which is
// the property the per-change envelope does not have, and it spans about
// thirty-seven grammars, so it reaches SQL, Terraform and shell scripts that
// no single language toolchain sees.
//
// Nothing here parses a language. It reads JSON that a parser already wrote,
// which is why this package does not cross the boundary internal/boundary
// guards. It is also the only package in this module that knows Graphify's
// schema: everything else sees an envelope, and a boundary test keeps it that
// way so this can move to its own repository without unpicking imports.
package graphify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Node is one thing Graphify found: a function, a type, a table, a file.
//
// FileType is the half of the graph a node came from. Only "code" nodes come
// out of tree-sitter; "document", "paper", "image", "rationale" and "concept"
// are written by Graphify's semantic pass, which is a model call. That
// distinction is load-bearing here and not decoration: see deterministic.
type Node struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	FileType       string `json:"file_type"`
	SourceFile     string `json:"source_file"`
	SourceLocation string `json:"source_location"`
	Community      *int   `json:"community"`
	CommunityName  string `json:"community_name"`
	Origin         string `json:"_origin"`
}

// Line is the 1-based line the node starts at, or 0 when the graph did not
// record one. Graphify writes "L42"; an empty or absent value is common
// enough on file-level nodes that it is not an error.
func (n Node) Line() int {
	s := strings.TrimSpace(n.SourceLocation)
	if !strings.HasPrefix(s, "L") {
		return 0
	}
	// "L42-L58" appears in hand-written graphs even though the extractors
	// only emit a single line. Take the start and ignore the rest.
	if i := strings.IndexAny(s[1:], "-:"); i >= 0 {
		s = s[:i+1]
	}
	line, err := strconv.Atoi(s[1:])
	if err != nil || line < 0 {
		return 0
	}
	return line
}

// isFile reports whether the node stands for a whole file rather than
// something inside one. Graphify labels a file node with its base name, and
// those are the spine of the graph rather than context worth sending: a file
// node's content is the file, and the file is either in the diff already or
// far too large to paste.
func (n Node) isFile() bool {
	if n.SourceFile == "" {
		return false
	}
	return n.Label == filepath.Base(filepath.ToSlash(n.SourceFile))
}

// Edge is one connection, with Graphify's own account of how sure it is.
// Confidence is EXTRACTED (the source says so), INFERRED (a second pass
// deduced it) or AMBIGUOUS. Redline never presents an INFERRED edge as a
// resolved fact, so the tag rides into the envelope's details.
type Edge struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Relation   string `json:"relation"`
	Confidence string `json:"confidence"`
	// Origin is "ast" on everything Graphify's tree-sitter pass wrote. The
	// marker exists so an incremental rebuild can evict AST edges without
	// touching the semantic ones, and it is the only field that separates the
	// two halves edge by edge: the semantic pass is allowed to write `calls`,
	// `implements` and `references` between two code nodes, so neither the
	// relation nor the node types can tell those apart on their own.
	Origin string `json:"_origin"`
}

// Graph is a loaded graph.json, indexed for the two questions the adapter
// asks: which nodes does this file own, and what is one hop from this node.
type Graph struct {
	// BuiltAtCommit is the revision Graphify built the graph from. It is what
	// makes staleness checkable at all: graph.json is a build artifact that
	// drifts with whatever last ran `graphify update`, and an adapter reading
	// a stale graph writes a different envelope for the same commit.
	BuiltAtCommit string

	// FailedSources are files Graphify's own build could not extract. It is
	// the one place the index admits to a hole, so the adapter reads it and
	// says so for any file in the change. Not every hole reaches it: a
	// missing tree-sitter grammar returns an empty extraction with an error
	// key that the merge step drops, so that file looks like a file with
	// nothing in it. Hence the second check, on files the graph holds no
	// nodes for at all.
	FailedSources []string

	// TagsOrigin is whether this graph marks which half wrote each edge. A
	// graph built before Graphify carried the marker has none, and then the
	// relation allowlist is all the separation there is. The adapter says so
	// in a note rather than letting a weaker guard pass for the stronger one.
	TagsOrigin bool

	nodes  map[string]Node
	byFile map[string][]string // normalized source_file -> node ids, by line
	out    map[string][]Edge
	in     map[string][]Edge
}

// rawGraph is the file as written. NetworkX's node_link_data names the edge
// list "links"; older graphs and hand-written ones use "edges", and
// Graphify's own validator accepts both, so this does too.
type rawGraph struct {
	Nodes         []Node   `json:"nodes"`
	Links         []Edge   `json:"links"`
	Edges         []Edge   `json:"edges"`
	BuiltAtCommit string   `json:"built_at_commit"`
	FailedSources []string `json:"failed_sources"`
}

// Load reads graph.json.
//
// A missing graph is an error rather than an empty result. The adapter exits
// non-zero on it, which Redline records as a provider that did not run: an
// empty context and an unexamined one read identically to a model, and this
// is the one place that difference can still be stated.
func Load(path string) (*Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw rawGraph
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s is not a graph: %w", path, err)
	}
	edges := raw.Links
	if len(edges) == 0 {
		edges = raw.Edges
	}
	g := build(raw.Nodes, edges, raw.BuiltAtCommit)
	g.FailedSources = raw.FailedSources
	return g, nil
}

func build(nodes []Node, edges []Edge, commit string) *Graph {
	g := &Graph{
		BuiltAtCommit: commit,
		nodes:         make(map[string]Node, len(nodes)),
		byFile:        map[string][]string{},
		out:           map[string][]Edge{},
		in:            map[string][]Edge{},
	}
	for _, n := range nodes {
		if n.ID == "" {
			continue
		}
		g.nodes[n.ID] = n
		if n.SourceFile != "" {
			f := normPath(n.SourceFile)
			g.byFile[f] = append(g.byFile[f], n.ID)
		}
	}
	for _, f := range g.byFile {
		ids := f
		sort.Slice(ids, func(i, j int) bool {
			a, b := g.nodes[ids[i]], g.nodes[ids[j]]
			if a.Line() != b.Line() {
				return a.Line() < b.Line()
			}
			return ids[i] < ids[j]
		})
	}
	for _, e := range edges {
		if e.Source == "" || e.Target == "" {
			continue
		}
		if e.Origin != "" {
			g.TagsOrigin = true
		}
		g.out[e.Source] = append(g.out[e.Source], e)
		g.in[e.Target] = append(g.in[e.Target], e)
	}
	return g
}

// Node returns the node with this id.
func (g *Graph) Node(id string) (Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// NodesIn returns the ids the graph holds for a repository path, ordered by
// line. The path is matched exactly after normalization, then by trailing
// segments: Graphify anchors source_file to the root it was given, and a
// graph built from graphify-out/ or from an absolute path would otherwise
// look empty for every file in the repository.
func (g *Graph) NodesIn(path string) []string {
	p := normPath(path)
	if ids, ok := g.byFile[p]; ok {
		return ids
	}
	var match []string
	suffix := "/" + p
	for f, ids := range g.byFile {
		if strings.HasSuffix(f, suffix) {
			match = append(match, ids...)
		}
	}
	sort.Strings(match)
	return match
}

// Files returns every distinct source_file in the graph, sorted.
func (g *Graph) Files() []string {
	out := make([]string, 0, len(g.byFile))
	for f := range g.byFile {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// neighbors returns the deterministic edges touching a node, outgoing first
// then incoming, each in a stable order. A caller must not range over the
// graph's maps to find these: the envelope for one revision has to be
// byte-identical on every run, and map order is the cheapest way to lose that.
func (g *Graph) neighbors(id string) []hop {
	var out []hop
	for _, e := range g.out[id] {
		if !g.deterministic(e) {
			continue
		}
		out = append(out, hop{edge: e, peer: e.Target, outgoing: true})
	}
	for _, e := range g.in[id] {
		if !g.deterministic(e) {
			continue
		}
		out = append(out, hop{edge: e, peer: e.Source, outgoing: false})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].peer != out[j].peer {
			return out[i].peer < out[j].peer
		}
		return out[i].edge.Relation < out[j].edge.Relation
	})
	return out
}

// hop is one edge walked from an owned node, with the far end named.
type hop struct {
	edge     Edge
	peer     string
	outgoing bool
}

// treeSitterRelations are the edge kinds Graphify's tree-sitter extractors
// emit. Everything the adapter walks has to be in here.
//
// An allowlist and not a denylist, on purpose. Graphify's other half runs
// subagents or calls a model to write semantic edges, and two fresh rebuilds
// of the identical commit are not guaranteed to produce the same ones. An
// envelope built from those is not reproducible, which makes the eval measure
// noise rather than the reviewer. A denylist would admit every relation a
// future model pass invents; this drops every relation a future grammar
// invents instead, and that direction fails visibly: the expansion is missing,
// and skippedRelations says which relation names were passed over.
var treeSitterRelations = map[string]bool{
	"accesses":            true,
	"binds_method":        true,
	"bound_to":            true,
	"calls":               true,
	"configures":          true,
	"contains":            true,
	"crate_depends_on":    true,
	"defines":             true,
	"depends_on":          true,
	"embeds":              true,
	"exports":             true,
	"extends":             true,
	"implements":          true,
	"imports":             true,
	"imports_from":        true,
	"includes":            true,
	"indirect_call":       true,
	"inherits":            true,
	"instantiates":        true,
	"listened_by":         true,
	"method":              true,
	"mixes_in":            true,
	"navigates":           true,
	"re_exports":          true,
	"reads_from":          true,
	"references":          true,
	"references_constant": true,
	"requires":            true,
	"specializes":         true,
	"triggers":            true,
	"uses":                true,
	"uses_component":      true,
	"uses_static_prop":    true,
}

// semanticRelations are the ones only Graphify's model pass writes. They are
// named here so a skipped edge can be reported as a deliberate exclusion
// rather than as a relation the adapter has not learned yet.
var semanticRelations = map[string]bool{
	"cites":                   true,
	"conceptually_related_to": true,
	"rationale_for":           true,
	"semantically_similar_to": true,
	"shares_data_with":        true,
}

// deterministic reports whether an edge came from the tree-sitter half.
//
// Three tests, and each catches something the others do not. The `_origin`
// marker is the direct answer where the graph carries it. The relation
// allowlist rejects semantically_similar_to and its siblings by name, which
// is all a graph built before that marker existed has. The file_type check
// rejects an edge into a document, image or rationale node, which only the
// semantic pass writes. An edge that passes all three replays byte-identically
// from the same commit, which is what an eval needs to be measuring the
// reviewer rather than the index.
func (g *Graph) deterministic(e Edge) bool {
	if g.TagsOrigin && e.Origin != "ast" {
		return false
	}
	if !treeSitterRelations[e.Relation] {
		return false
	}
	src, ok := g.nodes[e.Source]
	if !ok || src.FileType != "code" {
		return false
	}
	tgt, ok := g.nodes[e.Target]
	if !ok || tgt.FileType != "code" {
		return false
	}
	return true
}

// skipped splits the edges touching these nodes that deterministic rejected
// into the two reasons it could have rejected them: the graph's model-written
// half, which is excluded on purpose, and a relation this adapter does not
// know, which is a gap. Both are said out loud, because a subgraph that
// missed the one relevant edge looks exactly like one that found nothing to
// say, and a grammar Graphify learned after that allowlist was written is the
// likeliest way it happens.
func (g *Graph) skipped(ids []string) (semantic, unknown []string) {
	sem, unk := map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		for _, e := range append(append([]Edge{}, g.out[id]...), g.in[id]...) {
			if e.Relation == "" || g.deterministic(e) {
				continue
			}
			switch {
			case semanticRelations[e.Relation], g.TagsOrigin && e.Origin != "ast":
				sem[e.Relation] = true
			case !treeSitterRelations[e.Relation]:
				unk[e.Relation] = true
			}
		}
	}
	return sortedKeys(sem), sortedKeys(unk)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normPath puts a path in the form the changed list uses: slash-separated,
// no leading "./".
func normPath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "./")
	return p
}
