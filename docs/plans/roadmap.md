# What is left

One plan, holding the work that has not shipped. Eight documents ran beside
each other until now (`staged-review.md`, `two-stage-review.md`,
`potential-enhancements.md`, `report-roadmap.md`, `review-feedback.md`,
`graph-context.md`, `copilot-style-review-body.md`,
`redline-review-tooling-learnings.md`) and most of what they proposed is in
the code. What shipped is described in `CHANGELOG.md`, the README status table
and the doc comments beside the code; the arguments that produced it are in
git history, `git log --follow -- docs/plans`.

Effort is rough: **S** days, **M** weeks, **L** multi-week. Each item names
what blocks it, the evidence that put it here, and the test that ends it. An
item with no evidence is a guess and says so.

Three invariants hold across everything here, and an item that breaks one is
refused rather than weighed. `redline run` calls no model. The session is the
whole input to a review, so a fixture replays it, and a lookup pass keeps that
true by freezing what the scout found into the session before the reviewer is
called. A finding reaches a pull request only through the ruling's gate.

## Where the review stands

A default `redline review` is two calls over one cached prefix. The first
describes the change and writes the overview and one line per shown file; the
second is asked for findings alone and reads the first call's prefix back at a
tenth of base input. `--verify` adds the scout's lookups and a ruling behind
them. `--pipeline staged` splits the shown files into cohorts and sends one
judging call per cohort over the same prefix, with `--plan` to stop after the
partition and `--only-cohorts` to judge part of it. `--pipeline stepwise`
describes from the diff before the rest of the packet arrives. The prompts are
files under `internal/review/prompts`, cut to what the model cannot infer. The
packet no longer pastes in what the panes already found. `post` gates on the
ruling, withholds low confidence and hedged wording, and keeps a reviewer's
info findings in the body instead of on the diff.

Context comes from per-language providers. `gorefactor` resolves Go through
`go/types`, and `tsrefactor` now does the same job for TypeScript through
`ts-morph`, scoped to `ui/**`, with the same envelope contract, the same caps
and the same byte-identical output on a repeat run. A file that had zero
expansions of any role in two reviews of one change came back with fourteen
once it was wired in. Deletions rank apart from other history under the
`removal` role, at rank 2.

The best recall recorded is `--pipeline staged` at three samples, 16 of 35
labelled defects, against 11 for the describing split and 4 for one call. The
three ran in one sweep at the same sample count on the same labels, so they
order each other (2026-09-13, `claude-sonnet-5`, ten fixtures). None of them
compares to a run made today: the fixture set has since gained a crash
fixture, clean twins for each synthetic defect, and about a hundred relabelled
comments, and the priors cut made some expectations unreachable (#62). Any arm
scored from here needs its own baseline first.

## The gap every measurement points at

The producer checks arithmetic between things visible in the same window: this
constant against that comment, this estimate against that cap. It does not
follow a value into the function that consumes it. On this repository's PR #46
an agent reading the same change at the same revision found eight real defects
and `redline review` found none of them, and the crash it opened with,
`failures[0]` on an empty partition, has now been shown to four runs on
changed lines without once being reported. Naming the class in the prompt did
not move it.

A second change proved the same thing from the other side. A stale React Query
cache key spanning two files was missed by two reviews, and the first
explanation was that the frontend had no resolver, so those files were being
read from the raw diff alone. With `tsrefactor` wired in, both halves of the
bug were in the prompt and unclipped by the ceiling, and the review still
missed it. The thinking trace shows the model applying the right rule several
times, including the one already written into the provider's prompt fragment
about a query key being a contract. It never asked the single cross-file
question that catches this: what does the page's refresh button invalidate,
and does it cover this component's queries. Context was not the constraint and
neither was idiom in the prompt.

| Item | What | Effort |
|---|---|---|
| **The review runs its own lookups** | The one shape nothing has tested. `fetch_context` in explore mode indexes expansions the packet already resolved and cannot grep the tree, so a loop there buys a smaller prompt rather than more thought. A real search tool in the judging call, answers arriving in context, no separate pass to reconcile, is what addresses "a self-certified `diff` question is never checked", two rows down. A 170k prefix re-read at the cached rate is about $0.034 a turn on `claude-sonnet-5`, so five turns of self-service lookup is cents. It would collapse `internal/scout` and the ruling stage that exists to reconcile answers with findings written before they arrived. It becomes the default when an equal-sample comparison beats the shipped shape on recall at no more than twice the cost, and a session with the lookups frozen into it still replays. The cents estimate is the thing to hold loosely: Alibaba's `ocr`, an agentic search loop over a 43-file change at medium effort in September 2026, ran past $20 a review, overshot its own 5M token cap by 150% to 235% before stopping itself, and asserted two false positives at high severity while finding one real bug nothing else found. Reach and an unreliable cost profile come together unless the loop is bounded by something firmer than a budget flag. | L |
| **A targeted check for the cross-file question** | The cache-key miss in the section opening is one specific question asked of every changed `useQuery` and mutation: name what invalidates it, and show that the invalidation covers it. Whether that belongs in a prompt as a distinct pass, or in a pane as a structural check, is untested, and the evidence so far says more idiom prose will not do it. Run the controlled test before building either: the fixture is the cache-key change, and the test is whether the arm names the uncovered invalidation on a majority of samples. | S |
| **A self-certified `diff` question is never checked** | `rule.go:266` reads `QuestionDiff` as "the material already shown settles this" and skips the lookup. Nothing validates the claim against what was actually shown. On the PR #46 run, six of the nine findings that came back `unverifiable` carried `kind: diff`, and the review delivered five of fifteen, so this cost two thirds of it. A `diff` question whose subject does not appear in the shown material should be promoted to a lookup. | S |
| **Nothing in the pipeline can run the code** | Three of four questions on one run were settled in minutes by marshalling a request and printing the JSON, and by sending one probe. Read and grep reach none of that. The worktrees under `~/.redline/worktrees` are already checkouts at the reviewed revision, so a bounded execution step (build, run one test, marshal one request) is reachable, and it is the class of evidence the real risks lived in. | M |
| **Findings are conditionals, not claims** | Six findings over two runs were all "if the SDK does X then this breaks". A reviewer producing only conditionals has moved the whole burden onto a lookup pass priced at a sixth of it. Score it as a column: the fraction of findings whose body depends on a fact the producer could not reach. | S |
| **A decidable crash belongs in a pane** | An index into a slice a branch can leave empty is decidable without a model. Four runs shown that exact line reported nothing, so this is not prompt work. | M |
| **The ruling reads everything to rule on a handful of findings** | One ruling call carried 274,471 characters to weigh six findings with two answers. Caching hides the cost and not the effect: a judge working under 120k tokens of unrelated context. Send the findings, the hunks they anchor to, and the answers. | M |
| **Send the evidence that the tree compiles** | `golangci-lint` cannot run without a successful build, so a lint pane that ran is proof the code compiles. Pass that to both calls and say nothing when the pane failed. It is the only refutation for a false belief about the language, which no lookup can touch: `for turn := range opts.MaxTurns` was called invalid Go in four runs out of four under `go 1.26`. Blocked by "`gorefactor lint --json` panics" under Lint and external tools, which is why the pane keeps failing on this repository. Done when four runs over the same fixture stop calling legal Go invalid. | S |
| **Rank a declaration above a sibling call site** | A ruling settled a question about SDK behaviour on a repository habit (`internal/scout/tools.go` builds the same type the same way) when the answer was a struct tag one file away in the module cache. The answering brief ranks evidence against a finding above evidence for it; it does not rank a definition above a call site. | S |
| **The scout cannot see outside the tree** | The honest negative shipped: an empty search says what it searched and what is outside the repository. The module cache is still not in scope, so a question about a dependency still ends at "nobody can check it here". | M |
| **Effort thins verification** | `--effort high` asked one question against three findings; `--effort low` asked two against six. The finding that got a lookup is the one that reached the pull request. Worth an arm before any default moves; `REDLINE_EVAL_EFFORT` exists. | S |

## The staged pipeline

Cohorts ship and are off by default. What the plan called steps 6 and 7 is
what is left of it.

| Item | What | Effort |
|---|---|---|
| **The merge stage** | One call behind the cohorts that receives every cohort's findings numbered across cohorts, plus the scout's answers, and emits the rulings, the deduplicated comments and the final review. Dedup by `UnionKey`, which prefers the finding's question over its prose. With one cohort it degenerates to `Verify` as it ships today. `--merge-context prefix\|findings` decides whether it reads the whole cached prefix, which is where cross-cohort correlation is recovered, or the findings and the change section, which is cheaper and makes it a merger. | M |
| **Divide the scout's allowance** | `--scout-budget shared\|per-cohort`. `DefaultMaxCostUSD` is one scout's allowance for one change at $0.25, and one scout per cohort spends it N times. Shared divides it; per-cohort multiplies the bill knowingly. | S |
| **Label what the staged arm said** | The staged sweep wrote 97 unlabelled comments across thirty reviews, two and a half times the one-call arm's. None of it scores as wrong until somebody reads it, so the arm cannot be called better rather than louder until those dumps go through the `reject` list. Done when the staged arm's unlabelled count is zero, every comment being an `expect` hit or a `reject`. | S |
| **Score `stepwise`, `--plan` and `--only-cohorts`** | All three ship unmeasured. Explore mode shipped unmeasured too and came last but one when it was finally scored. Done when each has a sweep row against a baseline taken on the same labels. | S |
| **Equal-sample comparisons** | Every cross-arm number on record compares a union of three samples against one sample, or different denominators. Nothing supports a claim about shape until two arms run at the same sample count on the same labels. | S |
| **Review only the cohorts that changed** | On the second round of reviewing one pull request, redraw nothing and judge only the cohorts whose files moved since the last round. The pieces missing are the partition in `session.json` kept for reuse, a lookup from a target to the head SHA of its last review (the ledger records `revision` as `baseSHA:headSHA` and nothing queries it by pull request), mapping `git diff lastSHA..newHEAD` onto the saved partition by file path with `repairPartition`'s fallback for files that are new or moved, and a CI step that restores the prior `.redline` artifact, which the workflow already uploads per run with fourteen-day retention. What this saves is stage one's redraw and the cohort calls for files nobody touched. It does not save the cache write: the TTL is minutes and a review round is days. | M |
| **Spend the freed budget on context when reviewing narrow** | A single-cohort review leaves most of the ceiling unused, because only that cohort's files compete for room. That headroom could carry what a full review drops: design docs and path-scoped rules for the touched area, fuller test context, and the language-agnostic tail through a graph provider. Untested, and it depends on "review only the cohorts that changed". Done when a narrow run's ceiling carries the extra context and an equal-sample comparison shows what it bought. | M |
| **Settle the premise** | `redline review --stats` on a real ledger, now that it groups by shape. The test is arithmetic: median output scaled by the model's output-to-input rate ratio, five on Sonnet, against median input. If input wins then no rearrangement of stages saves money and the staging argument is about quality and latency. | S |
| **Retune the defaults** | A shape becomes the default when an equal-sample comparison puts it ahead on recall without losing correlation survival, and not before. That needs "the merge stage", "label what the staged arm said" and "equal-sample comparisons" first. Until then staged stays opt-in. | S |

Constraints a new stage inherits, each measured rather than read from
documentation:

- Every cached stage sees the whole shared prefix. Scoping is done by the
  instruction after the breakpoint, never by withholding input, or the prefix
  is not byte-identical and nothing caches.
- The output contract travels as a constant tools array selected by
  `tool_choice`. A `tool_choice` change preserves all three caches on
  `claude-sonnet-5`, which the API documentation says it does not. Re-check it
  on another model, and on any run whose cache reads come back zero.
- The array has a ceiling: `review + ruling + findings + cohorts` was refused
  with "the compiled grammar is too large", `ruling + findings + cohorts` was
  accepted. The array carries the contracts a run's shape can ask for.
- Effort and model are pinned across a cached pipeline on Sonnet. The
  mid-conversation forms that would let them vary are Opus 5 and Fable 5.1
  only.
- The TTL question is the start-to-start gap between consecutive calls sharing
  the prefix. The 1-hour write costs twice the 5-minute one and buys nothing
  while that gap stays under five minutes; the merge is the exposed call,
  because the scout runs on its own prefix and refreshes nothing.
- Forced `tool_choice` returns 400 on Fable 5.1 and Mythos 5.1, so the staged
  shape is refused there before anything is sent.
- Cohorts shrink the task a call is given and not the tokens it reads. Every
  call carries the whole prefix, which is what the cache is keyed on.
- `--plan` is not a cheap preview. It pays the full cache write for the prefix,
  $0.72 to $0.95 on a 43-file change on `claude-sonnet-5`, and only pays off if
  a cohort call follows and reads what it wrote.
- Cohort names come from a live call, so two runs over one change can name and
  split it differently. That is why `--only-cohorts` falls back to matching a
  file path when no name matches.
- `--thinking` is a large multiplier with unclear return. One eight-file cohort on
  `claude-sonnet-5` took 10m26s and $1.41 with it, against 8.6s and $0.056
  without, and produced three findings, two of them useful.
- Narrowing does buy precision on the files a call is given. The single-cohort
  run found an extension of a known bug that no full-diff run surfaced, from
  Redline, from `ocr` or from Copilot.

## Measuring it

An arm reports recall on labelled defects, correlation survival, the clean
rate, comments per changed file, walkthrough completeness, cache reads and
writes per stage, and cost and wall time per shape. A mean across shapes is a
price nobody was charged.

Two arms may be compared when the label set, the sample count and the fixture
set are the same on both. Anything else is evidence about one arm and cannot
order two, which is what most of the numbers on record are. Two bars decide
whether an arm may become a default: correlation survival does not fall, since
a staged arm that gains recall while losing cross-cohort findings has traded
away the reason the producer exists, and the labelled defects survive the
ruling, since a ruling tuned on precision alone learns to withdraw.

| Item | What | Effort |
|---|---|---|
| **Score the agent, not only the producer** | A `review.json` written by any agent already scores through `eval.Score`. An `--arm agent` that emits the two prompt files and scores whatever comes back keeps the premise measured as the packet changes. The throwaway that measured it once was deleted. | S |
| **Measure per expansion, not per envelope** | Every context comparison so far is all-or-nothing. Scoring by role says which ones pay: history bought `baseline-not-relocked-for-the-new-rules`, the graph's neighbour expansions bought nothing measurable at 60% more input. | M |
| **Freeze the field sessions** | The reviews the consumer's agent dismissed have sessions in that repository's `.redline` directory and a stated reason each. They become fixtures with the reason as a `reject` entry, marked as a reader's opinion rather than a verified fact. This has been waiting on those directories. | S |
| **Per-finding ledger rows** | `reviews.jsonl` keeps cost, duration, stop reason and a finding count; `review.json` is overwritten each run. Append a row per finding with its id, confidence and ruling verdict, and "low confidence predicts nothing" becomes a number instead of an argument. | S |
| **Rerun the sampling overlap** | Zero overlap across forty samples was measured when a finding's identity was its normalised wording. Identity is the question now, so agreement across samples may be a usable signal and `--samples` may stop multiplying noise. | S |

Settled, so nobody pays for them twice:

- The ladder that showed a forty-line prompt beating the shipped one did not
  survive direct measurement. Every earlier paid run went through a local
  proxy that appended an instruction block to the system prompt. Run directly
  at three samples the rungs catch 9, 10 and 7 of 31 and differ mainly in
  unlabelled comments, 12 against 4 against 2. The long prompt is the default.
- Free-form output against the strict grammar is inside the noise floor, so
  brief runs the short prompt over the grammar and both wires send the same
  request.
- Effort is a cost dial. Three arms at 2.4x the price caught 4, 2 and 5 of 32.
- The describing split buys a complete walkthrough and slightly fewer
  unlabelled comments, and no recall on its own. It is the default because a
  reader of a large change had roughly a one in three chance of a report with
  no walkthrough on it.
- The packet beats the diff alone on quiet rather than on catches: 12 against
  11 caught, with unlabelled comments falling from 25 to 16.

## Reading back what happened to a finding

Nothing measures whether a posted finding was any good. The people reading the
reviews already say what they think: they resolve a thread, react to it, reply
explaining why it is wrong, or merge without touching it. A finding never
replied to, never reacted to, and sitting on lines the author never touched
again is the shape of noise; one replied to and followed by a commit on those
lines is the shape of value. None of the signals is clean alone, and the one
worth adding rather than inferring is a reaction convention documented in the
review body: thumbs up useful, thumbs down wrong.

The numbers do not go in the report. A per-change report is the wrong place
for a cross-change statistic.

| Item | What | Effort |
|---|---|---|
| **Record what was posted** | One row per finding at `post` time, keyed by fingerprint plus repository and PR. Without it there is no denominator. Half of it is "per-finding ledger rows" under Measuring it, which writes the same row at review time. | S |
| **Read the threads back** | `redline feedback --pr N`, or a scheduled job over recently posted PRs, matching threads to fingerprints by marker and writing outcomes: disputed, acted on, ignored, resolved silently. `internal/feedback` already reads the markers and exposes `Answered` and `Disputed`; count an attributed reply as answered without waiting for the thread to close, because the consumer replies first and lets the merge gate close it. | M |
| **Report it** | `redline stats`: per substrate, per severity, per source, per ruling, over a window. It answers which substrates get disputed most, whether `source: llm` findings land better than deterministic ones, and whether the clean rate on real changes is near the three in ten the prompt assumes. | S |
| **Act on it** | A substrate disputed more often than acted on is a candidate for demotion to info; a rule disputed on the same grounds repeatedly is a candidate for a scope exclusion. A tool that tunes itself toward what reviewers accept will learn to say less, and the finding people most want to ignore is sometimes the one they most need, so the numbers inform a person changing a default and do not close the loop themselves. | M |
| **Pin, and write one rule down** | Not this repository. The consumer's `REDLINE_VERSION` on a build that carries the thread-memory work, and a committed `review.instructions` for feed ingest parity. `redline learnings` drafts path-scoped rules from replies and nobody ran it between two reviews of the same head, which is why the field paid for the same lesson twice. Two findings from the PR #1360 investigation are also still open on that repository's main, a `clearAllFilters` scope leak and a no-usage rule that lost its `UsageHistory` guard, drafted as a ticket and not filed. | S |

The target is stated so it can be missed: disputed under one in ten posted
findings, which is the low end of what the commercial tools report, while the
labelled-defect bar in the eval holds. A configuration that gets the first by
losing the second is not an improvement.

## What the reader is handed

| Item | What | Effort |
|---|---|---|
| A profiled post throws away a paid review on a head race | `require_head: true` is the default and `enforceProfile` runs before the payload is built, so a commit landing between the review step and the post step in one CI run ends as `session reviewed <old> but PR head is <new>` and exit 1. The review is computed, paid for and never posted. The fix is the non-profiled branch of the same function: plain `redline post` handles the same staleness by warning, prepending a notice that the review is of a superseded commit, and posting anyway. The profile should do that and withhold only the freshness credit, so a stale review still cannot satisfy a gate that wants one review covering current head. Two concerns are conflated into one hard failure and only the second needs enforcing. | S |
| Order by finding class, severity second | The only ERROR on one packet was `file-size`, 501 lines against a limit of 500, while the behavioural defect was INFO. Passing the tool's severity through is right, and gating is what severity is for, so the fix is in how the report and the pull request body order what they print. | S |
| A tripwire on model-written prose | A ruling emitted `…the finding claims.dependencies.python.org.(placeholder)` into a verdict, absent from both requests and from the review response. It rendered into `report.md` and would have reached a pull request comment. Bare domains and placeholder-shaped fragments in `source: "llm"` text are cheap to catch. | S |
| Report revision-sync integrity | `report.md` claimed no agent review was merged while `review.json` recorded the change under review and the report printed its five comments. The staleness note and the merged findings must not be able to disagree. | S |
| Let the reviewer contradict a pane | Two `sibling-missing-file` findings claimed a capability was tested on one side only, and a test file in the same diff tested it on both. A `correlation` finding can build on a pane finding and has no way to say one is wrong. Wants a rebuttal in the review schema, checked by the scout like any finding, never over an ERROR gate. | M |
| Route the unknowns to the reviewer | Two Copilot findings landed exactly in Redline's own "what could not be determined" list. That list is printed for the human and not handed to the reviewer as tasks. | S |
| Falsify the stated invariants | The pull request title, body and commit bodies now render under `## The change`. What is left is asking the reviewer to test each stated invariant against the code rather than read it as background. Copilot did this by default and caught two bugs with it. | S |
| Click a line, see the tests that cover it | The profile knows covered from uncovered and not which test ran a line. Needs per-test coverage, which means running the suite many times, so it cannot be default `run`: an opt-in command writing a line-to-tests map the report reads. Same data mutation testing wants, so build it once, and it holds the first invariant only while `run` never calls it. | L |
| Interface section | A permanent placeholder that only ever renders a gap. Agent-supplied route screenshots, a deterministic list of what UI moved, or drop the section. | M |
| Verdict vocabulary | Whether a coverage gap wants "risk / acceptable" and config drift "reasonable / hiding something", or whether justified, should-fix and rule-noisy carry those too. Decide it against real reviews. | S |
| Verdicts on mutation survivors | "needs test", "equivalent", "acceptable" per survivor, keyed on the stable mutant id. | S |
| Post verdicts to the pull request | Whether human and agent decisions ride on `redline post` or stay local. | M |
| Multi-reviewer `review.json` | Merge two agents, or an agent and Bugbot, into one report without overwriting, keyed by a `reviewer` field. | M |
| `redline serve` live loop | Replace the copy-paste handoff with a websocket or stdin bridge. | L |
| Whether a refused record should cost a cap slot | The scout filed one range twice under the same finding id. The resolver dedups identical ranges, so the cost is a wasted turn and a consumed slot rather than doubled evidence. A design decision, not a bug. | S |

## The deterministic half

The observation half is the part with no competition, and across the twenty
four commits before this was written it received zero added lines against
4,011 for `internal/review`. The packet measurably earns its cost and the
in-process reviewer measurably does not, which is the argument for spending
here: more panes, more of the change observed, and the eval arms that say
which expansions are worth their tokens.

### Coverage

| Item | What | Effort |
|---|---|---|
| Diff coverage against the repository baseline as a finding | Diff coverage renders as a markdown table with no severity and no `findings` entry. One dogfood change ran 49% against a 79% baseline in a repository whose own rules say never lower coverage thresholds. Threshold configurable per adopter. | S |
| Measured mode (`--measure`) | Run the repository's test command with coverage at head when no profile exists or the artifact is stale. Off by default so `run` never silently starts a ten-minute suite. | M |
| Function-level findings | Name new or rewritten functions with no executing test, anchored at the declaration. | M |
| Coverage delta | Per-package percentage at base and head on the two-worktree runner. A drop becomes a finding with the number in it. | M |
| TypeScript and lcov profiles | Read lcov or istanbul output the way Go's `coverage.out` is read. | M |
| CI profile ingest | Read a profile from a CI artifact path or URL when local produce is too heavy. | M |

### Lint and external tools

| Item | What | Effort |
|---|---|---|
| `gorefactor lint --json` panics | `panic: ast.Walk: unexpected node type <nil>` from `analyzer/block_naming.go:180`: a `for` with no condition gives `s.Cond == nil` and `loopCollection` walks it. Reproduced on v0.16.0. `redline/lint` reads `failed` on every run in this repository, so the lint delta is missing from every dogfood review. Upstream, not this repository. | S |
| Built-in `tsc` wrapper | A small reporter so TypeScript errors join the lint delta without every consumer inventing JSON flattening. | S |
| SARIF as a documented pattern | Docs and a setup recipe for tools that only emit SARIF. | S |
| Companion checks beside differs | Repositories pair an OpenAPI lint with a second command on the same file. Document the pattern; setup could emit sibling entries. | S |
| Markdown lint | `markdownlint-cli2` on changed `*.md`. Docs-only changes are common and CI often skips them. | S |
| Scope suppressions out of `_test.go` | Fuzz seeds and fixture `//nolint` read as real suppressions today. | S |
| `untested-function` on the diff | Whether a changed function has any test at all, which diff percentage does not answer. Blocked on repo-relative paths from gorefactor. | S |
| Smell findings with line anchors | Gorefactor smells and duplicate-block rules report no node position, so nothing can place them on a line. Upstream, not this repository. | M |
| Baseline files in setup | `baseline: {mode: file}` is implemented; `/redline-setup` should detect committed debt files and wire them. | S |

### Harness and worktree preparation

| Item | What | Effort |
|---|---|---|
| Dependency install steps | `ui/node_modules` in a detached review worktree, confirmed live: eslint failed for a missing `ui/node_modules/.bin/eslint`, so every UI-touching change reviewed from a detached worktree gets zero lint coverage. Same `produce`-on-`when: missing` shape as the shipped `ui-embed` entry. | S |
| Mutation produce, opt-in | A profile with `produce: make mutate` scoped to changed packages. Slow and database-dependent, so never on default `--prepare`. | S |
| Generated-code drift pane | Run codegen and diff, reporting which generated paths drifted. | M |
| Staleness hints for slow profiles | When `coverage.out` or `mutants.json` is older than the diff, say how stale in the report and in `findings.json`. Partial today. | S |
| Multi-profile merge | Go coverage beside UI lcov: apply scope per profile and merge examined files and unknowns without double counting. | M |
| `redline.toml` runtime config | One config for database URL, migrate command, seed and routes, designed when the first runtime pane ships. | L |

### New panes and substrates

| Item | What | Effort |
|---|---|---|
| As-of provenance trace | When a change introduces a snapshot or as-of value, follow it through the call graph and flag every sibling time source not derived from it (`now`, `CURRENT_DATE`, `UTCStartOfDay(now)`). One check catches both bugs Copilot found and Redline missed. | M |
| Response-shape detector | A collection field whose size scales with entity count and has no pagination. Structural heuristic on the OpenAPI schema and its consumers, no model. | M |
| React Query cache-key hygiene | A new `useQuery` key not nested under the prefix the page's refresh invalidates, which goes stale for as long as the refresh interval. This is the deterministic half of "a targeted check for the cross-file question", and the one bug in this class that has been traced end to end: the reviewer had both files in front of it and the rule in its prompt. | M |
| OpenAPI rule lint as built-in | Detect ruleset and spec paths and run vacuum without every repository duplicating the YAML. | M |
| sqlc and codegen staleness | Regenerate and diff. Same family as generated drift. | M |
| Spec against handler | The OpenAPI pane is contract diff only. Checking handlers match needs a running API or a static crosswalk. | L |
| Migration execution against Postgres | Apply migrations in a throwaway database: lock class, rewrite risk, timing, down migration, constraint violations on seed data. The git checks are the cheap half and they ship. | L |
| Fixture and seed safety scan | Scan changed testdata for real identifiers. | M |
| Import and architecture lint | `go-arch-lint` or a depguard matrix as a scoped custom tool. | S |
| UI capture | Pinned browser, route screenshots, console errors, failed requests. No perceptual diff. | L |
| Report-level mutation efficacy | A one-liner from `test_efficacy`, `mutants_total`, `mutants_killed` and `mutants_lived`, caveated when `infra_errors > 0`. | S |
| Mutation ingest from CI | Read the same `mutants.json` shape from a downloaded CI artifact path, with no local produce. | M |
| `--run-mutant-id` in the handoff | The repro command renders on the report; it is not in "Copy for the agent". | S |

### The standing graph

`cmd/redline-graphify-context` reads `graph.json` and writes an envelope, with
the mapping in `internal/graphify` and a boundary test keeping the rest of the
module out of it. On this repository it sent five expansions and every one was
`neighbor`, because the Go extractor emits no `implements` or `inherits`
edges at all. On a single-language repository with an exact provider already
in place, the graph adds type-definition context and little else, and the
correlation case it was built for needs a repository whose changes span kinds.

What it is for is settled now, and it is narrower than this started as. A
graph provider was tried as the answer to the TypeScript gap and could not see
that one page called a hook it imported, because resolving a destructured hook
return needs real symbol binding and tree-sitter parses syntax. A language
with a resolver gets a resolver, `gorefactor` or `tsrefactor`. What the graph
is left holding is the tail nobody will write a resolver for: SQL, YAML,
Terraform, shell, request collections.

| Item | What | Effort |
|---|---|---|
| An eval fixture that spans kinds | Nothing is measured. The clean rate and the correlation findings are the open question, and this repository cannot pose it. | M |
| The `neighbor` role | It arrives as an unknown role today and ranks last. Promote it to the vocabulary only if the correlation findings show up. | S |
| Graph queries as scout tools | `graph_affected` and `graph_path` give the scout the cross-kind question directly rather than reconstructed from a one-hop walk. The CLI's prose answers are the wrong shape for a program and the right shape for a model. | M |
| Graph queries in the reviewer's own loop | Needs the queries and their answers frozen into the session the way the envelope is, or a fixture cannot replay. | L |
| Audit the installed grammars | Without `tree_sitter_sql` the SQL extractor returns an error that the merge step drops, so 88 migrations produced zero nodes with no warning anywhere. The adapter notes a claimed file the graph holds nothing for; the missing grammar itself is upstream. | S |

Two rules hold whatever else changes here. `caller`, `sibling` and `type` draw
only from the tree-sitter half, because the semantic half is a model call and
does not replay byte-identically; pulling a semantic edge into the one-shot
envelope is a determinism regression. And a hop into a node with no
`source_file` is an unknown rather than silence: an incremental update invents
bare nodes for symbols defined outside the batch, 48 of them sharing the label
`Client` on one dogfood run.

### Gorefactor upstream

Smells and duplicate-block findings carry no position, so they cannot anchor
to a line. The `untested-*` rules emit module-qualified paths, which Redline
strips through `go.mod`. Orphaned-config-path fires on gitignored directories,
so consumers need placeholder directories; document it in the setup skill.

## Setup, CLI, distribution

| Item | What | Effort |
|---|---|---|
| Setup skill: worktree steps | Propose `harness.worktree` entries when linters need generated or installed paths. | S |
| Setup skill: harness profiles | Detect `make test-coverage` and `make mutate` and suggest profiles with the right scope and `when`. | S |
| `redline doctor` | Validate `.redline.yml`, check tool presence, dry-run worktree produce, print what would run for the current diff. | M |
| Version skew warning | Compare the skill pin against the binary version on `run`. | S |
| Publish the findings schema | Document the `findings.json` version field and migration notes for consumers. | S |
| Artifact upload recipe | A standard Actions step uploading `report.html` and `findings.json`, with the URL passed to `redline post --report-url`. | S |
| More merge gate profiles | Require the mutation section, require a coverage profile, allow missing UI lint when nothing under `ui/**` changed. | S |
| PR head reuse | When CI and a developer both run at one SHA, skip re-lint if the artifacts are fresh. | M |
| Measure what the exclusions bought | The budget summary counts held-back and redundant expansions and nothing reads those counts back across runs. | S |

## Adopter notes

- ESLint and tsc need `ui/node_modules` in detached worktrees; only the UI
  build stub is wired.
- Gorefactor runs in Redline's lint delta and not in a typical repository's
  fast lint path, so Redline is stricter than the agent default. Document the
  gap for adopters.
- A CI baseline compare is faster than a local `make mutate` and lives outside
  Redline; a harness `path` to a downloaded artifact would reach it.
- oasdiff and the built-in OpenAPI pane overlap, so the report should label
  structural diff against oasdiff breaking rather than print the same break
  twice.
- Shellcheck scope is usually narrower than a repository's own shell gate. The
  setup skill should copy the real gate.

## If picking a small set next

1. The self-certified `diff` question promoted to a lookup, which is the
   measured constraint on two thirds of a review.
2. `ui/node_modules` in the worktree, confirmed failing live.
3. Diff coverage against the baseline as a gated finding.
4. Order the report and the pull request body by finding class before
   severity.
5. The profiled post that loses a paid review to a head race, which costs a
   full review's spend and reads in CI as a tooling bug.
6. The tripwire on model-written prose, which is the difference between a
   garbled verdict in a local report and one posted under the repository's
   name.
7. Per-finding ledger rows, which are the denominator for everything in the
   read-back section.
8. Generated-drift pane, Go only first.
9. The merge stage, and the labelling that lets the staged arm be called
   better rather than louder.
