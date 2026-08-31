# Redline design

Status: active. Supersedes the earlier design (in git history); the
decisions reversed from it are listed under "Reversed decisions" below.

## What it is

Redline reviews a change: the working tree, a commit, a range, a branch, or
a GitHub PR. It combines two kinds of output in one report:

- Observed evidence: things Redline checked itself and can show. A
  migration edited after it merged, a schema diff, a screenshot, a coverage
  number.
- An agent's review: Claude Code, Cursor, or any reviewer with a CLI,
  reading the change and reporting bugs, security issues, and
  inconsistencies.

Both land in one findings file and one report, labelled so a reader can tell
which is which.

## The three phases, in build order

1. Reviews on GitHub you can verify happened. Replace Copilot code review:
an agent reviews the PR and Redline posts the result to GitHub. The part no
free tool provides is the opening line that says what was checked and what
was not ("reviewed by claude; lint delta, diff coverage, and UI not
checked"), with a link to the full report. Review comments by themselves say
nothing about what was examined.

2. Reviewing someone else's PR with evidence. One command that shows the
change in its own domain: UI before/after, API contract diff, what the
migrations do to a real database, lint and test-coverage deltas, and the
agent's read on architecture. No free tool does this, because no free tool
stands the app up at two revisions.

3. A review page for the team. The report grows into a page anyone can open
for any PR: the Jira ticket and the PR description beside what the change
actually does, screenshots, sample rows from the database, and inline
comments that come back to the author's agent. Host it the cheap way first:
the report is a single self-contained HTML file, so CI can publish it as a
build artifact per PR. Build a server only if that proves insufficient.

Phase 1 is days of work on top of what exists. Phase 2 is the real
investment. Phase 3 renders phase 2's evidence and starves without it, so it
goes last. The one exception is the Jira fetcher: it is small, independent,
and improves phase 1 reviews right away ("the PR claims the JIRA-123 fix but
never touches the code path the ticket names").

## What Redline provides

- Target resolution and worktrees. Point it at a PR number, a branch, a
  commit, a range, or nothing (the working tree, uncommitted work included)
  and get a checkout to review that never disturbs your own. Targets other
  than the working tree are checked out as detached git worktrees, cached
  by commit SHA under `~/.redline/worktrees/`. PR metadata comes through
  `gh`, so Redline never handles a token.

- One findings file. Redline's own checks, each reviewer's findings, and
  every future pane land in one JSON shape (`findings.json`,
  doctor-compatible, schema version 1) that one renderer and one GitHub
  poster consume. Adding a source of findings never means adding a
  consumer.

- The report. `report.md` for terminals. `report.html` as one
  self-contained file that needs no server, network, or framework. It
  carries a summary, a file-by-file walkthrough, per-area drill-ins with
  marked-up diffs, screenshots inlined as data URIs, per-file viewed state,
  and click-a-line comments exported as JSON for the agent. It is served
  over loopback HTTP (`redline open`) because editor webviews reject
  `file://`.

- Coverage. Every report states what was checked and what was not:
  `coverage{changedFiles, examinedFiles, unexamined[]}`, a banner when
  coverage is hollow, and a note when a check that exists in the catalog
  did not run. Phase 1's verification depends on this. A report that
  silently covers none of a change reads exactly like one that covers all
  of it, and the reader assumes the flattering reading.

- Source labels. Observed findings and model findings are labelled
  (`source: "deterministic" | "llm"`, plus `reviewer` when an external tool
  produced it). When the report says "this migration was edited after it
  merged," a teammate can trust it without wondering whether a model made
  it up.

## What Redline does not do

- Gate merges. It reports and never blocks. (A CI gate could consume
  `findings.json` later as a separate, thin thing.)
- Touch production data. Database checks run against a throwaway container
  with seeded or fake data, so there is nothing real to reach.
- Reimplement what exists. Linters, `sqlc vet`, OpenAPI diff engines
  (`pb33f/libopenapi` what-changed), and schema diffing
  (`stripe/pg-schema-diff`) are consumed as they are. When a free
  general-purpose reviewer already ships a feature (prose summaries,
  call-graph diagrams), that is a reason to cut it from scope.
- Ship a plugin API. New checks are code in the binary. The extension point
  is reviewer adapters, which are config entries in `reviewers.json`.
- Call model APIs directly. Agents run as CLIs Redline invokes, or as the
  session driving it. This keeps API keys, rate limits, and model-version
  drift out of Redline's surface. If a check someday needs a direct API
  path, that is a new decision.

## Reversed decisions

- Posting to GitHub was forbidden. Now it is phase 1. The default stays
  read-only; posting is a separate, explicit subcommand (`redline post`)
  and never a side effect of reviewing.
- Agents were confined to the packet. Now they may explore the repo, which
  Claude Code and Cursor are good at. The packet remains as the data source
  for the report's drill-ins and as input for skill-driven reviews.
  Slightly different reviews on re-run are acceptable; observed checks stay
  reproducible because they are ordinary deterministic code.

## Current state

- Migration hygiene checks: a migration modified or deleted after it exists
  at merge-base (golang-migrate records versions rather than contents, so
  an edit silently diverges every already-migrated database from every
  fresh one), and a new migration whose version prefix already exists
  upstream.
- The review loop: `redline review` emits the change as JSON for an agent;
  `redline ingest` merges the agent's review back, labelled.
- Posting: `redline post --pr N` submits the session's findings as one PR
  review (event COMMENT), led by the agent's summary of the change and the
  file-by-file walkthrough. A finding becomes a line comment only when its line
  is in the PR's diff (fetched from the files API); findings off the diff go in
  the body, so the all-or-nothing review API can never 422 on one stray line.
  It refuses unless the loaded session is that PR, is idempotent per
  (PR, head SHA) via hidden markers that name both, and posts through `gh` so
  Redline never handles a token. `run`/`review`/`ingest` stay read-only.
- Reviewer adapters: `--with claude` runs Claude Code's own `/code-review`
  in the reviewed tree and folds its findings in; `--with cursor` likewise.
  Adapters are config entries (command, prompt template, timeout). A
  reviewer that fails is recorded as "did not run" so it can never read as
  "found nothing."
- The report: markdown plus self-contained HTML with diffs, comments,
  viewed state, and screenshots, and a loopback server that verifies it is
  serving the right directory.

## Phase 1: post to GitHub

Replace Copilot review: one command reviews a teammate's PR with an agent
and posts the result to GitHub, with proof of what was and wasn't checked.
The review pipeline already works (`redline review --pr N --with claude`
checks the PR out, runs the agent, and merges labelled findings). Phase 1
was three pieces on top of it; the first two are the core, and shipped.

1. **Shipped.** `redline post --pr N` takes the current session's findings
and posts one PR review via `gh api`:

- Findings that carry file:line become line-anchored comments; the rest go
  in the review body.
- The review is posted as COMMENT. It never requests changes or approves.
- Posting is idempotent per (PR, head SHA): every finding carries a hidden
  marker naming the commit it was said for and its fingerprint, so a re-post
  never duplicates one and a re-run with no new findings on an already-reviewed
  commit posts nothing. The commit has to be in the key. The fingerprint is
  file, rule and normalized message, with no commit in it, while GitHub keeps
  every comment ever left on the pull request, so keying on the fingerprint
  alone made a finding that survived a push look already-posted against a
  comment attached to the commit before it. GitHub collapses that comment as
  outdated, so the defect ended up with nothing visible on the current diff.
- Findings that ride in the body are marked the same way. Tracking only the
  line comments meant a later ingest whose finding had no line produced no new
  comments, which read as nothing to post.
- Posting is never implicit. `review` and `run` stay read-only, and `post`
  refuses when the session's target is a different PR than the one named.

2. **Shipped.** The review body opens with the agent's summary of the change
and the file-by-file walkthrough, with a link to the full report
(`--report-url`, a CI artifact URL when available). An earlier version led with
coverage instead: which panes ran and what share of the changed files they
examined. On a change with no migrations and no spec that rendered as "none of
the 16 changed file(s) were examined", which reads as an apology and buries the
review under it. Coverage is still in `findings.json` and still on the report;
it is not what belongs at the top of a review.

Still open on this phase:

- Whether `post` also updates a check-run, which puts "reviewed" in the
  merge box instead of buried in comments and is the cheapest form of the
  verification this phase exists for. Not built.
- The Jira fetcher: resolve a ticket key from the branch name, the PR
  title, or a flag, then pull the ticket summary and description into the
  review input and the report header as intent, so the agent and the reader
  can compare claim against change from day one. Not built.

Done means: a teammate's PR goes through one command and a review appears
on GitHub that a reader can act on and can verify the scope of. That path
works today; the check-run and Jira pieces sharpen it. Everything else,
including the ignored-lint triage, diff coverage, and the formalized agent
UI walk below, is phase 2.

## Phase 2: the evidence panes

The backlog, roughly cheapest first. Each pane emits into the same findings
file and renders a section of the report. A pane that applies but cannot
run reports "did not run" loudly.

### Checks that need no runtime

- Lint delta: run the repo's linters at base and head, diff the two sets,
  and report only what this change introduces.
- Ignored-lint triage: collect the lint output nobody looks at (info-level
  findings, rules the config downgrades, baseline-suppressed findings,
  scoped to changed lines) plus any suppression directives the diff itself
  adds (`//nolint`, `eslint-disable`). The agent judges each one as worth
  fixing here, defensible, or noise, and promotes the few that matter as
  labelled model findings citing the lint rule. The collection step is
  deterministic and only the judgment comes from the agent. A suppression
  added by the diff is the high-signal case: the author explicitly told the
  linter to be quiet, and someone should check whether that was justified.
- Diff coverage: run the tests with a cover profile and map it onto the
  lines this change adds or modifies, to find new code no test executes.
  This is measured per change because the repo-wide number can look healthy
  while an entire new function ships untested. Findings name the untested
  behaviour and anchor to file:line; the per-file covered/uncovered split
  renders in the walkthrough.
- OpenAPI breaking-change diff via `libopenapi` what-changed, and spec lint
  (vacuum) filtered to newly violated rules.
- sqlc generation staleness.

### Database

A throwaway Postgres container, fresh every run.

- Apply pending migrations with the real driver. Snapshot the schema and
  the affected tables' rows before and after. Capture lock class, table
  rewrite, and timing; the migration that pages someone at 2am needs no
  model to spot.
- Two-world diff: apply the migrations from scratch and also incrementally
  on top of merge-base. If the two schemas differ, fresh installs and
  deployed environments end up different, and nothing else ever reports
  that.
- Down-migration round trip, and rows that violate newly added constraints
  (the repo's seed data plus a small generated adversarial set: the NULL
  going NOT NULL, the 65-char string into the narrowed column).
- These sample rows also feed phase 3's "show me the data" section.

### Agent UI walk

Partly exists; make it first-class.

- The skill already sends the agent through the changed journeys with
  agent-browser when the diff touches UI: open, interact, screenshot, check
  console errors, and judge correctness and experience. The walk is scoped
  to the diff and never crawls the whole app. Screenshots ingest into the
  report today.
- The missing half is setup. Redline should stand the app up (or find the
  running dev server) and hand the agent the URL and the routes the diff
  touches, so every walk starts from the same place instead of the agent
  re-deriving bring-up each time.
- The walk assesses one revision and is labelled as the agent's work. The
  capture pane below compares two revisions, so the two do not overlap. The
  walk ships first because it needs no browser infrastructure of Redline's
  own.

### UI capture

A pinned Chrome for Testing build, driven in-process via go-rod.

- Before/after screenshots per changed route, side by side. No perceptual
  diffing: it is the largest false-positive source in the catalog, and the
  side-by-side render serves the reviewer directly.
- Console errors and failed network requests during navigation. Those need
  no threshold and are real findings.
- Browser acquisition is explicit: `redline setup browser` downloads the
  pinned build with consent, so a review never triggers a silent 150MB
  download. With no browser present the pane reports "skipped: no browser."

### Deferred: performance

Benchmarks on changed packages are expensive and noisy. "No major
degradation" starts as an agent judgment over the diff, and a measured pane
gets built only if that proves insufficient in use.

Observations are normalized before diffing (timestamps, UUIDs, row order).
Diffing without normalizing produces 100% false positives on the first run,
which is how a tool like this dies.

Configuration for the runtime panes (`redline.toml`: how to build, run,
seed, and migrate; routes; normalizers) gets designed when the first
runtime pane is built, from what it actually needs. The checks built so far
run off convention and flags, and that has held.

## Phase 3: the review page

The existing `report.html` is the seed. It grows:

- Intent beside outcome, at the top: the Jira ticket and PR description on
  one side, the agent's account of what the change actually does on the
  other, with mismatches called out.
- Screenshots and database sample rows from phase 2's panes.
- Lint, coverage, and performance status at a glance.
- The current features stay: the file walkthrough with findings per file,
  viewed checkboxes, and click-a-line comments handed back to the agent.

Distribution: CI uploads the report as a per-PR artifact (or static page).
Comments in that mode export as JSON the author pastes to their agent, as
they do today. If artifact hosting proves too clunky, the fallback is a
hosted server with live comment state. Decide that from use.

## Reference stack

Redline is designed against a specific project shape, which breaks ties: a
Go service, a TypeScript/React UI, Postgres, golang-migrate, sqlc, ogen
from an OpenAPI 3 spec, a Makefile as the interface everyone already uses
(`build`, `run`, `seed`, `test`, `generate`), and a mock/offline mode so
the stack starts without cloud credentials. Two properties matter for
phase 2: the Makefile already documents bring-up, and mock adapters make
standing the stack up cheap enough to do twice per run.

## Standing decisions

- Fresh throwaway container, every run. A couple of seconds of startup
  buys a clean baseline, and it keeps "no production data" true because
  the container cannot reach any.
- Findings and human comment state live in separate files. `findings.json`
  is regenerated every run; reviewer accept/dismiss state is keyed by
  fingerprint elsewhere. If they were merged, every re-run would clobber
  the reviewer's decisions.
- Stable fingerprints (location or anchor, plus rule, plus digit-normalized
  message) so a finding keeps its identity across runs. The fingerprint is
  the join key for comment state and for idempotent GitHub posting.
- Reviewer findings are appended rather than merged across reviewers.
  Guessing that two differently worded findings are one defect deletes a
  finding silently, and exact duplicates already collapse on fingerprint.
  Two reviewers reporting the same thing is a confidence signal.
- Pinned browser version for screenshots. An unpinned browser can update
  between the base and head captures and produce phantom diffs.
- Confirmations are kept, collapsed. "Applied against 50k rows, no table
  rewrite" is a question the reviewer no longer has to ask, and discarding
  it throws away most of the value of running the check.

## Open questions

- How far artifact-hosted reports get before phase 3 needs real comment
  persistence, and therefore a server.
- Where the Jira credential lives. Likely the `jira` CLI or an env token,
  handled the same way as `gh`: Redline never stores it.
- The base revision for re-review. Merge-base is right for a first pass;
  "since the last Redline run" may be better when iterating.
