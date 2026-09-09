package graphify

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/globmatch"
)

// RoleNeighbor is a node the graph puts next to the change that no role in
// Redline's vocabulary describes: a migration beside the struct that writes
// the row, a Terraform block beside the handler it configures. Correlating
// those is what the second wave exists for, and `enclosing`, `caller`,
// `type`, `sibling`, `test` and `history` all describe something else.
//
// It ships as a role Redline does not know on purpose. The contract already
// handles that case: an unknown role ranks after every known one, is kept
// when it fits, and is named in the report when it does not. If the
// correlation findings show up in the measurements, that is the evidence for
// promoting it into the vocabulary. Adding it there first would be deciding
// the question by writing it down.
const RoleNeighbor = envelope.Role("neighbor")

// Options are the knobs the adapter passes through from its flags.
type Options struct {
	// Root is the tree under review. Expansion content is read from it.
	Root string
	// Changed is the changed paths, repository-relative, as Redline computed
	// them.
	Changed []string
	// BaseSHA is the merge base, carried into the envelope unchanged.
	BaseSHA string
	// HeadSHA is the revision under review, compared against the graph's own
	// build revision to catch a stale graph.
	HeadSHA string
	// GraphPath is where the graph was read from, relative to Root. It is the
	// fallback staleness check: `graphify update` writes graphs with no build
	// revision in them, and then the only evidence left is whether the change
	// is newer than the file, which is the same check a coverage profile gets.
	GraphPath string
	// DeferCallers are globs whose caller edges belong to another provider.
	// A tree-sitter edge matches by name, so two methods called Insert on
	// different types are one edge and "here is who calls what you changed"
	// becomes "here is who calls something spelled like it". Where an exact
	// resolver already covers a language, its callers win: set this to that
	// provider's scope.
	DeferCallers []string
	// MaxLines bounds one expansion's content. A node carries a start line
	// and no end, so the span runs to the next node in the file; a file node
	// has no next node and would otherwise be the whole file.
	MaxLines int
	// MaxNoteExamples bounds how many paths or labels a note names before it
	// falls back to a count.
	MaxNoteExamples int
}

const (
	defaultMaxLines        = 60
	defaultMaxNoteExamples = 5
)

func (o Options) maxLines() int {
	if o.MaxLines <= 0 {
		return defaultMaxLines
	}
	return o.MaxLines
}

func (o Options) maxNoteExamples() int {
	if o.MaxNoteExamples <= 0 {
		return defaultMaxNoteExamples
	}
	return o.MaxNoteExamples
}

// typeRelations name a type the changed code is built out of. Restricted to
// the relations that make that claim: `references` and `uses` are common
// enough to be about anything, and a role is a claim about what a piece of
// context is.
var typeRelations = map[string]bool{
	"embeds":       true,
	"extends":      true,
	"implements":   true,
	"inherits":     true,
	"instantiates": true,
	"mixes_in":     true,
	"specializes":  true,
}

// siblingRelations are the ones where two nodes pointing at the same target
// are the other half of "you changed one of these".
var siblingRelations = map[string]bool{
	"embeds":     true,
	"extends":    true,
	"implements": true,
	"inherits":   true,
}

// Envelope resolves the change against the graph and writes what it found.
//
// The envelope for one revision must be byte-identical on every run or the
// eval measures noise, so everything here is ordered explicitly and nothing
// is emitted from a map walk.
func (g *Graph) Envelope(opts Options) *envelope.Envelope {
	w := &walk{g: g, opts: opts, cand: map[string]*candidate{}, files: map[string][]string{}}
	w.run()
	return w.envelope()
}

// candidate is a peer node the walk decided to send, before content is read.
// A node reached twice keeps the stronger claim: the role that ranks higher,
// and within one role the higher priority.
type candidate struct {
	node     Node
	role     envelope.Role
	priority int
	details  map[string]string
}

func (c *candidate) better(role envelope.Role, priority int) bool {
	mine, _ := c.role.Rank()
	theirs, _ := role.Rank()
	if theirs != mine {
		return theirs < mine
	}
	return priority > c.priority
}

type walk struct {
	g    *Graph
	opts Options

	cand  map[string]*candidate
	files map[string][]string // changed path -> symbols the graph knows

	owned      []string
	unresolved []string // changed paths the graph holds nothing for
	bare       []string // labels of hops with no source_file
	missing    []string // graph paths absent from the tree under review
	dangling   int
}

func (w *walk) run() {
	changed := make([]string, len(w.opts.Changed))
	for i, p := range w.opts.Changed {
		changed[i] = normPath(p)
	}
	sort.Strings(changed)
	changedSet := map[string]bool{}
	for _, p := range changed {
		changedSet[p] = true
	}

	for _, path := range changed {
		ids := w.g.NodesIn(path)
		if len(ids) == 0 {
			w.unresolved = append(w.unresolved, path)
			continue
		}
		w.owned = append(w.owned, ids...)
		for _, id := range ids {
			n := w.g.nodes[id]
			if !n.isFile() {
				w.files[path] = append(w.files[path], n.Label)
			}
		}
	}
	for _, id := range w.owned {
		w.hopsFrom(id, changedSet)
	}
}

// hopsFrom walks one hop out of an owned node and files each peer under the
// role its edge can honestly claim.
func (w *walk) hopsFrom(id string, changed map[string]bool) {
	owner := w.g.nodes[id]
	for _, h := range w.g.neighbors(id) {
		peer, ok := w.g.Node(h.peer)
		if !ok {
			// Graphify prunes dangling edges on write, so this is a graph
			// that was edited or truncated after the fact. Counted rather
			// than named: the id alone tells a reader nothing.
			w.dangling++
			continue
		}
		if peer.SourceFile == "" {
			// A `graphify update` over a batch of changed files invents an
			// unqualified node when the real target is defined outside that
			// batch, rather than resolving to the node that already exists
			// for it. On a large repository that is dozens of bare nodes
			// sharing one label. Landing on one is a dead end that looks like
			// "nothing here" rather than "this symbol lives outside the batch
			// the update touched", so say which ones.
			w.bare = appendUnique(w.bare, peer.Label)
			continue
		}
		if changed[normPath(peer.SourceFile)] {
			continue // already in the diff
		}
		role, priority, details := w.classify(owner, peer, h)
		if role == "" {
			continue
		}
		details["relation"] = h.edge.Relation
		details["confidence"] = confidenceOf(h.edge)
		details["via"] = owner.Label
		w.offer(peer, role, priority, details)
	}
	w.siblingsOf(id, changed)
}

// siblingsOf finds the other implementations of something the changed node
// implements. It is the one two-hop walk here, and it is what the `sibling`
// role means: out to the interface, back to everyone else who implements it.
func (w *walk) siblingsOf(id string, changed map[string]bool) {
	owner := w.g.nodes[id]
	for _, h := range w.g.neighbors(id) {
		if !h.outgoing || !siblingRelations[h.edge.Relation] {
			continue
		}
		for _, back := range w.g.neighbors(h.peer) {
			if back.outgoing || back.edge.Relation != h.edge.Relation {
				continue
			}
			peer, ok := w.g.Node(back.peer)
			if !ok || peer.ID == id || peer.SourceFile == "" {
				continue
			}
			if changed[normPath(peer.SourceFile)] || peer.isFile() {
				continue
			}
			target, _ := w.g.Node(h.peer)
			w.offer(peer, envelope.RoleSibling, priorityFor(back.edge)+20, map[string]string{
				"relation":   back.edge.Relation,
				"confidence": confidenceOf(back.edge),
				"basis":      target.Label,
				"via":        owner.Label,
			})
		}
	}
}

// classify is the honest mapping: what claim can this edge back.
func (w *walk) classify(owner, peer Node, h hop) (envelope.Role, int, map[string]string) {
	priority := priorityFor(h.edge)
	cross := fileKind(owner.SourceFile) != fileKind(peer.SourceFile)
	switch {
	case cross:
		// The cross-kind case, which is the reason the graph is here at all.
		// A file node is allowed through here and nowhere else: a migration
		// often has no node below the file, and skipping it would drop the
		// flagship case to avoid pasting a large file, which maxLines already
		// bounds.
		details := map[string]string{"kind": "cross", "adjacent": fileKind(peer.SourceFile)}
		if sameCommunity(owner, peer) {
			priority += 10
		}
		return RoleNeighbor, priority + 30, details
	case peer.isFile():
		// Same kind and a whole file: the diff is already the better view of
		// it.
		return "", 0, nil
	case h.edge.Relation == "calls" || h.edge.Relation == "indirect_call":
		if h.outgoing {
			// The callee, not the caller. No role in the vocabulary claims
			// it, and inventing a second unknown role here would blur the
			// measurement `neighbor` exists to produce.
			return "", 0, nil
		}
		if globmatch.MatchesAny(w.opts.DeferCallers, normPath(owner.SourceFile)) {
			// An exact resolver covers this file. Its callers are resolved
			// through type identity; these are resolved by name, and two
			// answers to the same question where one is weaker is worse than
			// one answer.
			return "", 0, nil
		}
		return envelope.RoleCaller, priority, map[string]string{"resolution": "name"}
	case h.outgoing && typeRelations[h.edge.Relation] && h.edge.Confidence == "EXTRACTED":
		return envelope.RoleType, priority, map[string]string{}
	case !h.outgoing && h.edge.Relation == "method" && h.edge.Confidence == "EXTRACTED":
		// The type that owns the changed method.
		return envelope.RoleType, priority, map[string]string{}
	default:
		details := map[string]string{"kind": "same"}
		if sameCommunity(owner, peer) {
			priority += 10
			details["community"] = communityName(peer)
		}
		return RoleNeighbor, priority, details
	}
}

func (w *walk) offer(peer Node, role envelope.Role, priority int, details map[string]string) {
	if c, ok := w.cand[peer.ID]; ok {
		if !c.better(role, priority) {
			return
		}
	}
	w.cand[peer.ID] = &candidate{node: peer, role: role, priority: priority, details: details}
}

func (w *walk) envelope() *envelope.Envelope {
	// Ordered, not folded into the literal below: reading the expansions is
	// what discovers a file the graph names and the worktree does not have,
	// and the notes have to be written after that discovery rather than
	// depending on the order Go evaluates struct fields in.
	manifest := w.manifest()
	expansions := w.expansions()
	notes := w.notes()
	return &envelope.Envelope{
		SchemaVersion: envelope.SchemaVersion,
		Provider: envelope.Provider{
			Name: "graphify",
			// The graph's own build revision. provider.version exists so a
			// frozen fixture can say what wrote it, and for an index that is
			// the commit it was built from: two graphs of the same repository
			// at different revisions are different providers as far as a
			// replayed review is concerned.
			Version: versionOf(w.g.BuiltAtCommit),
		},
		BaseSHA:        w.opts.BaseSHA,
		Files:          manifest,
		Expansions:     expansions,
		PromptFragment: promptFragment,
		Notes:          notes,
	}
}

func (w *walk) manifest() []envelope.File {
	paths := make([]string, 0, len(w.opts.Changed))
	for _, p := range w.opts.Changed {
		paths = append(paths, normPath(p))
	}
	sort.Strings(paths)
	out := make([]envelope.File, 0, len(paths))
	for _, p := range paths {
		symbols := append([]string{}, w.files[p]...)
		sort.Strings(symbols)
		out = append(out, envelope.File{Path: p, Class: classOf(p), Symbols: dedupe(symbols)})
	}
	return out
}

func (w *walk) expansions() []envelope.Expansion {
	ids := make([]string, 0, len(w.cand))
	for id := range w.cand {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	src := &sourceCache{root: w.opts.Root}
	out := make([]envelope.Expansion, 0, len(ids))
	for _, id := range ids {
		c := w.cand[id]
		rel, lines, ok := src.read(c.node.SourceFile)
		if !ok {
			w.missing = appendUnique(w.missing, normPath(c.node.SourceFile))
			continue
		}
		start, end := w.span(c.node, len(lines))
		if start == 0 {
			continue
		}
		details := c.details
		if end-start+1 >= w.opts.maxLines() {
			details["span"] = "truncated"
		}
		out = append(out, envelope.Expansion{
			Role:      c.role,
			Priority:  c.priority,
			Symbol:    c.node.Label,
			Scope:     c.node.ID,
			File:      rel,
			StartLine: start,
			EndLine:   end,
			Content:   strings.Join(lines[start-1:end], "\n") + "\n",
			Details:   details,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ra, _ := a.Role.Rank()
		rb, _ := b.Role.Rank()
		switch {
		case ra != rb:
			return ra < rb
		case a.Priority != b.Priority:
			return a.Priority > b.Priority
		case a.File != b.File:
			return a.File < b.File
		case a.StartLine != b.StartLine:
			return a.StartLine < b.StartLine
		default:
			return a.Symbol < b.Symbol
		}
	})
	return out
}

// span is where a node's text starts and ends. Graphify records a start line
// and no end, so the end is the line before the next node in the same file,
// bounded by MaxLines and by the file itself.
func (w *walk) span(n Node, fileLines int) (start, end int) {
	start = n.Line()
	if start <= 0 || fileLines == 0 {
		return 0, 0
	}
	if start > fileLines {
		// The graph is describing a file that has since been edited. Sending
		// the wrong lines under a symbol's name is worse than sending
		// nothing; the staleness note covers the why.
		return 0, 0
	}
	end = fileLines
	for _, id := range w.g.NodesIn(n.SourceFile) {
		other := w.g.nodes[id]
		if line := other.Line(); line > start && line-1 < end {
			end = line - 1
		}
	}
	if end < start {
		end = start
	}
	if end-start+1 > w.opts.maxLines() {
		end = start + w.opts.maxLines() - 1
	}
	if end > fileLines {
		end = fileLines
	}
	return start, end
}

// notes are what the adapter could not resolve. They reach the report as
// unknowns, which is the release valve for the failure mode a graph has: it
// answers something for almost any question, and a subgraph that missed the
// one relevant edge looks identical to one that found nothing to say.
func (w *walk) notes() []string {
	var notes []string
	switch {
	case w.g.BuiltAtCommit != "" && w.opts.HeadSHA != "" && w.g.BuiltAtCommit != w.opts.HeadSHA:
		notes = append(notes, fmt.Sprintf(
			"the graph was built from %s and the tree under review is %s; expansions below may describe code as it was, not as it is (run `graphify update`)",
			short(w.g.BuiltAtCommit), short(w.opts.HeadSHA)))
	case w.g.BuiltAtCommit == "" && w.stale():
		notes = append(notes, "the graph is older than files this change touches and it records no build revision, so the expansions below may describe code as it was, not as it is (run `graphify update`)")
	case w.g.BuiltAtCommit == "":
		notes = append(notes, "the graph records no build revision, so it was checked against the changed files' timestamps rather than against the revision it describes")
	}
	if failed := w.failedSources(); len(failed) > 0 {
		notes = append(notes, fmt.Sprintf(
			"the graph's own build recorded %s as failing to extract, so nothing in this change's context comes from them",
			w.sample(failed)))
	}
	if len(w.unresolved) > 0 {
		notes = append(notes, fmt.Sprintf(
			"the graph holds no nodes for %s; a file whose grammar is not installed and a file with nothing structural in it look identical here",
			w.sample(w.unresolved)))
	}
	if len(w.bare) > 0 {
		notes = append(notes, fmt.Sprintf(
			"%s resolved to a node with no source file, so what the change connects to there was not followed; that is what an incremental graph update leaves behind when the real definition sits outside the batch it re-extracted",
			w.sample(w.bare)))
	}
	if len(w.missing) > 0 {
		notes = append(notes, fmt.Sprintf(
			"the graph names %s, which is not in the tree under review, so their context was left out",
			w.sample(w.missing)))
	}
	semantic, unknown := w.g.skipped(w.owned)
	if len(semantic) > 0 {
		notes = append(notes, fmt.Sprintf(
			"edges of kind %s were left out on purpose: a model wrote them, so two rebuilds of this commit need not agree on them and a review built from them could not be replayed",
			strings.Join(semantic, ", ")))
	}
	if len(unknown) > 0 {
		notes = append(notes, fmt.Sprintf(
			"edges of kind %s were not followed because this adapter has no mapping for them; a grammar Graphify learned since it was written looks exactly like this",
			strings.Join(unknown, ", ")))
	}
	if !w.g.TagsOrigin && len(w.owned) > 0 {
		notes = append(notes, "the graph does not record which of its two halves wrote each edge, so model-written edges were excluded by relation name alone; a rebuild with a current Graphify makes that check exact")
	}
	if w.dangling > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d edge(s) point at nodes the graph does not contain and were dropped", w.dangling))
	}
	return notes
}

// stale reports whether the change is newer than the graph. It is the check
// a coverage profile gets, for the same reason: an artifact written before
// the code it describes cannot be describing it.
func (w *walk) stale() bool {
	if w.opts.GraphPath == "" || w.opts.Root == "" {
		return false
	}
	return cover.ArtifactStale(w.opts.Root, w.opts.GraphPath, w.opts.Changed)
}

// failedSources are the changed files Graphify's build says it could not
// extract.
func (w *walk) failedSources() []string {
	if len(w.g.FailedSources) == 0 {
		return nil
	}
	failed := map[string]bool{}
	for _, f := range w.g.FailedSources {
		failed[normPath(f)] = true
	}
	var out []string
	for _, p := range w.opts.Changed {
		if failed[normPath(p)] {
			out = append(out, normPath(p))
		}
	}
	return out
}

func (w *walk) sample(items []string) string {
	sorted := append([]string{}, items...)
	sort.Strings(sorted)
	limit := w.opts.maxNoteExamples()
	if len(sorted) <= limit {
		return strings.Join(sorted, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(sorted[:limit], ", "), len(sorted)-limit)
}

// promptFragment is the graph's half of the review prompt. It says what kind
// of evidence these expansions are, because a name-resolved edge cannot back
// the claim a type-resolved one can and the reviewer has no other way to know.
const promptFragment = `The context tagged graphify comes from a tree-sitter index of the whole
repository, not from a language toolchain. Its edges match by name, so a
caller listed here calls something spelled like the changed symbol and may
not call the changed symbol itself; weigh it as a lead, not as proof. Each
expansion's details carry the graph's own confidence: EXTRACTED means the
source states the connection, INFERRED means the index deduced it.

Expansions in the neighbor role are files the index puts next to the change
that no other role describes, usually of a different kind: a migration beside
the code that writes the row, a config file beside the code it configures.
Those pairings are worth reading together, and a disagreement between them is
the kind of thing a single-language review misses.`

type sourceCache struct {
	root  string
	files map[string][]string
	rels  map[string]string
	bad   map[string]bool
}

// read returns the file's repository-relative path and its lines. The path in
// the graph is anchored to whatever root Graphify was given, which is not
// always the repository root, so an absolute path and a path with extra
// leading segments both have to land on the same file the diff names.
func (s *sourceCache) read(sourceFile string) (string, []string, bool) {
	if s.files == nil {
		s.files = map[string][]string{}
		s.rels = map[string]string{}
		s.bad = map[string]bool{}
	}
	key := normPath(sourceFile)
	if s.bad[key] {
		return "", nil, false
	}
	if lines, ok := s.files[key]; ok {
		return s.rels[key], lines, true
	}
	rel, data, ok := s.load(key)
	if !ok {
		s.bad[key] = true
		return "", nil, false
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	s.files[key] = lines
	s.rels[key] = rel
	return rel, lines, true
}

func (s *sourceCache) load(key string) (string, []byte, bool) {
	if filepath.IsAbs(key) {
		if data, err := os.ReadFile(key); err == nil {
			rel, err := filepath.Rel(s.root, key)
			if err != nil || strings.HasPrefix(rel, "..") {
				return "", nil, false
			}
			return normPath(rel), data, true
		}
		return "", nil, false
	}
	candidate := filepath.Join(s.root, filepath.FromSlash(key))
	if data, err := os.ReadFile(candidate); err == nil {
		return key, data, true
	}
	// A graph built from a subdirectory carries that prefix on every path.
	// Peel one segment at a time rather than guessing the prefix.
	rest := key
	for {
		i := strings.Index(rest, "/")
		if i < 0 {
			return "", nil, false
		}
		rest = rest[i+1:]
		if data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(rest))); err == nil {
			return rest, data, true
		}
	}
}

func priorityFor(e Edge) int {
	switch e.Confidence {
	case "EXTRACTED":
		return 60
	case "INFERRED":
		return 30
	default:
		return 10
	}
}

func confidenceOf(e Edge) string {
	if e.Confidence == "" {
		return "AMBIGUOUS"
	}
	return e.Confidence
}

func sameCommunity(a, b Node) bool {
	return a.Community != nil && b.Community != nil && *a.Community == *b.Community
}

func communityName(n Node) string {
	if n.CommunityName != "" {
		return n.CommunityName
	}
	if n.Community != nil {
		return fmt.Sprintf("%d", *n.Community)
	}
	return ""
}

func versionOf(commit string) string {
	if commit == "" {
		return "unknown"
	}
	return "graph@" + short(commit)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func appendUnique(list []string, s string) []string {
	if s == "" {
		return list
	}
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

func dedupe(sorted []string) []string {
	var out []string
	for i, s := range sorted {
		if i > 0 && s == sorted[i-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}
