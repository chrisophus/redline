# Redline — Design Doc

Status: draft. Core model and scope settled; comment lifecycle and anchor
derivation outstanding.

## Problem

Reviewing a change by reading its diff forces the reviewer to mentally reconstruct
the change's effects in the domain where it actually lives: what the API contract
now promises, what the UI now looks like, what a migration does to real rows.
That reconstruction is slow, error-prone, and the reason nontrivial PRs sit
unreviewed.

Existing AI review tools address this by **reading** the change — summarizing it,
diagramming its call flows, ordering it into a guided walkthrough. That is real
and it works. But everything so produced is inference from source: a model of what
the code says it will do. Redline's premise is the complement. **Evidence from
execution, not inference from source.** A diagram derived from a migration file
cannot tell you that the migration acquires `ACCESS EXCLUSIVE`, that it is a no-op
against an existing database, or that it leaves three seeded rows violating the
constraint it just added. Only applying it to a real database can.

There is a second, sharper version of the problem. In a spec-driven Go/TypeScript
stack, every tool already validates itself against a machine-readable declaration
— golang-migrate has migration files, sqlc has schema and queries, ogen has an
OpenAPI spec, vacuum lints that spec, golangci-lint and the coverage gate cover
the rest. Run `make pre-push` and everything is green. But those declarations are
redundant descriptions of one system, and **nothing owns the gaps between them.**
The defects that survive a green pre-push live in those gaps.

## Prior art

CodeRabbit is the closest thing to Redline in the field, and the overlap is
larger than it first appears. It ships PR walkthrough summaries, sequence diagrams
for new call flows, ERDs for data-model changes, and "Change Stack" — a
layer-by-layer reordering of a PR into logical rather than alphabetical order. It
has a CLI that reviews staged and unstaged changes pre-commit. It clones into an
isolated microVM, builds the project, runs 20+ linters and static-analysis tools,
and runs the tests. Its free tier covers private repositories, so the local
pre-push loop is available at no cost.

Three things are outside it, and they are what Redline is:

1. **Observation of a running system at two revisions.** CodeRabbit's sandbox is
   for analysis — linters, tests, grep. It does not stand up the application,
   apply migrations to a live Postgres and diff the resulting schemas, drive the
   UI, or diff actual API responses.
2. **Determinism.** Its review agent investigates by writing its own shell
   commands rather than calling predefined tools. Redline makes the opposite
   trade: a closed check catalog and a closed-slot agent boundary, so the same
   working tree always produces the same evidence.
3. **Stack-specific cross-tool checks.** A general-purpose reviewer across all
   languages will not carry "you modified a migration that is already merged, and
   golang-migrate does not checksum."

Redline is built to be used, not sold. That has a design consequence: **feature
overlap with a general-purpose reviewer is a reason to cut scope, not a reason to
compete.** Anything commoditized elsewhere — prose summaries, generic findings,
call-graph analysis — is maintenance burden with low marginal value. Effort goes
to the checks nobody else will ever build for this stack.

## Goals

1. **Publish the working set, not just the verdict.** An automated reviewer builds
   a large body of understanding — what calls what, which assumptions hold, what a
   migration actually does to rows — and then discards nearly all of it, keeping
   only the handful of checks that failed. That discarded material *is* the review
   work, and a human reviewer otherwise has to reconstruct it from scratch. Redline
   emits **an evidence artifact with findings as annotations**, not a findings list
   with evidence attached. Rendered in the domain of the change: OpenAPI diffs for
   contract changes, before/after screenshots for UI, migrations executed against a
   real database with genuine before/after schema and row states.

2. **Check consistency across the toolchain, not within any single tool.** Redline's
   distinct contribution is cross-tool: migrations vs. the deployed schema,
   migrations vs. sqlc-generated Go, spec vs. handler semantics, spec vs. observed
   responses. Each tool is green in isolation; Redline checks that they agree.

3. **A set of tools and skills driven by a coding agent.** Redline is not an
   application with a UI. It is composable CLI subcommands that emit structured
   evidence, plus skills that teach Claude Code when and how to invoke them. The
   agent is the driver and the presentation layer. Local-first and pre-push are
   hard constraints — no repo egress, no external service — but not
   differentiators. A live web surface and post-push/CI rendering are possible
   later front ends over the same JSON; nothing in v1 depends on either.

4. **One findings stream, consumed by the agent.** Deterministic sources —
   Redline's own panes, gorefactor's doctor, linters — emit into a single JSON
   stream scoped to lines in the diff, deduped and capped. The agent reads it and
   decides how to present it. Noise is treated as a first-class failure mode.

5. **Findings become the agent's work queue.** The stream is ordered work the
   invoking agent resolves one item at a time. Mechanical fixes are routed to
   gorefactor's cheap-model + AST-verification path rather than burning the strong
   model. Human accept/dismiss state lives in `comments.json` keyed by
   fingerprint; whether that ever gets an interactive surface is deferred.

6. **Deterministic evidence, non-deterministic commentary — never the reverse.**
   What Redline executes and observes is fully reproducible: same working tree,
   same evidence, every time. The LLM reads rendered results and writes prose and
   LLM-source findings; it never decides what runs or what the evidence says, and
   its findings are labeled `Source: "llm"` so they are never mistaken for
   deterministic ones. A reviewer must be able to trust that the evidence in front
   of them is not a sample from a distribution.

7. **Maximize signal per pixel.** Generated files (ogen output, sqlc output,
   lockfiles, go.sum, protobuf, UI build output) are hidden by default via
   linguist-style detection plus .gitattributes and per-repo config. Panes appear
   only when applicable to the change. Findings are ranked by surprise, not
   severity (see "Ranking by surprise").

8. **Contracts over coupling.** A stable findings JSON schema is the integration
   surface. Doctor already defines it and Redline adopts it; the future CI gate
   consumes it. Edit requests to gorefactor use gorefactor's task input shape. No
   shared internals between the three tools — Redline reimplements the schema
   rather than importing `gorefactor/doctor` — and each remains independently
   useful.

9. **Zero-friction distribution.** One static Go binary and a skill directory. No
   node, no npm, no server to run, no frontend to build. Visual output is a
   self-contained HTML file written to disk, not a hosted app.

10. **Extensible by pane.** Each pane is a subcommand implementing a small
    interface (applicability detection + observe + diff) and emitting the shared
    findings schema. SQL, OpenAPI, and screenshots are the first three; the
    dispatcher stays ignorant of pane semantics.

## Shape

Redline is a binary of subcommands plus a skill directory. The agent composes
them; there is no server and no frontend.

```
redline init                  # setup-time discovery → redline.toml
redline run   [--base REF]    # every applicable pane, applicability from config+diff
redline sql   [--base REF]    # one pane, for drilling in
redline api   [--base REF]
redline ui    [--base REF]
redline cover [--base REF]
```

Every subcommand emits the findings schema on stdout and writes evidence
(schema dumps, screenshots, response captures) under `.redline/`. Skills wrap
them: when to invoke, how to read the output, how to present it.

**`redline run` is the deterministic entry point and the default.** Applicability
is computed from `redline.toml` and the diff, never chosen by the agent. This
matters: if the agent decides which checks to run, coverage becomes a sample from
a distribution and a check can be silently skipped. Individual subcommands exist
for drilling in after `run`, not as the normal path.

**Visual output is a self-contained HTML file**, written to `.redline/report.html`
and surfaced by the agent. Goal 1 makes the artifact the product, and before/after
screenshots and schema diffs are inherently visual — but that needs a file, not a
server. No port discovery, no live app, no comment lifecycle UI.

Note the cost of that cut. Prequel's stated value is not diff rendering but that a
GitHub-shaped interface *shifts the reviewer into review mode*. A static file has to
recover some of that or the saving is illusory. This is the main open risk in the
shape decision.

### Report structure

Four sections, in this order. Most review tools ship only the fourth.

1. **What changed** — the rendered evidence, in the domain of the change.
2. **What was checked and held** — confirmations, collapsed but present. Every
   question the reviewer does not need to ask again.
3. **What could not be determined** — and why. Panes that did not run, changed
   handlers no seeded route exercises, migrations with an out-of-band backfill.
   This tells the reviewer where to spend their own attention, and it is the
   dark-sensor principle generalized past "the pane crashed."
4. **Findings** — ranked by surprise.

Section 3 is the one most easily skipped and the most costly to omit. A report that
silently covers 60% of a change reads exactly like one that covers all of it.

This shape collapses a large amount of previously-planned work: the local web
server, the frontend framework, the PATCH-to-resolved comment lifecycle, and the
port-range/healthz discovery machinery are all out.

## Non-goals (v1)

- **Merge gating.** The CI status-check gate is a separate thin consumer of the
  findings schema, designed elsewhere. Redline never blocks anything.
- **Real production data.** The SQL pane runs exclusively against seeded or fake
  data in a throwaway container. No live-database connections in any mode.
- **Bot approvals or review submission to GitHub.** Redline is pre-push; it does
  not talk to the GitHub review API.
- **Replacing the deterministic linting stack.** golangci-lint, vacuum, sqlc vet,
  doctor, etc. remain the source of deterministic findings; Redline consumes and
  cross-checks, never reimplements.
- **Reimplementing diff engines Redline can consume.** OpenAPI diffing uses
  `pb33f/libopenapi`'s `what-changed`. Postgres schema diffing uses
  `stripe/pg-schema-diff` (Go; `migra` is the Python equivalent if it proves a
  better fit). Coverage comes from Go's cover profile.
- **Language-universal panes.** Diff rendering and findings are
  language-agnostic; the initial rich panes target the Go / TypeScript /
  Postgres / web stack.
- **A plugin API.** Extension happens by adding a pane to the binary or a slot to
  the config schema, not by letting repos ship arbitrary code.
- **A web application.** No local server, no port discovery, no frontend
  framework, no interactive comment lifecycle. A static HTML report file covers
  the visual need; an app can come later over the same JSON if the file proves
  insufficient.
- **Direct LLM API access.** Redline calls no model. Prose and judgment come from
  the agent driving it. Adding an API path later is possible but is not on the
  v1 line, and no check may depend on one.

## Reference stack

Redline is designed against a specific, common project shape. Where a decision
could go several ways, this shape breaks the tie.

- **Go** service, **TypeScript/React** UI, **Postgres**.
- **golang-migrate** for schema migrations (versioned up/down SQL files).
- **sqlc** for query-to-Go generation.
- **ogen** generating the server from an OpenAPI 3 spec; generated code committed
  and verified in CI.
- **vacuum** for OpenAPI linting; **golangci-lint** and a coverage threshold for Go.
- A **Makefile** as the de-facto interface: `build`, `run`, `seed`, `test`,
  `lint`, `generate`, `verify-generated`.
- A **mock/offline mode** so the stack starts with no cloud credentials, and an
  **in-memory store** so UI and API work does not always require Postgres.

The `marketplace` repo is the reference implementation of this shape and the
development target. Two properties of it are load-bearing for the design: the
Makefile is already a bring-up contract, so no new config is needed to know how to
start the app; and mock adapters plus an in-memory store make standing the stack
up cheap enough to do twice per run.

## The consistency matrix

This is the generator for panes and checks. Each row is a pair of declarations
that describe the same system; the gaps are where defects survive a green build.

| A | B | Owner today |
|---|---|---|
| migrations | running/deployed schema | **nobody** |
| migrations applied fresh | migrations applied incrementally | **nobody** |
| migrations | sqlc-generated Go | **nobody** |
| sqlc queries | a real database | `sqlc vet` (if run) |
| `openapi.yaml` | ogen output | `verify-generated` ✓ |
| `openapi.yaml` | handler semantics | **nobody** |
| spec at base | spec at head | **nobody** (breaking changes) |
| ogen server contract | observed responses | **nobody** |
| migrations | API response shape | **nobody** |
| added diff lines | tests that execute them | coverage % (repo-wide, wrong granularity) |
| lines executed | lines asserted on | **nobody** |

The end-to-end case worth stating explicitly: a migration drops a column, sqlc
regenerates, the store struct loses a field, but `openapi.yaml` still declares it
required — so the API returns null for a documented-required field. Every tool
passes. Only the chain check catches it.

When considering a new check, the question is "which row of this matrix does it
close?" A check that does not close a row is probably something an existing tool
already owns.

## Execution model

Every pane is the same operation: observe the system at two revisions and diff the
observations. This is the core primitive, not a per-pane detail.

```
base revision (merge-base)  ─┐
                             ├─→ identical probes ─→ diff observations ─→ findings
working tree                 ─┘
```

Consequences:

- Two worktrees are materialized per run. The base-revision side is content-addressed
  and cached; it only changes when merge-base moves, so steady-state cost is one
  build, not two.
- Bringing the stack up is a first-class capability, not an optional extra. The
  reference stack makes this cheap (mock mode, memory store, one embedded binary).
- Observations must be **normalized** before diffing — timestamps, UUIDs, row
  ordering, antialiasing. Un-normalized observation diffing is 100% false
  positives on the first run, which is how a tool like this dies. Normalizers are
  declared per-pane in config.
- Executing with the real toolchain means Redline does not need to model tool
  internals. It does not need a lint rule for "golang-migrate's Postgres driver
  runs migrations in a transaction, so `CREATE INDEX CONCURRENTLY` fails" — it
  runs the migration with the actual driver and observes the failure.

### Pane interface

```go
type Pane interface {
    Name() string
    Applies(ctx Context, diff Diff, cfg Config) bool
    Observe(ctx Context, rev Revision) (Observation, error)
    Diff(before, after Observation) (Render, []Finding, error)
}
```

`Observe` is the expensive, side-effectful half and is cached by
(pane, revision-hash, config-hash). `Diff` is pure. Panes that need no running
system implement `Observe` as static extraction; the interface does not change.

## Ranking by surprise

Severity is the wrong axis. The easy findings — lint, coverage threshold, test
failure — are already handled and are scalar-with-a-threshold. What is left has no
threshold, so it needs a **reference expectation**, and the diff itself supplies one.

```
surprise = observed − expected-from-diff
```

If the diff adds one endpoint and the OpenAPI observation shows one endpoint
added, that is confirmation: render it collapsed. If the diff adds one endpoint
and the observation shows two contract changes, the second one is the finding. If
a migration says "add column" and the observed schema diff says "add column,
implicit index, table rewrite," the tail is the signal.

This ranks the *findings*. It does not decide what gets rendered.

Confirmations — the checks that came back clean — are **the deliverable**, not
suppressed noise. "Applied against 50k rows, no table rewrite, `ACCESS SHARE`
held 12ms" is valuable precisely because it is not a finding: it is a question the
reviewer no longer has to ask. Confirmations are collapsed for density and never
emit findings, but discarding them would throw away most of goal 1.

The distinction: surprise decides **emphasis**. It never decides **inclusion**.

## The agent boundary

Redline needs repo-specific knowledge it cannot hardcode, and an agent is the
right way to get it. The boundary must be strict or the tool becomes
unpredictable, and a reviewer cannot trust an unpredictable tool.

**The agent discovers how to run the repo. Redline owns what to check.**

### Role 1 — setup-time agent: repo → config

Runs once, and again when discovery inputs change. Emits a checked-in
`redline.toml` conforming to a fixed schema. The config is human-reviewable,
hand-editable, and diffable in a PR. Every subsequent run reads the file and is
deterministic. The agent is not in the hot path — Redline is a pre-push tool and
cannot wait on an LLM to re-derive `make build` on every invocation.

Most discovery is deterministic and the agent should not touch it:

- `go.mod` dependencies → golang-migrate / ogen / sqlc presence
- config file existence → `sqlc.yaml`, `.ogenserver.yml`, vacuum ruleset
- Makefile target names by convention → build / run / seed / test / generate
- `package.json` scripts → dev / build
- `.gitattributes` + linguist → generated files
- migration directory by convention → `db/migrations`, `migrations/`

The agent is needed only where convention runs out:

- which run mode starts the stack with no cloud credentials (in the reference
  repo this means reading the adapter wiring and `.env.example` to find the mock
  switch — no convention gets you there)
- the UI route inventory, out of the React router
- required vs. optional env vars, and safe local values
- the readiness signal (health endpoint, expected response)
- which response fields are non-deterministic and need normalizing

### Role 2 — run-time agent: observations → prose

Reads the *rendered result* and writes the plain-language summary and the LLM
findings. It never chooses what to execute. This keeps all non-determinism
downstream of the evidence, so the evidence is reproducible even when the
commentary varies.

It also emits an **examination record**: which assumptions it checked, which held,
which it could not settle. This feeds report sections 2 and 3 and is the cheapest
possible application of goal 1 — the agent already does this reasoning internally
and currently throws it away. Capturing it is a skill-design decision, not
engineering. The record is structured, not prose, so the report can render it
consistently.

### Where the run-time agent executes

Redline **never calls the Anthropic API itself.** The run-time pass executes
inside the invoking Claude Code session, which already holds whatever approval
exists in the environment. This keeps the binary at **zero network egress** —
materially easier to get through review than "local, except it also calls an API,"
and it removes API keys, rate limits, and model-version drift from Redline's
surface entirely.

Consequence: Redline's own output must be complete and useful with no LLM pass at
all. The prose layer is an enhancement over a deterministic artifact, never a
dependency of it.

### One instruction corpus

The run-time agent's review pass is steered by the same repo-resident instruction
files the rest of the ecosystem reads — `REVIEW.md` / `CLAUDE.md` / `AGENTS.md`
(GitHub Copilot code review reads these too, alongside its own
`.github/copilot-instructions.md`). Redline defines no prompt-config channel of
its own: anything the repo teaches one reviewer, it has taught them all, and
review rules live in exactly one place.

### Explicitly forbidden to the agent

Writing checks. Writing SQL. Choosing severity. Deciding which files are
generated. Selecting fixture values. Each of these turns "the agent figured it
out" into "the tool said something different on Tuesday."

### Config schema

A closed set of slot categories — toolchain, paths, commands, runtime, database,
ui.routes, browser, normalize, discovery, hooks. If something does not fit one of
them it is not expressible — that is the point. When a new one is genuinely
needed, add a slot, not an extensibility mechanism.

```toml
schema_version = 1

[toolchain]                      # closed enums; an unknown value is an error
migrations  = "golang-migrate"   # | goose | atlas | none
queries     = "sqlc"             # | ent | handwritten
api_codegen = "ogen"             # | oapi-codegen | none
api_lint    = "vacuum"           # | none
ui_build    = "vite"             # | next | none

[paths]
migrations   = "db/migrations"
openapi_spec = "api/openapi.yaml"
api_handlers = "api/endpoints"
store_pkg    = "internal/store/postgres"
ui_root      = "web"
generated    = ["api/marketplace-servergen/**", "internal/store/gen/**", "web/dist/**"]

[commands]                       # fixed key set, repo-specific values
build            = "make build"
run              = "make run"
seed             = "make seed"
test             = "make test"
generate         = "make generate"
verify_generated = "make verify-generated"

[runtime]
api_url       = "http://localhost:8080"
ui_url        = "http://localhost:8080"
health        = "http://localhost:8080/healthz"
ready_timeout = "30s"
env           = { DATABASE_DSN = "$REDLINE_DSN", MARKETPLACE_MODE = "mock" }

[database]
engine  = "postgres"
version = "17"

[[ui.routes]]                    # agent-derived from the router
path       = "/products"
name       = "products"
needs_seed = true

[browser]                        # pinned for screenshot reproducibility
channel = "chrome-for-testing"
version = "131.0.6778.85"

[normalize]                      # agent-proposed from observed responses
json_fields = ["created_at", "updated_at", "id"]
uuid        = true
timestamps  = true

[discovery]                      # staleness detection
inputs      = ["go.mod", "Makefile", "package.json", "sqlc.yaml", "web/src/App.tsx"]
fingerprint = "sha256:…"

[hooks]                          # the one escape hatch
pre_bringup = ""
```

**Staleness.** When any file in `[discovery].inputs` changes, its fingerprint no
longer matches and Redline reports "config may be stale, re-run discovery" rather
than silently doing the wrong thing. This is what keeps the config from decaying.

## SQL pane execution model

The pane's job is not to show that a migration ran. It is to show what the
migration does that you did not ask it to do, and what it fails to do in
environments that are not fresh.

### Steps

```
1. detect     migration files in the diff (path + [toolchain].migrations)
2. baseline   throwaway Postgres container, always fresh, never reused
3. worlds     A: apply all migrations from scratch
              B: apply migrations at merge-base, then apply the new ones
4. seed       repo fixtures via [commands].seed
5. snapshot   pre-state: schema (catalogs) + rows for affected tables only
6. apply      run pending migrations with the real driver, instrumented
7. snapshot   post-state, same shape
8. down       apply the .down.sql, snapshot, diff against pre-state
9. emit       pane JSON
```

Three decisions:

**Always a fresh throwaway container.** Startup is a couple of seconds with a
pre-warmed image; a clean baseline is worth far more. It also discharges the
"no production data" non-goal by construction rather than by policy — the pane
structurally cannot reach anything real.

**Affected tables determined statically.** Parse the migration for touched
relations and snapshot only those. Whole-database row diffing does not scale and
produces noise from unrelated seed churn.

**Instrument the apply step.** Set `lock_timeout` and `statement_timeout`
aggressively, sample `pg_locks` during execution, and record timing. A migration
taking `ACCESS EXCLUSIVE` on a hot table is a deterministic finding requiring no
LLM, and it is the finding that pages someone at 2am. This is the pane earning its
keep even when the row diff is boring.

### The two-world diff

`schema_A ≠ schema_B` means a fresh install and a deployed environment end up with
different schemas. This is the pane's highest-value output.

The dominant cause is **editing an already-applied migration**. golang-migrate
stores only `version` and `dirty` in `schema_migrations` — unlike Flyway or
Liquibase, **it does not checksum applied migrations**. Editing
`000004_add_offers.up.sql` after it has shipped means fresh installs get the edit,
every existing environment never does, and nothing ever reports it. The divergence
is permanent and silent.

### Fixtures

Two clearly separated groups: the repo's own seed set for realism, and a small
generated adversarial set for coverage. The adversarial rows are derived by static
analysis of the pending migration — the 65-character string for a narrowing type
change, the NULL in a column going NOT NULL, the duplicate meeting a new unique
index, the FK orphan. Values come from Redline's generator, never from the agent.

v1 scope limit: adversarial values only in columns the migration directly touches,
cloning an existing seed row as the carrier where possible. Synthesizing rows
across a deep FK graph is its own project and is out of scope.

### Known gaps

- Migrations with application-side backfills (chunked backfill in Go, not SQL).
  The pane sees the schema change but not the data motion. Flag as "has an
  out-of-band component" rather than leaving a silent gap.
- Data lost in a narrowing type change cannot be restored by a down migration.
  That is expected, not a defect; the down check must distinguish structural
  restoration from data restoration.

## UI pane capture model

**go-rod, in-process, driving a pinned Chrome for Testing build.**

Delegating capture to the invoking agent's browser skill was considered and
rejected: it puts the agent in the hot path deciding what to navigate, which the
agent boundary forbids; it cannot drive the two-revision bring-up sequence; and it
does not yield a reliable CDP event stream for console and network capture.

The stronger reason is distribution. Every agent-facing browser tool —
`chrome-devtools-mcp` (Puppeteer + CDP), `playwright-mcp`, `puppeteer-mcp` — is a
Node package. Adopting one means npm at runtime, which contradicts goal 9. go-rod
and chromedp are the Go-native CDP options; go-rod wins on browser management,
which is the actual cost center. Console errors and failed requests (check 16) come
from subscribing to the same CDP domains `chrome-devtools-mcp` exposes.

**Pin the browser.** `[browser].version` fixes a Chrome for Testing build so
rendering is byte-identical across machines and over time. An unpinned browser can
auto-update between the base capture and the head capture, producing phantom
diffs. Pinning removes that class of failure for free.

**Browser acquisition, in order:** use a local Chrome/Chromium/Edge if the pinned
version is present; otherwise an explicit, consented `redline setup browser`
fetch — never a silent ~150MB download during a pre-push check; otherwise the UI
pane is a **dark sensor** and renders as "skipped — no browser."

**v1 does not diff screenshots.** It renders them side by side. Perceptual
comparison has a threshold that must be tuned blind, and antialiasing, font
hinting, and animation timing make it the largest false-positive source in the
catalog. Check 15 is therefore a *render*, serving legibility directly; check 16
is the finder, because console errors and failed requests need no threshold.
Perceptual diffing is deferred indefinitely, pending real flakiness data.

Determinism controls still apply to capture without diffing — fixed viewport,
fixed device pixel ratio, animations disabled, `prefers-reduced-motion`, fonts
settled before capture — because they make the side-by-side readable, not merely
diffable.

## Findings schema

Redline does not define a findings schema. It **adopts doctor's** (`doctor/types.go`,
`SchemaVersion = 1`) as the wire format and extends it additively.

Doctor is the incumbent: a shipped version constant, a `.gorefactor-lint-baseline.json`
cache format already on disk, and more accumulated judgment than a greenfield
design would get. Having doctor emit into a new Redline schema would be
backwards. Redline reimplements the JSON shape independently — a shared contract,
not shared internals.

Two properties of doctor's design make the fit better than mere deference:

- **Redline is a diff-based substrate, and doctor already has that concept.**
  `baseline.go` excludes diff-based substrates from baseline construction because
  "their findings are relative to the base by construction." Every Redline pane is
  diff-based by definition, so Redline registers as `redline/sql`,
  `redline/openapi`, `redline/ui`, `redline/cover`; `New` is always true and no
  baseline build is needed. Redline inherits the fingerprint apparatus by not
  needing it.
- **Doctor builds baselines in a detached git worktree, cached by base SHA** —
  the same two-worktree mechanism and caching strategy the execution model needs.
  Independent convergence is evidence the mechanism is right.

### Additive fields

```go
Anchor   *Anchor  `json:"anchor,omitempty"`   // pane-relative location; file:line often absent
Evidence []string `json:"evidence,omitempty"` // observation IDs backing the claim
Expected string   `json:"expected,omitempty"` // for surprise ranking
Observed string   `json:"observed,omitempty"`
Source   string   `json:"source,omitempty"`   // "deterministic" | "llm"
```

Doctor's existing `Substrate` field identifies the pane; no separate `Pane` field.

`Source` is load-bearing for goal 6. A reviewer must see at a glance which
findings are reproducible and which came from the LLM pass — without it, the
determinism guarantee is invisible at the point of use.

### Anchors and fingerprints

Doctor's fingerprint is `file + rule + NormalizeMessage(message)`. Many Redline
findings have no file: schema drift is a difference between two observed schemas.
Each pane therefore defines an anchor identity that substitutes for file — table
plus column for SQL, method plus path for OpenAPI, route plus region for UI — and
feeds the fingerprint in the same position.

Anchor identity must be **stable across runs**, because the fingerprint is the
join key for human comment state. This is the one piece of genuine design work in
the schema and it is per-pane, not global.

### Findings and comments are separate files

`findings.json` is regenerated every run and fully deterministic. `comments.json`
is human state — accept, dismiss, amend — keyed by fingerprint. Merging them means
every re-run clobbers the reviewer's decisions.

### Categories

Doctor's `CategoryAPI` means *undeclared Go exported-API changes*, which is a
different thing from an HTTP contract change. Redline adds distinct categories
rather than overloading it: `schema`, `contract`, `cover`, `ui`.

### Dark sensors

Doctor's `SubstrateStatus{ran|skipped|failed, Gating}` refuses to treat a gating
substrate that did not run as a pass. Redline is non-gating, so `GateOK` does not
apply — but the rendering obligation is the same principle and is adopted
verbatim: **a pane that did not run renders as "did not run," loudly.** If the
Postgres container fails to start, an empty SQL pane must never be readable as
"nothing to worry about." That misreading is the failure mode most likely to
destroy trust in the tool.

## Check catalog

Closed and versioned. New checks ship in the binary, not in prompts. Each is
gated on `[toolchain]`.

**Migrations (git-level — no database required)**
1. Diff modifies a migration file that exists at merge-base → silent divergence
2. New migration's version prefix already exists on `origin/main` → collision

**Migrations (execution)**
3. Two-world schema diff (fresh vs. incremental)
4. Migration fails under the real driver
5. Lock class, table rewrite, duration at size N
6. Up → down → up round trip
7. Rows violating newly added constraints

**Queries**
8. `sqlc vet` with db-prepare against the post-migration database
9. sqlc generation staleness (the missing counterpart to `verify-generated`)

**API contract**
10. Breaking-change diff via `libopenapi/what-changed`
11. vacuum, filtered to rules *newly* violated by this change
12. Spec vs. handler semantics (added enum value with no switch arm, etc.)
13. Observed response vs. declared spec

**Tests**
14. Diff coverage — added lines no test executes (not repo-wide coverage)

**UI**
15. Before/after screenshots per route, rendered side by side (a render, not a finder)
16. Console errors and failed requests captured during navigation

**Deferred — build only if the first sixteen prove out**
17. Diff-scoped mutation testing — lines executed but not asserted on
18. Blast radius — callers reaching changed functions, newly reachable, newly dead
19. Benchmarks on changed packages — allocs/op primarily; ns/op only past a wide margin

Checks 17–19 are expensive to build and to run, and 18 in particular is well
covered by general-purpose tools. They are listed for completeness, not planned.

## Build order

Incremental, and each rung ships something usable on its own. Infrastructure-heavy
rungs come late, after daily use has shown what is actually needed.

**Rung 1 — migration hygiene. No infrastructure at all.**
Checks 1 and 2: a migration file modified after it was merged, and a version prefix
that already exists upstream. Pure git and filename parsing — no database, no
container, no bring-up. Ships with the findings schema, one subcommand, one skill,
and markdown output. Days, not weeks, and it proves the whole loop end to end:
agent invokes tool, reads structured output, presents it.

**Rung 2 — static cross-tool checks. Still no runtime.**
sqlc generation staleness (9), `libopenapi/what-changed` (10), vacuum filtered to
newly-violated rules (11). All the same pattern — run a tool at two revisions,
set-diff the output. Closes three rows of the consistency matrix for the cost of
process invocation.

**Rung 3 — diff coverage (14).**
Map the Go cover profile onto added diff lines. Single revision; no base build
needed. This is the fix for a coverage gate that measures the wrong thing.

**Rung 4 — two-worktree harness.**
The first real infrastructure. Ships no value by itself; build it paired with
rung 5.

**Rung 5 — Postgres container, checks 3–8.**
The two-world schema diff, lock and rewrite observation, down-migration round trip,
`sqlc vet` db-prepare. The largest investment and where the real differentiation
lives.

**Rung 6 — browser and UI pane.**
`redline setup browser`, pinned Chrome for Testing, checks 15 and 16, and the HTML
report growing to carry screenshots.

**Deferred indefinitely.** Checks 17–19 and perceptual screenshot diffing. The
default is not to build them.

### What this defers

`redline.toml` and `redline init` are not needed until rung 4. Rungs 1–3 run off
convention plus command-line flags. That postpones the entire setup-time
agent-discovery mechanism — the most speculative part of the design — until there
is a tool in daily use that can say what it actually needs to know.

## Open questions

- **How rich does `report.html` need to be?** It has to carry sections 1–3 well
  enough to induce prequel's "review mode," not merely dump JSON legibly. Main risk
  to goal 1, and the cost of cutting the web app.
- **What shape is the examination record?** Structured enough to render
  consistently, loose enough that the agent will actually fill it in. Gets
  designed with the first skill, at rung 1.
- **Skill granularity** — one `redline` skill that knows the whole flow, or one
  per pane. Affects how much the agent has to hold in context to use it well.
- Whether doctor's api-change intent declarations should be surfaced in the
  OpenAPI pane (declared intent vs. observed diff mismatch) — this is another row
  of the consistency matrix and probably belongs there.
- Base revision for incremental re-review: merge-base is right for a first run,
  but "since the last Redline run" may be the better baseline for iterating.
- Whether expensive probes (mutation, benchmarks) are default-on or opt-in. Ties
  to whether Redline runs on save or once before push.

## Resolved

- **Fixture strategy** — seed set plus a statically generated adversarial set,
  rendered as two separate groups. See "Fixtures".
- **Does Redline bring the stack up?** — Yes. The reference stack makes it cheap,
  and half the catalog depends on it.
- **Pure-function-of-diff panes vs. observation** — observation. See "Execution
  model".
- **How bounded is the agent?** — a closed set of config slots at setup time,
  prose at run time, nothing else. See "The agent boundary".
- **Findings JSON schema** — adopt doctor's rather than defining one; extend
  additively. See "Findings schema". Remaining work is per-pane anchor identity,
  tracked below.
- **Screenshot capture** — go-rod in-process against a pinned Chrome for Testing
  build; side-by-side rendering, no perceptual diff in v1. See "UI pane capture
  model".
- **Where the LLM pass runs** — inside the invoking Claude Code session; Redline
  itself has zero network egress. See "Where the run-time agent executes".
- **Competitor or layer?** — neither. Redline is an internal tool, so overlap with
  CodeRabbit is a scope-cutting signal rather than a positioning problem. See
  "Prior art".
- **Product shape** — CLI subcommands plus agent skills, not an application. No
  server, no frontend, no interactive comment lifecycle. See "Shape".
- **Goal 5 scope** — resolved by the shape decision: the findings stream is the
  work queue, human state is a JSON file, no interactive surface in v1.
- **Does Redline own the plain-language summary?** — no. The agent driving it
  does. Redline emits evidence and deterministic findings only.

## To be written

- Per-pane anchor identity and fingerprint derivation
- Skill definitions: trigger conditions, invocation, output handling
- `report.html` structure
- Observation normalizer specification
