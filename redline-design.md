# Redline design

Status: active. Supersedes the earlier design (in git history). Three things
changed since the last revision. The framing: Redline is the detail behind
the gates, and this document now says so first. The agent: an earlier
revision cut the agent from the tool; this one keeps that cut and records how
the agent came back, as a reviewer rather than as something Redline runs. And
the review producer: one command now calls a model, which narrows a rule this
document had stated absolutely, so the narrowing is argued rather than
assumed.

## The framing

A repository's harness is a row of gates. Tests pass or fail, coverage
clears a threshold or does not, the linter is quiet or CI is red. Each gate
compresses everything it measured into one bit, and that is the right shape
for a merge queue. It is the wrong shape for a reviewer, because the
compression discards what a decision needs: which added lines nothing
executes, which lint findings the change introduced rather than inherited,
where the author silenced a rule, and whether a number moved and which way.

Redline is the detail the gates threw away, plus the facts no gate computes.
It measures a change and keeps what it measured at the resolution a reviewer
reads, and it names what it could not measure so that an absent check never
reads as a pass.

The facts fall in three kinds:

- Deltas. A gate judges a snapshot against a threshold; a reviewer wants to
  know what the change did. A change that takes coverage from 90 to 85
  passes an 80 percent gate, and a change that adds a dozen uncovered error
  branches passes a total-coverage gate. The lint delta and the coverage
  delta exist because the interesting fact is the movement.
- Details. The lines behind the number: the uncovered added lines, the
  findings on changed lines, the suppression directives the diff adds, the
  rules a config change turns off.
- Facts with no gate. Most repositories gate nothing about migrations or
  API contracts. The migration and OpenAPI panes compute what no harness
  ever looked at.

## Who judges

Redline measures and never judges. The judgment belongs to whoever is
reviewing, and that is one of two readers on equal footing: a person in
the browser, or an agent reading `findings.json`. They see the same facts
and write their decisions onto the same report. Every finding and every
ruling carries its source, a pane or the reviewer, and the report shows
that source without a warning attached.

The rule that survived every revision until now: the redline binary never
invokes a model, by any route. That rule has been narrowed, and the
narrowing is argued in "The review producer" below. It now reads: `redline
run` never invokes a model, by any route, and `redline review` is the only
command that does. Everything else is unchanged. The agent's review still
reaches the report through a file it writes (`review.json`), the reviewer's
requests still reach the agent through a payload a person copies out of the
page, and a repository with no API key still gets the whole report.

## What was cut, and what came back

The revision before this one removed everything Redline did to package
the agent's review: reviewer adapters that ran a vendor's review command,
a packet of facts for the agent to judge, a context brief that pre-gathered
callers and docs, a review-and-ingest loop with its own JSON schema,
instruction discovery, call-graph threads, the agent UI walk, and a
Copilot-shaped posted review built from the agent's prose. Use showed that
none of it made the review better than Claude Code or Cursor produce on
their own, and the orchestration cost real maintenance. All of that stays
gone, and `run` remains model-free. What a later revision added in its place
is one command, argued in "The review producer" below.

What came back is narrower and sits on the other side of the boundary.
The agent reviews the change with its own tools, writes `review.json`, and
Redline renders it: an overview at the top of the report, a one-line
summary per file, line comments that sit in the drawer as findings marked
`source: llm`, and verdicts on the suppressions Redline found. The report
lets a person ask the agent to explain or change a line, and those
requests ride the same copy-for-the-agent payload the click-a-line comments
already used. None of this asks Redline to run anything. It asks Redline to
carry both reviewers' decisions onto the facts, which is the job the
framing gives it.

## The review producer

This section records a reversal. The revision before last cut everything
Redline did to package an agent's review, and gave a reason: none of it made
reviews better than Claude Code or Cursor produce on their own, and the
orchestration cost real maintenance. That reasoning still holds for what it
was aimed at. `redline review` is a different thing, and the difference is
worth stating plainly, because a reader who finds a model call in a tool whose
design doc says there is none has every reason to distrust the rest of it.

What was cut was an orchestration layer: reviewer adapters that shelled out to
a vendor's review command, a packet assembled for an agent to judge, a context
brief that pre-gathered callers and docs, a review-and-ingest loop with its own
schema, and a posted review built from agent prose. It wrapped an agent that
was already better at the job than the wrapper, and its output could not be
measured, because every part of the input moved with the repository.

What replaced it is one function call. `redline review` takes the session
`run` already wrote, sends one request with no tools, and writes the result
through the same `review.json` a human or an agent writes by hand. Three
properties make it a different proposition from the thing that was cut:

- It is evaluable. The input is a frozen session, so the same fixture produces
  the same prompt on every run, and a change to the prompt or the model can be
  scored against annotations rather than argued about. The layer that was cut
  had no fixed input and so no way to tell an improvement from a mood.
- Its price is bounded and measured. One turn against a capped request means a
  review cannot run away, and every run is priced into a ledger. The old loop's
  cost grew with the conversation, because every turn re-sends the whole of it.
- It reads what the tools already found. An agent reviewing on its own
  re-derives what Redline measured. This producer is given those findings as
  priors and told not to restate them, which moves its attention onto the one
  thing no producer can reach alone: whether two facts from different
  producers contradict each other.

That last property is the argument for the producer existing at all. A
migration that adds a non-nullable column is a fact. A struct field that
cannot express absence is a fact. Neither pane can see the other, and static
analysis structurally cannot cross that gap. Correlating them is the work.

The boundary that makes it maintainable: the harness lives here, and
everything language-specific lives behind a provider interface. Redline links
no language toolchain, resolves no symbols, and knows no provider by name. A
provider is a separate program run as a subprocess that fills in a context
envelope; Redline ranks and truncates that envelope by role and priority hint
without reading the code inside, which is what lets one budgeting
implementation serve every language. The contract is in
`docs/context-envelope.md`, and `internal/boundary` fails the build if this
repository ever reaches for `go/ast`, `go/types`, or `x/tools`. The claim is
worth exactly what its enforcement is worth.

The prompt is split along the same line. Redline supplies the harness half:
the output schema, the instruction not to restate priors, that connecting two
priors is valuable, and that zero findings is a valid result. The provider
ships the language half in the envelope, as data Redline concatenates and
frames as advice. The cost of that cut, worth naming because it is the one
argument against it: Go review knowledge ends up next to the provider's own
rules rather than next to the harness, encoded in one repository and consumed
in another. That is why the envelope carries a provider version.

### Panes gate, reviewers advise

Doctor's contract works because its findings are deterministic: errors gate,
warnings report, only new findings count. A reviewer's finding is
nondeterministic and costs money to reproduce. Folding one into a gate gives a
merge queue a verdict the author cannot re-derive, and a gate that blocks on
something you cannot reproduce gets bypassed with `--no-verify` inside a
month.

So the merge gate skips `source: llm` entirely: `GateVerdict` ignores those
findings and no blocking marker is stamped on one, whatever severity it
claims. This was not hypothetical when it was written down. An agent comment
with severity error in `review.json` already failed the gate, which means the
invariant was being violated before it was stated.

There will be a moment when a review finding seems important enough to block
on. The answer is here, and it is no.

### The bar, and how cost is budgeted

The goal is a review comparable to Copilot's code review in cost and in
effectiveness, and both halves need numbers or "comparable" means whatever
the reader wants it to mean.

Effectiveness, from GitHub's published figures over 60M reviews: 71 percent of
reviews produce actionable feedback averaging 5.1 comments, and 29 percent
return clean. The clean rate is the target that decides whether anyone keeps
reading the output. Cost: Copilot's code-review model carried a multiplier of
13 premium requests, roughly 52 cents at the legacy overage rate. It now bills
AI Credits against token consumption at list rates, so there is no pricing
structure to arbitrage and matching the cost means matching the token spend.

That target is an average, and the distinction changes what the ceiling is
for. If every review had to come in under the average, the ceiling would be a
governor, and it would trim context from the large changes that most need it.
Because the target is an average, a large change is allowed to cost more than
a small one, and the ceiling only has to stop the pathological tail. So the
ceiling is set where a review stops being worth doing in one turn, the average
is measured rather than assumed, and every run appends its cost to a ledger
that `--stats` reads back.

The ceiling bounds the whole request rather than the context block alone. That
is not a detail: the first time this was wired to a real provider, a ceiling
that governed only the context let a large diff carry the total to 205k tokens
against a nominal 120k budget. A bound that does not bound is worse than none,
because it is quoted.

### What Copilot actually does, and what that changed here

This design was planned against an assumption that turned out to be wrong, so
the correction is recorded rather than quietly absorbed.

The assumption was that Copilot code review is a single pass over a diff with
repository context, and that its cost advantage came from not running an agent
loop. It is the opposite. Copilot code review moved to an agentic tool-calling
architecture in March 2026: it explores the repository, reads related files,
and traces cross-file dependencies before commenting. It reads the full
content of every changed file rather than the modified lines. It follows
imports and call sites into the modules a change affects. And it blends those
LLM detections with deterministic tools, CodeQL and ESLint among them.

So the cost is not held down by avoiding turns. It is held down because the
retrieval behind each tool call is a pre-built semantic index: repositories
are indexed into embeddings in seconds, and a call returns a small relevant
result instead of making the model grep and read. Cheap turns, not no turns.

Three things follow, and all three are now implemented.

The one-shot design still holds, for a reason narrower than the original one.
Resending a large diff on every turn is what makes a loop expensive, and this
tool's diffs are large. Two-stage retrieval was measured and rejected: on the
largest change here it would have cost more than sending everything once,
because the diff would be paid for twice.

Whole changed files are now carried, up to four hundred lines, which is what
Copilot does and what this did not. The gap was real: the model was seeing
about three quarters of the lines in the files it was reviewing. It is close
to free, because with the file present every enclosing-declaration expansion
inside it is already shown and drops out of the budget. On the largest change
here the total did not move at all; the whole files displaced weaker context
at the same price.

The deterministic half was the part this already had, and had more of. Copilot
blends two linters. Wave one here runs the repository's own configured tools,
scopes them to the delta, and hands the results to the review as priors it is
told not to restate. That is the same architecture, further along.

### Two ways to spend more, and the index question

The cost target being an average leaves headroom, and there are two ways to
spend it. Both are now built or scoped, and neither is the default until
something measures whether it reviews better.

`--mode explore` sends a catalogue of the resolved context instead of the
context: every expansion listed by role, symbol and location, with a tool that
returns the ones the reviewer asks for. It reads the diff, forms a question,
and fetches what answers it, paying for what it asked for rather than for what
was guessed in advance. This costs more, measurably: modelled against these
fixtures it runs about forty per cent above one shot at two turns and double
at three, because every turn resends the conversation. That is the point of
the mode, so the cost cap stops being a tripwire there and becomes the
governor, enforced after each turn against what another resend would cost.
The API's own task budget paces the model within a turn but does not cap the
resend, which is where the money goes, so it cannot be the only control.

The tool's whole corpus is the frozen envelope. No filesystem, no network, so
an explored review is still a pure function of a saved session and the eval
can still replay it.

The deeper question is the one the Copilot research raised: their index is
built once over the repository and queried, while this envelope is derived per
change and travels in the request. That is why the agentic shape loses here
and wins there. An index that persisted would remove the per-change payload
entirely, and it is the single largest structural improvement available.

Whether that index should be a graph like Graphify was settled too early, and
on one axis only. Tree-sitter resolves call edges by name and not by type, and
that is a real weakness for the claim "you changed this signature and here is
who calls it", which is why the Go provider resolves through go/types object
identity instead. But precision and persistence are different properties, and
rejecting the graph on precision decided nothing about persistence. A graph
that is a file on disk, updated incrementally and queried, has exactly the
property the measurements above say is missing.

They are not competitors. The exact resolver answers "who really calls this
symbol I changed", which needs types and only needs the changed symbols. A
standing graph answers "what is near this change" across languages and across
kinds, including SQL schemas and infrastructure files that no Go toolchain
will ever see. The correlation case this whole second wave exists for, a
migration read against the struct that writes the row, is exactly a
cross-kind question, and a Go type checker cannot answer half of it.

Nothing in the contract has to change to try it. Providers are found by
config, several merge into one budget, and the roles are language-agnostic. A
graph provider would sit beside the Go one and Redline would not know the
difference.

### What is still open

The producer ships with eight fixtures. The plan calls for twenty drawn from
real pull requests, and the number that matters most is the clean rate: how
often a change that deserves no comment gets none. Configuration is unchosen
until that sweep runs.

Prompt caching stays off. Cache writes cost 1.25x base input at the
five-minute TTL and 2x at the hour, and a pre-push tool firing a few times a
day pays every write and reads none of them. It is worth revisiting only if
measured review frequency shows clustering.

## The product

`redline run` observes a change (working tree, commit, range, branch, or
PR) and writes `findings.json`, `report.md`, and `report.html`. A person
opens the report. An agent reviewing the same change reads `findings.json`
so it spends its context on judgment instead of re-deriving measurements,
and writes `review.json` with what it concluded. Either reviewer decides
what each fact means, and the decisions persist by fingerprint across
re-runs. `redline post` publishes the observed findings to the PR for
reviewers who never leave GitHub, and a profile lets a merge gate read the
result.

`findings.json` plus git is the whole machine interface. An agent that
wants the diff runs git; the evidence it should not re-derive is in
`findings.json`; its own conclusions go in `review.json`.

Redline can only show the detail behind gates it can run, and which
tools a repository runs is a fact about the repository that no detection
rule fully recovers. So setup is an agent's job, through a second skill.
`/redline-setup` walks CI, the task runner, hooks, and tool configs for
the analysis tools already in use, states which ones Redline detects on
its own, writes `.redline.yml` entries for the rest after running each
tool once to read its output shape, validates with a run, and suggests
tools that apply to the repository's files but are missing. It installs
nothing and commits nothing; the owner reviews the file.

## Decisions

The facts are the better-built half. The decisions a reviewer records
against them are narrow today: three rulings on a suppression (justified,
should-fix, rule-noisy), and comment, explain, or apply on a diff line or
finding. If the product is the detail a reviewer decides on, each kind of
fact wants its own vocabulary, and a decision once made should survive a
re-run and a rebase the way verdicts already do.

The vocabulary to build, per fact type:

- An uncovered added line or function: needs a test; not worth testing,
  with the reason; unreachable.
- A coverage drop: accepted, with the reason; needs work.
- A lint delta finding: fix; accept, with the reason; rule is noisy.
- A config drift entry: intended; should be reverted.
- A migration or contract finding: accepted, with the reason; needs work.

Every decision carries who made it, a person or the agent, and a one-line
rationale. A decision keyed to a fingerprint that no longer matches any
finding is kept but shown as stale rather than dropped, since a rebase
should not erase a reviewer's reasoning. Decisions render on the finding
they judge, and a summary at the top of the report counts what is decided
and what is still open, so a reviewer can tell at a glance whether the
review is finished.

Whether decisions should post to the PR is an open question below.

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

The suppressions are the first fact type with a decision vocabulary: the
agent (or a person) records a verdict per directive, keyed by fingerprint,
and Redline renders it on the card beside the fact. That is the pattern the
Decisions section extends to the rest.

Mechanics: detection is by the tools' own config files, and the runs use
their JSON output. An earlier revision of this plan preferred the Makefile
`lint` target as the interface; building it reversed that. A make target's
output has no structure to fingerprint, and the delta needs identity, the
same finding at two revisions, rather than text. The base side runs in the
cached detached worktree gitx already keeps per commit, which is the
base-and-head runner the coverage delta below reuses.

## Phase 3: coverage, measured

The shipped pane intersects an existing profile with the change's added
lines and is honest about absence and staleness. It reads what the harness
left behind, which is the framing's preferred source: the gate and Redline
then agree on the number, and the reviewer sees the lines behind the bit
the gate reported. Six additions, in order:

- A measured mode. `--measure` runs the repo's test command with an
  instrumented profile at head, for when the harness leaves no profile or
  the one it left is stale. Off by default: a pre-push tool that silently
  runs a suite needing a database or ten minutes is one nobody reaches for.
  Reading the harness's own profile stays the default, with its provenance
  and staleness reported as today.
- Function-level findings. A new or wholly rewritten function that no test
  executes becomes a named finding anchored at its declaration. "34% of
  added lines" tells a reviewer to worry; "`ResolveTarget` is new and
  nothing executes it" tells them where, and gives them a fact to decide
  on.
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

These are the facts with no gate, cheapest first:

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
  reports "skipped: no browser". This is the only UI story, which raises
  its priority within this phase.
- Performance stays deferred entirely. There is no cheap stand-in for a
  measurement; a measured pane gets built when use shows the need.

Configuration for the runtime panes (`redline.toml`: build, run, seed,
migrate, routes, normalizers) gets designed when the first runtime pane is
built, from what it actually needs.

## Posting and the gate

`redline post --pr N` posts the observed findings: line comments for those
whose `file:line` is on the PR diff, the rest in the body, and a body that
opens with a one-line-per-pane summary table (migrations, contract, lint
delta, diff coverage, and what did not run) plus `--report-url`. Agent
review comments post too, attributed to the agent. Everything hard-won
stays: event COMMENT only, idempotency per (PR, head SHA) via hidden
markers, findings off the diff riding in the body so one stray line cannot
422 the review, refusal when the session is a different PR, credentials via
`gh`.

The `--profile` gate reads observed findings: the YAML names the markers,
blocking severities fail, info does not. The profile may fail closed on a
pane that applied but did not run, on the principle that a missing check
must not read as a pass.

The check-run question stays open. "Migrations applied cleanly, lint delta
+0, diff coverage 84%" in the merge box is the natural delivery for
evidence, where a comment thread was the natural delivery for prose.

## The report page

The page is the reviewer's view of the facts and the decisions on them.
Orientation is the PR title and body, and the Jira ticket when the fetcher
gets built, at low priority. The agent's overview, when it wrote one, sits
at the top. The evidence panes fill the rest: screens, contract, schema,
coverage, lint, and the observed findings. The drill-in shows every changed
line with everything known about it: changed, flagged by a linter, run by a
test, commented on by either reviewer. Viewed state, click-a-line comments,
and explain-and-apply requests stay, exported as JSON for the author's
agent as today.

Distribution is unchanged: one self-contained HTML file, served over
loopback locally, published as a CI artifact per PR when a team wants a
shared page. A hosted server with live comment state remains the fallback
if artifacts prove too clunky.

## Standing decisions

Carried forward: stable fingerprints as the join key for decisions, comment
state, and posting; findings and reviewer state in separate files, and
Redline never writes or overwrites the reviewer's files; fresh throwaway
containers; pinned browser builds; confirmations kept and collapsed; every
exclusion named; a pane that applies but cannot run reports loudly.

New this revision: the default source for a measurement is the artifact the
harness already produced, and re-running is the fallback; the report
states a fact's source, a pane or a reviewer, and attaches no caveat to
it; a decision survives a re-run; a pane the repository has no files for
is recorded as not applicable and mentioned nowhere, while a pane that
applied and could not run stays loud. Absence is stated when it is a gap
in the review and left out when it is a fact about the repository.

Retired: the rule that Redline never transports a model's judgment. It
does, through `review.json`, in the same way it transports a person's
through `comments.json`. What holds is narrower: the binary never invokes a
model, and it never composes a judgment of its own.

## Order of work

1. Done. The cut: the adapters, packet, brief, review loop, and agent
   sections of the report.
2. Done. Lint delta, suppression triage, config drift, on the two-revision
   runner.
3. Done. The agent as reviewer: `review.json` ingest, suppression verdicts,
   explain and apply from the report.
4. Done, in part. The review producer: the context envelope, the provider
   registry, the budgeting, `redline review`, and the fixtures it is scored
   against. What remains is the fixture set at twenty rather than eight, and
   the sweep that picks a configuration.
5. Decisions: the per-fact-type vocabulary above, stale rather than dropped
   on a fingerprint miss, and the decided-versus-open summary.
6. Coverage: function-level findings, coverage delta, error-path split,
   lcov, test-delta facts, then measured mode.
7. vacuum and sqlc staleness, then the database pane, then UI capture.
8. The check-run, and the Jira header when it earns its slot.

## Open questions

- Whether decisions post to the PR. A verdict that a suppression is
  justified is useful to a reviewer on GitHub, and it is also a reviewer's
  opinion riding in a review Redline signs. Likely answer: post them,
  attributed, and let the profile decide whether an undecided blocking
  finding fails the gate.
- The base revision for the delta runs when merge-base does not build or
  lint cleanly enough to compare. Likely answer: report the base run's
  failure as its own fact and degrade to head-only.
- How much toolchain detection to attempt before requiring `redline.toml`.
  The reference stack's Makefile convention has held so far.
- Where the Jira credential lives when the fetcher gets built. Same answer
  as `gh`: a CLI or env token Redline never stores.
- Whether the review producer should run more than one wave. Two waves cover
  every producer that exists, and a dependency graph is more machinery than
  the problem currently has. Revisit when a producer needs a third level.
- Whether a second language provider wants a shared expansion engine.
  Expansion is inherently language-specific, so expect the shared part to be
  no larger than the envelope plus the budgeting, which already exist.
