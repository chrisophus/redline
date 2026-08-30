# Redline — Design Doc

Status: active. This version supersedes the earlier design (in git history),
which framed Redline around a determinism thesis and a strict agent boundary.
Redline is not a bet about how agentic review should work. It is a tool to
make reviewing pull requests easier, built for use, and the design keeps
exactly the machinery that serves that.

## What it is

Redline reviews a change — the working tree, a commit, a range, a branch, or
a GitHub PR — by combining two kinds of output in one report:

- **Observed evidence**: things Redline checked itself and can show — a
  migration edited after it merged, a schema diff, a screenshot, a coverage
  number.
- **An agent's review**: Claude Code, Cursor, or any reviewer with a CLI,
  reading the change and reporting bugs, security issues, and
  inconsistencies.

Both land in one findings file and one report, labelled so a reader can tell
which is which.

## The three jobs, in build order

**1. Reviews on GitHub you can verify happened.** Replace Copilot code
review: an agent reviews the PR and Redline posts the result to GitHub. The
part no free tool provides is the audit line — the posted review leads with
what was checked and what was not ("reviewed by claude; lint delta, diff
coverage, and UI not checked"), and links the full report. "It has review
comments" and "it was reviewed" stop being the same claim.

**2. Reviewing someone else's PR with evidence.** One command that shows the
change in its own domain: UI before/after, API contract diff, what the
migrations actually do to a database, lint and test-coverage deltas, and the
agent's read on architecture. This is the part nobody ships for free —
nobody else stands the app up at two revisions.

**3. A review page for the team.** The report grows into something anyone
can open for any PR: the Jira ticket's intent and the PR description beside
what the change actually does, screenshots, sample rows from the database,
and inline comments that come back to the author's agent. Hosted the cheap
way first — the report is a single self-contained HTML file, so CI can
publish it as a build artifact per PR. A server only if that proves
insufficient.

Job 1 is days of work on top of what exists. Job 2 is the real investment.
Job 3 is rendering over the same JSON and starves without job 2's evidence,
so it goes last — except the Jira fetcher, which is small, independent, and
improves job 1's reviews immediately ("the PR claims JIRA-123's fix but
never touches the code path the ticket names").

## What Redline provides

The tool earns its keep with four mundane pieces:

- **Target resolution and worktrees.** Point it at a PR number, a branch, a
  commit, a range, or nothing (the working tree, uncommitted work included)
  and get a checkout to review that never disturbs your own. Non-worktree
  targets materialize as detached git worktrees, cached by commit SHA under
  `~/.redline/worktrees/`. PR metadata comes through `gh`, so Redline never
  handles a token.

- **One findings file.** Everything — Redline's own checks, each reviewer's
  findings, future panes — lands in one JSON shape (`findings.json`,
  doctor-compatible, schema version 1) that one renderer and one GitHub
  poster consume. Adding a source of findings never means adding a consumer.

- **The report.** `report.md` for terminals; `report.html` as one
  self-contained file — no server, no network, no framework. Summary, a
  file-by-file walkthrough, per-area drill-ins with syntax-marked diffs,
  screenshots inlined as data URIs, per-file viewed state, and click-a-line
  comments exported as JSON for the agent. Served over loopback HTTP
  (`redline open`) because editor webviews reject `file://`.

- **The honesty line.** Every report states what was checked and what was
  not: `coverage{changedFiles, examinedFiles, unexamined[]}`, a banner when
  coverage is hollow, and an explicit note when a check that exists in the
  catalog did not run. This is cheap to keep and it is the audit trail job 1
  is sold on. A report that silently covers none of a change reads exactly
  like one that covers all of it, and the reader takes the flattering
  reading.

One rule survives from the old design on pure utility grounds: **observed
findings and model findings stay labelled** (`source: "deterministic" |
"llm"`, plus `reviewer` when an external tool produced it). Not for purity —
because when the report says "this migration was edited after it merged," a
teammate should trust it without wondering whether a model made it up. One
field, already built, zero ongoing cost.

## What Redline does not do

- **Gate merges.** It reports; it never blocks. (A CI gate could consume
  `findings.json` later; that is a separate, thin thing.)
- **Touch production data.** Database checks run against a throwaway
  container with seeded or fake data, by construction.
- **Reimplement what exists.** Linters, `sqlc vet`, OpenAPI diff engines
  (`pb33f/libopenapi` what-changed), schema diffing (`stripe/pg-schema-diff`)
  are consumed, not rebuilt. Feature overlap with free general-purpose
  reviewers (prose summaries, call-graph diagrams) is a reason to cut scope,
  not compete.
- **Ship a plugin API.** New checks are code in the binary. Reviewer
  adapters are the extension point (`reviewers.json`), and they are
  configuration, not code.
- **Call model APIs directly.** Agents run as CLIs Redline invokes (or as
  the session driving it). No API keys, rate limits, or model-version drift
  in Redline's surface. If a check someday needs a direct API path, that is
  a new decision, not a default.

Superseded from the earlier design:

- ~~**Never posts to GitHub.**~~ Job 1 requires posting. The rule becomes:
  read-only by default; posting is a separate, explicit subcommand
  (`redline post`), never a side effect of reviewing.
- ~~**Strict agent boundary / packet as the product's spine.**~~ Agents may
  explore the repo; Claude Code and Cursor are good at it, and a fixed
  evidence bundle is a floor, not a cage. The packet remains as the data
  source for the report's drill-ins and as a convenience for skill-driven
  reviews — it is no longer load-bearing doctrine. Slightly different
  reviews on re-run are acceptable; observed checks stay reproducible
  because they are ordinary deterministic code, not because a boundary
  enforces it.
- ~~**Determinism as the organizing thesis.**~~ Kept where it is free
  (labels, coverage honesty, stable fingerprints), dropped where it is
  ceremony (packet curation guarantees, guidance-brief versioning arguments,
  ingest target-matching as a design pillar).

## Current state

Built and working today:

- Migration hygiene checks: a migration modified or deleted after it exists
  at merge-base (golang-migrate records versions, not contents, so an edit
  silently diverges every already-migrated database from every fresh one),
  and a new migration whose version prefix already exists upstream.
- The review loop: `redline review` emits the change as JSON for an agent,
  `redline ingest` merges the agent's review back, labelled.
- Reviewer adapters: `--with claude` runs Claude Code's own `/code-review`
  in the reviewed tree and folds its findings in; `--with cursor` likewise.
  Adapters are config entries (command, prompt template, timeout) — a failed
  reviewer is recorded as "did not run", never as "found nothing."
- The report: markdown + self-contained HTML with diffs, comments, viewed
  state, screenshots; loopback server with identity checks.

## Job 1: post to GitHub (next)

`redline post --pr N` takes the current session's findings and posts one PR
review via `gh api`:

- Line-anchored comments for findings that carry file:line; the rest in the
  review body.
- The body leads with the audit line (who reviewed, what was checked, what
  was not) and links the full report (CI artifact URL when available).
- Severity maps to nothing gating — the review is COMMENT, not
  REQUEST_CHANGES, unless asked.
- Idempotent per (PR, head SHA): re-posting updates rather than duplicates.
- Posting is never implicit. `review`/`run` stay read-only.

Alongside it, the Jira fetcher: given a ticket key (from the branch name, PR
title, or a flag), pull the ticket summary/description into the review input
and the report header as **intent**, so both the agent and the reader can
compare claim against change.

## Job 2: the evidence panes

The backlog, roughly cheapest first. Each pane emits into the same findings
file and renders a section of the report; a pane that applies but cannot run
reports "did not run" loudly.

**No runtime needed:**
- Lint delta — run the repo's linters at base and head, set-diff, report
  only what this change introduces.
- Diff coverage — map the test cover profile onto added lines: which new
  lines no test executes. Per-change, not the repo-wide number.
- OpenAPI breaking-change diff via `libopenapi` what-changed; spec lint
  (vacuum) filtered to newly violated rules.
- sqlc generation staleness.

**Database (throwaway Postgres container, always fresh):**
- Apply pending migrations with the real driver; snapshot schema and
  affected-table rows before/after; capture lock class, table rewrite, and
  timing (the finding that pages someone at 2am needs no model).
- Two-world diff: migrations from scratch vs. applied incrementally at
  merge-base — divergence here means fresh installs and deployed
  environments end up with different schemas, and nothing else ever reports
  it.
- Down-migration round trip; rows violating newly added constraints (seed
  data plus a small generated adversarial set — the NULL going NOT NULL, the
  65-char string into the narrowed column).
- These sample rows are also job 3's "show me the data" section.

**UI (pinned Chrome for Testing, driven in-process via go-rod):**
- Before/after screenshots per changed route, side by side. No perceptual
  diffing — it is the largest false-positive source in the catalog; the
  render serves the reviewer directly.
- Console errors and failed network requests during navigation — those need
  no threshold and are real findings.
- Browser acquisition is explicit and consented (`redline setup browser`),
  never a silent 150MB download mid-review; no browser means the pane
  reports "skipped — no browser."

**Deferred: performance.** Benchmarks on changed packages are expensive and
noisy; "no major degradation" starts as an agent judgment over the diff, and
a measured pane gets built only if that proves insufficient in use.

Observations are normalized before diffing (timestamps, UUIDs, row order) —
un-normalized observation diffing is 100% false positives on the first run,
which is how a tool like this dies.

Configuration for the runtime panes (`redline.toml`: how to build, run,
seed, migrate; routes; normalizers) gets designed when the first runtime
pane is built, from what it actually needs — not before. The checks built so
far run off convention and flags, and that has held.

## Job 3: the review page

The existing `report.html` is the seed. It grows:

- **Intent vs. actual**, at the top: the Jira ticket and PR description on
  one side, the agent's account of what the change actually does on the
  other, with mismatches called out.
- Screenshots and database sample rows from job 2's panes.
- Lint/coverage/perf status at a glance.
- The existing affordances kept: file walkthrough with findings-per-file,
  viewed checkboxes, click-a-line comments handed back to the agent.

Distribution: CI uploads the report as a per-PR artifact (or static page).
Comments in that mode export as JSON the author pastes to their agent, as
they do today. A hosted server with live comment state is the fallback plan
if artifact-hosting proves too clunky — a decision to make from use.

## Reference stack

Redline is designed against a specific project shape, which breaks ties: Go
service, TypeScript/React UI, Postgres, golang-migrate, sqlc, ogen from an
OpenAPI 3 spec, a Makefile as the de-facto interface (`build`, `run`,
`seed`, `test`, `generate`), and a mock/offline mode so the stack starts
without cloud credentials. Two properties are load-bearing for job 2: the
Makefile is already a bring-up contract, and mock adapters make standing the
stack up cheap enough to do twice per run.

## Practical decisions kept, with reasons

- **Fresh throwaway container, every run.** A couple of seconds of startup
  buys a clean baseline and makes "no production data" true by construction.
- **Findings and human comment state in separate files.** `findings.json` is
  regenerated every run; reviewer accept/dismiss state is keyed by
  fingerprint elsewhere. Merged, every re-run clobbers the reviewer's
  decisions.
- **Stable fingerprints** (location-or-anchor + rule + digit-normalized
  message) so a finding keeps its identity across runs — the join key for
  comment state and for idempotent GitHub posting.
- **Reviewer findings are appended, not merged across reviewers.** Guessing
  that two differently worded findings are one defect deletes a finding
  silently; exact duplicates already collapse on fingerprint. Two reviewers
  reporting the same thing is signal, not noise.
- **Pinned browser version** for screenshots — an unpinned browser can
  update between the base and head captures and produce phantom diffs.
- **Confirmations are kept, collapsed.** "Applied against 50k rows, no table
  rewrite" is a question the reviewer no longer has to ask; discarding it
  throws away most of the value of running the check.

## Open questions

- How far artifact-hosted reports get before job 3 needs real comment
  persistence (and therefore a server).
- Where the Jira credential lives (likely: the `jira` CLI or an env token,
  same posture as `gh` — Redline never stores it).
- Whether `redline post` should also update a check-run so "reviewed" is
  visible in the merge box, not just in a comment.
- Base revision for re-review: merge-base is right for a first pass; "since
  the last Redline run" may be better when iterating.
