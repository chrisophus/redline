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

A local tree-sitter parse of a repository into a graph on disk, for the code
half. No model, no embeddings, no network *there*. It writes
`graphify-out/graph.json` alongside an HTML view and a report. `graphify
update <path>` re-extracts only changed files, and a git hook keeps it
current across commits and checkouts. The query side is a CLI: `graphify
query "<question>"` returns a scoped subgraph, `graphify path "A" "B"` traces
how two things connect, `graphify explain "X"` describes one node. Edges are
tagged EXTRACTED, INFERRED, or AMBIGUOUS, so the graph says which connections
it read and which it guessed at.

Graphify also has a second half, for docs, papers, and images: semantic
extraction dispatches subagents (or calls Gemini if a key is set), i.e. a
real model call. Dogfooding this on Marketplace Core confirmed it directly —
a full build ran 22 subagents and ~2.5M output tokens over ~400 doc/image
files. The `caller`/`sibling`/`type` roles below draw only from the code
half's tree-sitter edges, which is the right instinct; say so explicitly, so
nobody reads "no model" and assumes it covers the semantic edges too
(`semantically_similar_to`, `conceptually_related_to`, `rationale_for`).
Those are real and sometimes useful, but they are LLM output, not tree-sitter
output, and the determinism claim below does not extend to them.

It parses about 37 grammars, and the list is the interesting part: Go and
TypeScript, but also SQL, Terraform, Bash, JSON, package manifests, Markdown.
That covers the kinds a change spans and no single language toolchain
reaches — in principle. SQL is the flagship case this doc opens with, and it
does not work out of the box: `extract_sql()` depends on `tree_sitter_sql`,
an optional package the standard `pip install graphifyy` / `uv tool install
graphifyy` flow does not pull in. Missing it, the extractor returns
`{"nodes": [], "edges": [], "error": "tree_sitter_sql not installed..."}`,
and the merge step in `extract()` drops the `error` key — so the failure
never surfaces anywhere, not in the build log, not as a `notes` entry.
Confirmed on Marketplace Core: `detect()` correctly buckets 88 real
migrations under "code," and the built graph has zero nodes from any of
them, with no warning printed at any point in the pipeline. A missing
grammar looks identical to "this file had nothing structurally interesting,"
which is the "coverage that looks total" risk below, except one layer lower
than where that section places it — at extraction, before an adapter or a
query ever runs, where the release valve (`notes`) can't reach it because
the failure never reaches the adapter at all. Audit the scoped grammars are
actually installed, and make a missing one fail loud, before leaning on this
example.

Two properties matter for the claim above. It persists, and it is
deterministic on the code half — the tree-sitter half only.

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

  It is sharper than that in the incremental case, and worth the adapter
  guarding against directly: running `graphify update` on a single batch of
  changed files does not just misroute an edge to a same-named wrong node,
  it can invent a new unqualified node with no `source_file` when the real
  target is defined outside that batch, rather than resolving to the node
  that already exists for it. Confirmed on Marketplace Core after a 15-file
  incremental update: `Client` had 48 nodes sharing that label, 46 of them
  bare; same shape for `Provider`, `AgreementCandidate`,
  `OrganizationPolicies`. A one-or-two-hop walk that lands on one of these
  is a dead end that looks like "nothing here" rather than "this symbol
  lives outside the batch this update touched" — the adapter should treat a
  hop into a node with no `source_file` as an unknown, not silence.
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

Staleness is not the only way this breaks. The semantic half is a model call
(see above), so two *fresh* rebuilds of the identical commit are not
guaranteed to produce the same `semantically_similar_to` / `rationale_for`
edges — an LLM pass does not replay byte-identical the way tree-sitter does.
A staleness check on `graph.json`'s mtime cannot catch this, because the
graph is not stale, it is just non-reproducible. The role mapping above
already sidesteps this by only drawing `caller`/`sibling`/`type` from the
tree-sitter half, which is the correct fix — keep it that way on purpose,
not by accident, and treat pulling a semantic edge into the one-shot
envelope as a determinism regression if anyone proposes it later.

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

These are all Redline-side. They do not cost Graphify's own build: a full
build over a large repo (~4,000 files, ~400 non-code) ran 22 subagents,
~2.5M output tokens, and real wall-clock minutes to cluster ~38k nodes — and
an AST-only incremental update still costs wall-clock minutes to re-cluster
even with zero token spend. Whoever adopts this pane owns keeping
`graphify update` current (the git hook, or CI) as a standing cost beside
the adapter itself, not a one-time build.

## What the first cut built

`cmd/redline-graphify-context` is the adapter, with the mapping in
`internal/graphify` and a boundary test that stops anything else in the
module importing it. The M and the first S above; the `neighbor` role and the
explore-mode queries are not built.

It lives here rather than in its own repository because it parses nothing:
the language boundary test is satisfied, one `make build` produces both
binaries, and the fixture graph sits next to the contract it is written
against. If it earns its own repository, the boundary test names exactly what
has to travel with it.

Three things about the graph were not knowable when this was written, and two
of them changed the design.

`graph.json` is NetworkX `node_link_data`, so the edge list is `links` and
not `edges` (the loader accepts both), and the file carries `built_at_commit`
at the top level. That last field is a better staleness check than the mtime
one proposed above, and it is what `provider.version` carries: two graphs of
the same repository at different revisions are different inputs as far as a
replayed review is concerned. The adapter compares it to the revision under
review and says so in `notes`, which means staleness needs no Redline-side
code at all. The harness profile is then the ordinary kind, a path and a
produce command, with nothing about graphs in it.

The determinism guard needed a third test. Nodes carry `file_type`, so an
edge into a document, paper, image, rationale or concept node is the semantic
half and is refused. Relations are checked against an allowlist of what the
tree-sitter extractors emit, which refuses `semantically_similar_to` and its
siblings by name. Neither is sufficient: Graphify's semantic pass is allowed
to write `calls`, `implements` and `references` between two code nodes, and
those pass both tests. What separates them is `_origin`, which Graphify
stamps `"ast"` on every node and edge the tree-sitter pass writes and which
persists into `graph.json`. Where a graph carries it, it decides. Where it
does not, the weaker check runs and the envelope says so in a note rather
than letting one pass for the other.

The build revision is usually absent. `graphify update .` on this repository
wrote a graph with no `built_at_commit` at all, so `provider.version` reads
"unknown" and the commit comparison has nothing to compare. The fallback is
the mtime check the section above proposed, the same one a coverage profile
gets: a graph older than a file this change touches cannot be describing it.
Both checks run, and the envelope says which one it had.

The SQL gap is confirmed at `graphify/extractors/sql.py:284`: without
`tree_sitter_sql` the extractor returns `{"nodes": [], "edges": [], "error":
...}` and the error is dropped downstream. Redline cannot fix that from here,
and it does not have to. The adapter knows which files it claimed, so a
claimed file the graph holds no nodes for becomes a note and reaches the
report as an unknown. A missing grammar and a file with nothing structural in
it still look identical, but the review is now told that something it claimed
came back empty.

## What was left out, and why

The callee is not sent. An outgoing `calls` edge points at what the changed
code calls, which is often the most useful thing to read, and no role in the
vocabulary describes it. Sending it under `neighbor` would blur the one
measurement `neighbor` exists to produce, so it waits for that answer.

Community membership raises an expansion's priority within its role and never
produces one of its own. A community on a large graph runs to hundreds of
nodes with no edge to the change; walking it would be an unbounded amount of
weakly related code competing for the same ceiling. One hop, with the
community as a tiebreak, is the bounded version of "other files in the same
community".

Nothing is measured yet. The adapter has unit tests and a fixture graph; it
has no eval fixture, so the clean rate and the correlation findings are still
the open question the effort list above was sized against.

## What one real run looked like

`graphify update . --no-cluster` over this repository: 142 files, 1516 nodes,
4610 edges, no model call. The adapter against that graph, on a change to its
own source, sent five expansions, and every one of them was `neighbor`.

That is worth reading carefully before anyone concludes much from it. No
`caller`, because `--defer-callers **/*.go` hands those to gorefactor, which
is the arrangement working as intended. No `type` and no `sibling`, because
the Go extractor emits no `implements` or `inherits` edges at all: the
relations in that graph are `calls`, `contains`, `references`,
`imports_from`, `method`, `depends_on`, `defines` and `embeds`. So on a Go
repository the interface-shaped roles will almost never fire, and what is
left is the fallback: a `references` edge to a type the change names, sent as
`neighbor` because a `references` edge cannot back the `type` role's claim
on its own.

It could, with a label heuristic. Graphify's Go extractor labels functions
`Name()`, methods `.Name()` and types bare, so "a `references` edge whose
target has no parentheses is a type" would promote most of those five. That
is guessing a language's conventions out of a string, in the one component
that is supposed to know no language, to make a claim the edge does not
carry. Left alone on purpose.

The bare-node problem is real here too, not only on Marketplace Core: the
same run reported `Graph`, `Options`, `io.Writer` and `testing.T` resolving
to nodes with no source file.

The reading: on a single-language repository that already has an exact
provider, the graph adds type-definition context and not much else. The
correlation case it was built for needs a repository whose changes actually
span kinds, and that is what the missing eval fixture has to be drawn from.

## The third option, which is what shipped next

This doc frames the choice as one-shot expansions now and the explore loop
later, with the loop deferred because a live query in the reviewer's own turn
cannot be replayed by the eval. There is a third arrangement it does not
consider, and on the numbers it beats both: put the loop in a cheap model in
front of the expensive one.

`cmd/redline-scout` is that. It is a context provider like any other, so
Redline runs it as a subprocess and reads an envelope; what is inside is a
tool loop on Sonnet under a cost cap. It reads the diff, decides what this
change needs, and fetches it. The reviewer stays one-shot over a fixed
payload, which is the property the deferral was protecting: the scout's
findings are frozen into the envelope before the expensive call happens, so a
saved session still replays.

Three things it settles that the graph adapter could not.

The costs come out in the right places. Three turns of exploring on Opus is
about a dollar and every resend is billed at Opus rates; the same three turns
on Sonnet, with the result handed to one Opus call, is about sixty cents and
the resends are billed at Sonnet's. Prompt caching does not close that gap in
the one-shot case at all, because a cache write is 1.25x the base rate and a
review has no second request to read it back.

The graph's CLI turns out to be the right interface after all, one layer up.
The section above rejected `graphify query` and `affected` for the adapter
because they answer a question in prose and truncate to a token budget, which
is the wrong shape for a program. It is the right shape for a model: the scout
gets `graph_affected` and `graph_path` as tools, and `graph path "A" "B"` is
the cross-kind correlation question asked directly rather than reconstructed
from a one-hop walk. So the graph earns its place as a tool even where the
adapter's own expansions did not.

And the roles become a claim someone actually made. The adapter guesses a role
from an edge relation, which is why five type definitions arrived under
`neighbor`. The scout knows why it fetched something, because it went and got
it for a reason, so `history` on a deleted guard and `caller` on a changed
signature are the reading of the change rather than a mapping table's best
effort.

What it costs is reproducibility, and only on its own side. gorefactor writes
the same envelope for a revision every time and the scout does not, so
`provider.version` carries the model and the effort, and regenerating a
fixture that used it produces a different one. The reviewer's half keeps the
property that matters. Whether the trade is worth it is the same measurement
this whole doc has been waiting on: the fixture set, reviewer alone against
reviewer plus scout, on the clean rate and the correlation findings.
