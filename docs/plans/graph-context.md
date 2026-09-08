# A standing graph beside the per-change envelope

What Redline could get from Graphify (github.com/Graphify-Labs/graphify), and
what it would have to be careful about. Nothing here is committed. The design
doc raises the question and stops; this is the answer worked out far enough to
decide with.

## The gap this is aimed at

The context envelope is derived per change and travels in the request. Every
review pays for the whole payload again, and the ceiling that bounds one
request is therefore a bound on how much a reviewer can be shown. An index
built once over the repository and queried has the opposite shape: cost is
paid at build time and a query returns the small relevant piece. That is the
single largest structural improvement available to wave two, and it is why
Copilot's agentic loop is cheap and Redline's would not be.

The Go provider is not that index. It resolves through go/types, so it is
exact about "you changed this signature and here is who calls it", and it
knows only Go. A migration read against the struct that writes the row is the
correlation case wave two exists for, and half of it is a SQL file no Go type
checker will ever see.

## What Graphify is

A local tree-sitter parse of a repository into a graph on disk. No model, no
embeddings, no network. It writes `graphify-out/graph.json` alongside an HTML
view and a report. `graphify update <path>` re-extracts only changed files,
and a git hook keeps it current across commits and checkouts. The query side
is a CLI: `graphify query "<question>"` returns a scoped subgraph, `graphify
path "A" "B"` traces how two things connect, `graphify explain "X"` describes
one node. Edges are tagged EXTRACTED, INFERRED, or AMBIGUOUS, so the graph
says which connections it read and which it guessed at.

It parses about 37 grammars, and the list is the interesting part: Go and
TypeScript, but also SQL, Terraform, Bash, JSON, package manifests, Markdown.
That covers the kinds a change spans and no single language toolchain reaches.

Two properties matter for the claim above. It persists, and it is
deterministic on the code half.

## Where it fits

Nowhere new. It is a second context provider beside gorefactor, found by
config, merged into one budget by role rank:

```yaml
context:
  - name: graphify
    command: redline-graphify-context
    args: ["--graph", "graphify-out/graph.json", "--changed", "{{base}}"]
    scope: ["**/*"]
```

The command is an adapter, not Graphify itself: something has to read
`graph.json`, work out which nodes the changed files own, walk one or two hops
out, and write an envelope. It is small, and it is where every mapping
decision below lives. Redline would not know a graph was involved.

`scope: ["**/*"]` is the point of it. gorefactor claims `**/*.go` and every
other changed file is reported as unexamined by the context layer today.

## Roles, and the honest mapping

The envelope's roles are a claim about what a piece of context is, and the
model is told which claim it is reading. A name-resolved graph edge cannot
back the same claim a type-resolved one can.

- `caller`. Leave it to gorefactor for Go. A tree-sitter edge matches by
  name, so two methods called `Insert` on different types are one edge, and
  "here is who calls what you changed" turns into "here is who calls
  something spelled like it". For a language with no exact provider, a graph
  caller is better than nothing, and it should carry the EXTRACTED or
  INFERRED tag in `details` so the reviewer can weigh it.
- `sibling`. A good fit. Other implementations of a touched interface, other
  files in the same community the graph detected.
- `type`. Usable where the edge is EXTRACTED.
- The cross-kind neighbor has no role today. A migration node adjacent to the
  struct that writes the row is exactly the thing wave two wants and none of
  `enclosing`, `caller`, `type`, `sibling`, `test`, `history` describes it.
  Two options: emit it under a new role like `neighbor` and let the unknown
  role rank last and be reported, which the contract already handles, or add
  the role to the vocabulary. Try it as an unknown role first. If the
  correlation findings show up, that is the evidence for promoting it.

`test` expansions from the graph are held back like every other test
expansion, so do not spend adapter work there.

## The three things that could go wrong

**Determinism.** The envelope for one revision must be byte-identical on every
run, or the eval measures noise. `graph.json` is a build artifact that drifts
with whatever last ran `graphify update`, and an adapter reading a stale graph
writes a different envelope for the same commit. Treat it exactly like
`coverage.out` and `mutants.json`: a harness profile with a path and a
staleness check, so a graph older than the change is said out loud rather than
silently spent. The adapter should also put the graph's own build revision in
`provider.version`, which is what that field is for.

**Precision leaking into a claim.** Covered by the role mapping above. The
rule is that Redline never presents an INFERRED edge as a resolved fact, and
the tag rides in `details` where it renders generically.

**Coverage that looks total.** A graph over the whole repository will answer
something for almost any question, and a subgraph that misses the one relevant
edge looks identical to one that found nothing to say. `notes` is the release
valve: the adapter says what it could not resolve, and those reach the report
as unknowns.

## The larger prize, and why it is second

Explore mode already has the shape an index wants: a catalogue and a fetch
tool over a frozen corpus. Replacing that corpus with graph queries, so the
reviewer asks "what writes this table" and gets an answer rather than picking
from a list Redline guessed at in advance, is the version of this that removes
the per-change payload instead of adding to it.

It costs the property that makes the eval work. The tool's corpus today is the
frozen envelope, so an explored review replays from a saved session with no
filesystem and no network. A live `graphify query` in the loop is a call into
a mutable index, and a fixture cannot replay it. The way through is to freeze
the queries and their answers into the session the way the envelope is frozen,
which is real work and should wait until the one-shot version has shown the
graph earns its keep.

So: one-shot expansions first, measured against the same fixtures, on the
clean rate and on correlation findings. The loop after, if the first half
pays.

## Effort

- Adapter that reads `graph.json` and writes an envelope, one hop, siblings
  and cross-kind neighbors only: **M**.
- Harness profile and staleness wiring for `graph.json`: **S**, and it reuses
  what mutation already does.
- `neighbor` role, if the measurements ask for it: **S**.
- Graph queries as an explore-mode tool, with session freezing: **L**.
