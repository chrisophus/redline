# Redline design

Status: active. Supersedes the earlier design (in git history). The largest
reversal since the last revision: the agent-review half of the tool is
dropped. This document records that decision, lists what it removes, and
plans what remains.

## The decision

Redline spent most of its effort packaging the agent's review: a packet of
facts for the agent to judge, reviewer adapters that ran a vendor's own
review command, a context brief that pre-gathered callers and docs, and a
Copilot-shaped posted review built from the agent's prose. Use showed that
none of this made the review better. Claude Code and Cursor read a change,
find its callers, and weigh it against the repo's rules at least as well on
their own as when Redline briefs them, and the orchestration cost real
maintenance: adapter timeouts, progress plumbing, a review JSON schema, an
ingest step, and a skill teaching the agent how to fill all of it in.

So Redline stops trying to improve or package agent review. What it keeps is
the half no agent does on its own: measurement. An agent will not run the
linters at two revisions and diff the output, run the tests under an
instrumented profile, apply migrations to a real database, or screenshot two
builds of the UI. Those produce facts a review needs and a model cannot
invent. Redline becomes the instrument; whoever is reviewing, human or
agent, supplies the judgment from their own skills.

One line captures the split: Redline observes, and it never composes,
solicits, or transports a model's judgment.

## What goes

The cut shipped as phase 1. What was deleted or reshaped:

- Reviewer adapters. `--with claude`, `--with cursor`, the adapter config in
  `reviewers.json`, the progress ticker, the timeout handling:
  `internal/reviewer/`, `cmd/redline/reviewers.go`.
- The context brief. `--brief` re-created the context pass agents already
  run for themselves: `internal/reviewer/brief.go`, `internal/packet/brief.go`,
  `cmd/redline/brief.go`.
- The review loop. `redline review` emitted a packet for the agent to judge
  and `redline ingest` merged the judgment back. With no judgment to merge,
  both subcommands retire, along with the review JSON schema (summary,
  actual, discrepancies, intent, surfaces, the file walkthrough) in
  `internal/packet/ingest.go`. `redline run` remains the one entry point.
- Instruction discovery. `internal/instructions/` collected the repo's
  Copilot instructions and Cursor rules to hand the agent. Agents read
  those files themselves.
- Graph threads. `internal/graph/` derived call threads through the changed
  code for the agent packet. Following a call graph is exactly what agents
  are good at unaided.
- The Copilot-shaped post. The posted review's agent summary, file
  walkthrough, and threaded discrepancies go. `redline post` stays, posting
  observed findings only; see "Posting and the gate" below.
- The agent UI walk. The skill sent the agent through changed journeys with
  agent-browser and ingest embedded the screenshots. The deterministic
  capture pane below is the UI story now; walking the app stays available
  to any agent without Redline's involvement.
- Most of the skill. `skills/redline/SKILL.md` was mainly a review
  curriculum. It shrinks to: run `redline run`, read the findings, do not
  re-report them, present the report URL, and say what went unexamined.
- Report sections that rendered agent output: "What the reviewers found"
  and the agent prose at the top of the page.

What stays untouched: target resolution and worktrees, `findings.json` and
its fingerprints, the migration and OpenAPI panes, generated-file
suppression, diff coverage from an existing profile, the coverage banner,
the markdown and HTML reports, the loopback server, and the click-a-line
comment loop. That loop carries a human reviewer's comments to the author's
agent, which is a transport for human judgment, so it survives the rule
above.

## The product after the cut

`redline run` observes a change (working tree, commit, range, branch, or
PR) and writes `findings.json`, `report.md`, and `report.html`. A human
opens the report. An agent reviewing the same change reads `findings.json`
so it spends its context on judgment instead of re-deriving measurements.
`redline post` publishes the observed findings to the PR for reviewers who
never leave GitHub, and a profile lets a merge gate read the result.

`findings.json` plus git is the whole machine interface. The packet gets no
successor: an agent that wants the diff runs git, and the evidence it should
not re-derive is in `findings.json`. It was built for a consumer that no
longer exists, so `internal/packet/` goes with it, except the generated-file
detection, which moves to where the panes and report can share it.

The CLI after phase 1: `run`, `open`, `serve --stop`, `post`. No subcommand
invokes a model, and no flag brings one back.

## Phase 2: lint, with nuance

Shipped. CI gates errors, so re-reporting them is noise; the signal is in
what CI does not surface. Three panes, all deterministic, all scoped to
the change:

- Lint delta. Run the repo's linters at merge-base and at head, in the two
  worktrees Redline already checks out, and report only the findings the
  change introduces. Also count the findings it resolves; a change that
  clears twelve warnings deserves the confirmation. Fingerprint on file,
  rule, and digit-normalized message rather than line, so moved code does
  not read as new violations; identical messages in one file are matched by
  count. A linter that is configured but absent, or that fails to run at
  either revision, reports as a pane that did not run, never as a clean
  delta.
- Suppression triage. Collect every silencing the diff itself adds:
  `//nolint`, `eslint-disable` in both forms, `@ts-ignore`,
  `@ts-expect-error`, `#noqa`, `#[allow(...)]`. Each renders with the rule
  it silences and the code beneath it. This is the highest-signal lint fact
  a diff carries: the author told the linter to be quiet, and the reviewer
  should see where. Alongside it, surface findings on changed lines at
  severities the config downgrades or CI ignores, and entries the change
  adds to a baseline or ignore file.
- Config drift. When the diff touches a lint config, name every rule it
  disables, downgrades, or newly excludes, and mark the lint delta as
  partly config-driven so a clean delta earned by turning a rule off reads
  as what it is.

An earlier revision of this plan had the agent judge each triaged item as
worth fixing, defensible, or noise. That judgment step is gone with the
rest of the agent half. Redline collects and labels; the reader judges.

Mechanics: detection is by the tools' own config files, and the runs use
their JSON output. An earlier revision of this plan preferred the Makefile
`lint` target as the interface; building it reversed that. A make target's
output has no structure to fingerprint, and the delta needs identity - the
same finding at two revisions - not text. The base side runs in the cached
detached worktree gitx already keeps per commit, which is the base-and-head
runner the coverage delta below reuses.

## Phase 3: coverage, measured

The shipped pane intersects an existing profile with the change's added
lines and is honest about absence and staleness. Six additions, in order:

- A measured mode. `--measure` runs the repo's test command with an
  instrumented profile at head, so the number describes the code under
  review instead of whatever run left a profile behind. Off by default: a
  pre-push tool that silently runs a suite needing a database or ten
  minutes is one nobody reaches for. The stale-profile path stays as the
  cheap default, with its provenance and staleness reported as today.
- Function-level findings. A new or wholly rewritten function that no test
  executes becomes a named finding anchored at its declaration. "34% of
  added lines" tells a reviewer to worry; "`ResolveTarget` is new and
  nothing executes it" tells them where.
- Coverage delta. Per-package percentage at base and at head, on the same
  two-worktree runner as the lint delta. A drop is a finding with the
  number in it.
- Error-path nuance. Uncovered added lines inside error-handling branches
  are called out separately from the rest of the gap. In Go that is the
  block following an error check; untested error handling is the classic
  gap the aggregate number hides.
- A second toolchain. The reference stack has a TypeScript UI, so the pane
  learns to read lcov and istanbul output the same way it reads Go
  profiles, including for the measured mode when the repo's test command
  produces one.
- Test-delta facts. Cheap observations from the diff alone: code changed in
  a package whose tests did not, a `t.Skip` or `.skip` the diff adds, test
  functions or assertions the diff deletes. Each is a fact on the report,
  severity info, for the reviewer to weigh.

Everywhere a number renders, the three states stay distinct: uncovered,
not coverable, and not measured. A missing profile still renders as nobody
measured, never as zero.

Deferred: per-test attribution, mapping changed lines to the specific tests
that execute them. It needs a profile per test and the cost is out of
proportion to the question; revisit if the function-level findings prove
too coarse.

## Phase 4: the rest of the evidence

Unchanged in substance from the previous revision, cheapest first:

- OpenAPI spec lint via vacuum, filtered to newly violated rules, next to
  the shipped breaking-change diff. sqlc generation staleness.
- Database. A throwaway Postgres container, fresh every run: apply pending
  migrations with the real driver, capture lock class, table rewrite, and
  timing; the two-world diff of from-scratch versus incremental schema;
  the down-migration round trip; rows that violate newly added
  constraints.
- UI capture. A pinned Chrome for Testing build driven via go-rod:
  before/after screenshots per changed route, console errors, and failed
  requests during navigation. No perceptual diffing. Browser acquisition
  stays explicit (`redline setup browser`); with no browser the pane
  reports "skipped: no browser". With the agent walk gone this is the only
  UI story, which raises its priority within this phase.
- Performance stays deferred entirely. With the agent half gone there is
  no cheap judgment-based stand-in; a measured pane gets built when use
  shows the need.

Configuration for the runtime panes (`redline.toml`: build, run, seed,
migrate, routes, normalizers) gets designed when the first runtime pane is
built, from what it actually needs.

## Posting and the gate

`redline post --pr N` survives the cut with its machinery intact and its
content narrowed. What it posts: line comments for observed findings whose
`file:line` is on the PR diff, the rest in the body, and a body that opens
with a one-line-per-pane summary table (migrations, contract, lint delta,
diff coverage, and what did not run) plus `--report-url`. The agent
summary and walkthrough sections go with the rest of the agent half.
Everything hard-won stays: event COMMENT only, idempotency per (PR, head
SHA) via hidden markers, findings off the diff riding in the body so one
stray line cannot 422 the review, refusal when the session is a different
PR, credentials via `gh`.

The `--profile` gate stays, re-keyed to observed findings: the YAML names
the markers, blocking severities fail, info does not. `fail_closed_reviewer`
retires with the reviewers; in its place the profile may fail closed on a
pane that applied but did not run, which is the same principle, that a
missing check must not read as a pass.

The check-run question from the previous revision stays open, and matters
more now: "migrations applied cleanly, lint delta +0, diff coverage 84%"
in the merge box is the natural delivery for evidence, where a comment
thread was the natural delivery for prose.

## The report page

The page keeps its shape minus the agent sections. Orientation shrinks to
the PR title and body, and the Jira ticket when the fetcher gets built; it
stays worth building as header context, at low priority, since the
intent-versus-change comparison it fed is the reviewing agent's job now.
The evidence panes fill the rest: screens, contract, schema, coverage,
lint, and the observed findings. Viewed state and click-a-line comments
stay, exported as JSON for the author's agent as today.

Distribution is unchanged: one self-contained HTML file, served over
loopback locally, published as a CI artifact per PR when a team wants a
shared page. A hosted server with live comment state remains the fallback
if artifacts prove too clunky.

## Standing decisions

Carried forward: stable fingerprints as the join key for comment state and
posting; findings and human comment state in separate files; fresh
throwaway containers; pinned browser builds; confirmations kept and
collapsed; every exclusion named; a pane that applies but cannot run
reports loudly.

Retired as moot: appending rather than merging reviewer findings, agents
being allowed to explore beyond the packet, and adapter extension via
`reviewers.json`. The old rule "no direct model API calls" strengthens to:
Redline never invokes a model by any route.

## Order of work

1. Done. The cut: delete the packages and subcommands listed above, reshape
   post and the report, rewrite the skill and README.
2. Done. Lint delta, suppression triage, config drift, on the two-revision
   runner.
3. Coverage: measured mode, function-level findings, coverage delta,
   error-path split, lcov, test-delta facts.
4. vacuum and sqlc staleness, then the database pane, then UI capture.
5. The check-run, and the Jira header when it earns its slot.

## Open questions

- The base revision for the delta runs when merge-base does not build or
  lint cleanly enough to compare. Likely answer: report the base run's
  failure as its own fact and degrade to head-only.
- How much toolchain detection to attempt before requiring `redline.toml`.
  The reference stack's Makefile convention has held so far.
- Where the Jira credential lives when the fetcher gets built. Same answer
  as `gh`: a CLI or env token Redline never stores.
