# Potential enhancements

Ideas for Redline itself: new panes, harness behavior, tool wiring, and
workflow. Report layout and drill-in UX live in
[report-roadmap.md](report-roadmap.md). A standing code graph beside the
per-change envelope is worked out in [graph-context.md](graph-context.md).
Measuring whether any finding was worth making is in
[review-feedback.md](review-feedback.md), and it gates most of the choices
these tables pose. A review that checks its own findings before posting them
is worked out in [two-stage-review.md](two-stage-review.md).
Phased design rationale is in [redline-design.md](../../redline-design.md) at
the repo root.

Each item is a candidate, not a commitment. Effort is rough: **S** days,
**M** weeks, **L** multi-week.

## Mutation (gomutants v0.6.0+)

gomutants **v0.6.0** (latest stable; MCT pins it in `GOMUTANTS_VERSION`) adds
several features aimed at CI and review workflows. Redline ingests
`mutants.json`, overlays **LIVED** survivors on changed lines, and now reads
the v0.6.0 statuses and the stable mutant `id`.

### Shipped in gomutants v0.6.0

| Upstream change | Why it matters | Redline today |
|-----------------|----------------|---------------|
| **`INFRA_ERROR` status** ([PR #83](https://github.com/szhekpisov/gomutants/pull/83)) | Per-mutant classification when `go test` fails for environmental reasons (OOM, disk full, too many open files), not because the mutant was caught. Stops silent **KILLED** inflation on flaky CI runners. | **Read.** Counted apart as `mutation.infra`, named in `unreliable[]`, reported as an unknown, and it withholds the all-killed confirmation. |
| **Stable mutant `id` in JSON** (#86) | Fingerprint like `internal/foo/foo.go:Double:RETURN_ZERO#1` survives rebases better than file+line+type. Enables `gomutants --run-mutant-id` for repro. | **Read.** On every survivor as `id`, used as the verdict key when present, with the repro command on the report. |
| **`--changed-since` merge-base scope** (#85) | Mutants limited to lines changed vs the merge base, not an arbitrary ref tip. Matches PR review scope. | N/A (gomutants CLI). MCT `make mutate` already passes `--changed-since origin/main`. |
| **Return-value mutators** (#80) | `RETURN_ERROR_NIL`, wrong-return variants; catches error-swallowing gaps. | Surfaces as ordinary **LIVED** when on added lines (no special labeling). |
| **`--run-mutant-id`** (#87) | Re-run one mutant for debugging after reading the report. | Not wired into "Copy for the agent" or report UI. |
| **`--detect-equivalent`** (existing; MCT uses on `make mutate`) | Marks provably unkillable survivors **EQUIVALENT** after assembly compare. | **Read.** Counted apart from **LIVED** and rendered as a muted note, not a survivor. |

### Redline enhancements (gomutants-aware)

| Item | What | Effort |
|------|------|--------|
| **Surface `INFRA_ERROR`** | Count infra failures separately from killed/lived. On changed lines, add `findings.Unknown` (substrate `mutation`) naming file, line, mutant `id`, and that efficacy for that line is unreliable. Mutation section summary: "N infra errors on changed lines; do not treat as killed." Never fold into **KILLED**. | done |
| **Carry mutant `id` in `findings.json`** | Parse `id` from report; expose on each survivor; use as verdict fingerprint key (see verdicts row below). Link text in HTML: "repro: `gomutants --run-mutant-id '…'`". | done |
| **Label mutator types on survivors** | Show `RETURN_ERROR_NIL`, `CONDITIONALS_BOUNDARY`, etc. in the drawer (type is already in JSON as `type`; ensure it renders in markdown/HTML). | done |
| **Report-level efficacy context** | Optional one-liner from top-level `test_efficacy`, `mutants_total`, `mutants_killed`, `mutants_lived` when present, with caveat when `infra_errors > 0`. | S |
| **`EQUIVALENT` / `NOT COVERED` nuance** | **NOT COVERED**: leave to coverage pane (today). **EQUIVALENT**: info finding or muted marker, not a red survivor stripe. | done |
| **Harness: minimum gomutants version** | Setup skill and docs: recommend **v0.6.0+** when mutation profile is enabled; note that pre-0.6.0 reports lack `INFRA_ERROR` and stable ids. | done |
| **CI ingest without local mutate** | MCT `mutation-quality-check` filters daily CI artifacts to changed files (~seconds). Redline could read the same JSON shape from a downloaded artifact path (harness profile `path` only, no produce). Complements local `make mutate`. | M |
| **Verdicts keyed by mutant `id`** | "needs test" / "equivalent" / "acceptable" per survivor; stable across line shifts when id is present. Depends on id field above. | done |

### MCT CI context (dogfood)

Marketplace Core moved to gomutants **v0.6.0** for the daily mutation workflow.
Before **INFRA_ERROR**, that repo used a log-grep script
(`mutation_infra_signal_check.sh`) to flag OOM/disk-full signatures and warn
that efficacy might be inflated. The script remains a whole-run backstop;
per-mutant **INFRA_ERROR** is the authoritative signal in `mutants.json`.

Redline should treat a mutation report from v0.6.0+ CI the same way a human
would: **LIVED** on changed lines is actionable; **INFRA_ERROR** on changed
lines is "re-run or distrust this shard", not "tests are fine."

Recommended `.redline.yml` pattern for MCT-style repos:

```yaml
harness:
  profiles:
    - id: mutation
      path: mutants.json
      when: stale
      scope: ["**/*.go"]
      # no produce: make mutate is hand-run or from CI artifact download
```

Document that `make mutate` / `make mutation-quality-check` should run on
gomutants **v0.6.0+** so reports include `id` and `INFRA_ERROR`.

## Coverage

| Item | What | Effort |
|------|------|--------|
| Measured mode (`--measure`) | Run the repo test command with coverage at head when no profile exists or the harness artifact is stale. Off by default so `run` never silently starts a ten-minute suite. | M |
| Function-level findings | Name new or rewritten functions with no executing test, anchored at the declaration. Finer than a diff-coverage percentage alone. | M |
| Coverage delta | Per-package percentage at base and head on the two-worktree runner. A drop becomes a finding with the number in it. | M |
| Error-path nuance | Call out uncovered added lines inside error-handling branches separately from other gaps. | done |
| TypeScript / lcov profiles | Read lcov or istanbul output the same way as Go `coverage.out`, including measured mode when the UI test command produces one. | M |
| Test-delta facts (shipped) | Info findings from the diff: a Go package changed with no test in it, a `t.Skip`/`.skip`/`.only` added, or a net drop in assertion-like lines in a test. Pane `internal/pane/testdelta`. | done |
| Per-test attribution | Map a line to the tests that execute it. Needs per-test profiles and real test runs; same data mutation wants. Opt-in command, not default `run`. See report-roadmap. | L |
| CI profile ingest | Read `coverage.out` (or lcov) from a CI artifact URL or path when local produce is too heavy. Harness `from: ci` or env substitution. | M |
| Diff-coverage vs. repo baseline as a gated finding | Today diff coverage renders as a descriptive markdown table only (`report.coverage.diffCoverage`), no severity, no `findings` entry. Dogfooding on MKT-1415 (Marketplace Core): diff coverage was 49% against a 79% repo-wide baseline, and MCT's own `CLAUDE.md` states "Never lower coverage thresholds" as an invariant — exactly the kind of drop that should surface as a `WARNING`/`ERROR` finding, not sit only in a table a reviewer might skim past. Threshold (repo baseline, a configured floor, or both) should be configurable per adopter. | S |

## Lint and external tools

| Item | What | Effort |
|------|------|--------|
| Built-in `tsc` wrapper | Ship a small reporter script (like repo-specific `redline-eslint-json`) so TypeScript errors join lint delta without every consumer inventing JSON flattening. Example stub exists in `.redline.yml.example`. | S |
| SARIF as a documented pattern | First-class doc + setup skill recipe for tools that only emit SARIF (many security scanners). Parser may already exist; make setup discover it. | S |
| Companion checks beside differs | Repos often pair OpenAPI lint with a second command on the same file (`gen-permissions --check`, custom codegen verify). Document the pattern; setup skill could emit sibling tool entries. | S |
| Markdown lint | `markdownlint-cli2` on changed `*.md` via JSON or wrapper. Docs-only PRs are common; CI often skips them. | S |
| Scope suppressions in `_test.go` | Fuzz seeds and fixture `//nolint` in Redline's own tests read as real suppressions today. Per-path exclusion or default skip for test-only paths. See report-roadmap backlog. | S |
| `untested-function` on the diff | Gorefactor info finding for changed functions with no test at all. Blocked on repo-relative paths from gorefactor (see below). | S |
| Smell findings with line anchors | Gorefactor smells and duplicate-block rules report no node position today, so Redline cannot place them on a line. Upstream fix unlocks the unified line view. | M (upstream) |
| Baseline-file tools in setup | `baseline: {mode: file}` is implemented; `/redline-setup` should detect committed debt files and wire them without hand-editing. | S |

## Harness and worktree preparation

| Item | What | Effort |
|------|------|--------|
| Dependency install steps | Worktree `produce` for `node_modules`, `go generate` stubs, or other gitignored dirs linters need. MCT needs `ui/dist` (shipped) and `ui/node_modules` (not yet). **Confirmed live on MKT-1415**: `redline/lint` (eslint) failed on that run with exactly this cause — `ui/node_modules/.bin/eslint` missing in the detached review worktree — meaning every UI-touching PR reviewed from a detached worktree gets zero lint coverage until this ships. Same `produce`-on-`when: missing` shape as the existing `ui-embed` entry; wire `scripts/ui-yarn-install-if-needed.sh`. | S |
| Mutation produce (opt-in) | Harness profile with `produce: make mutate` scoped to changed packages. Slow and DB-dependent; must stay off default `--prepare`, documented as explicit opt-in. Require gomutants **v0.6.0+** in docs. | S |
| Generated-code drift pane | `verify-generated` style: run codegen and diff. Not a linter delta; needs a pane that reports "these generated paths drifted" with file list. | M |
| Staleness hints for slow profiles | When `coverage.out` or `mutants.json` is present but older than the diff, say how stale (mtime vs HEAD) in the report and `findings.json`. Partial today; make it consistent. | S |
| Multi-profile merge | Some repos have Go coverage and UI lcov. Apply scope per profile and merge `examinedFiles` / unknowns without double-counting. | M |
| `redline.toml` runtime config | Single config for DB URL, migrate command, seed, routes when runtime panes land. Design when the first runtime pane ships (design doc phase 4). | L |

## New panes and substrates

| Item | What | Effort |
|------|------|--------|
| OpenAPI rule lint (vacuum) as built-in | Many repos already wire vacuum in `.redline.yml`. Could detect ruleset + spec paths and run spectral-report without YAML duplication. | M |
| sqlc / codegen staleness | `sqlc generate` (or equivalent) and diff `internal/data/db`. Same family as generated drift. | M |
| Spec vs handler agreement | OpenAPI pane today is contract diff only. Checking handlers match the spec needs a running API or static codegen crosswalk. Deferred in openapi pane comments. | L |
| Migration execution (Postgres) | Apply migrations in a throwaway DB: lock class, rewrite risk, timing, down migration, constraint violations on seed data. Git checks (shipped) are the cheap half. | L |
| Fixture / seed safety scan | Scan changed testdata for real identifiers. Go tool, text output; wrapper or SARIF. Valuable for data-heavy repos. | M |
| Import / arch lint | `go-arch-lint` or depguard matrix as a scoped custom tool. Complements golangci, not a duplicate delta. | S |
| UI capture | Pinned browser, route screenshots, console errors, failed requests. No perceptual diff. Explicit `redline setup browser`; pane skipped when missing. Design doc phase 4. | L |
| Interface section | Permanent gap banner vs agent screenshots vs deterministic "what UI moved" list. No decision yet. See report-roadmap backlog. | M |
| Provider-parity pane (`internal/pane/parity`, shipped) | Deterministic, gorefactor-complementary: for repos with sibling provider/adapter directories (e.g. MCT's `internal/provider/{aws,azure,gcp}/**`), diff filenames and exported-symbol names across siblings when a change touches one. Flags "this capability exists for provider A but not B/C" without a shared interface — the exact class of gap gorefactor's sibling-expansion missed on MKT-1415 (`internal/provider/gcp/offer_amend_mutability.go` has no Azure/AWS analog, and no shared interface links them, so `redline/context` reported "no sibling expansions" even though the parity question itself is answerable cheaply and deterministically). Path/name heuristic only — no LLM, no semantic graph. Shipped as filenames only: comparing exported symbols would need a per-language notion of what exported means, which is the knowledge that lives behind the provider boundary, and the motivating case is a filename case. A `minShared` guard of three common filenames keeps it quiet on directories that merely sit side by side. | done |

## Agent workflow and decisions

| Item | What | Effort |
|------|------|--------|
| Verdict vocabulary expansion | Was M when only an agent writing review.json could rule on anything. `redline review` now carries verdicts in its own schema and the prompt asks for them, using the same three words (justified / should-fix / rule-noisy) across every finding class rather than a second vocabulary. What is left is smaller and is a wording question, not machinery: whether a coverage gap wants "risk / acceptable" and config drift "reasonable / hiding something", or whether the three existing words carry those too. Decide it against real reviews rather than in advance. | S |
| Verdicts on mutation survivors | `mutants.json` overlays lines; reviewer records "needs test" / "equivalent" / "acceptable" per survivor. Key verdicts on gomutants v0.6.0+ stable `id` when present. | S |
| Post verdicts to PR | Whether human/agent decisions ride on `redline post` or stay local. Open question in design doc. | M |
| Multi-reviewer `review.json` | Merge agent + Bugbot (or two agents) into one report without overwriting. MCT publishes per-reviewer; Redline could key by `reviewer` field. | M |
| `redline serve` live loop | Replace copy-paste "Copy for the agent" with a websocket or stdin bridge. Upgrade path noted in report-roadmap. | L |
| Broader review skill context | Skill text for "why was this nolint added" and whether a rule is noisy. Docs/skills, not binary code. | S |
| Domain-aware `promptFragment` | Delivered by the scout rather than as proposed, and better: instead of a config key naming convention docs and a digest appended to every review, `redline-scout` finds `AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md` and the rest at the root and beside the changed files, reads the short ones in its opening turn, and records the lines that bear on this change under a `guideline` role, quoted from the file. Per-change instead of per-repo, and no config to keep current. The MKT-1415 case it was written for is the one it is aimed at: a domain rule written down in the repository, invisible to generic-Go doctrine. | done |

## Measured defects (dogfood, 2026-09-10)

Found by running Redline on its own pull requests and on `gorefactor`, with
`--debug` capturing every request. Each row was reproduced, not read. The
arms referred to below are two runs over one frozen session (commit
`5774523`), `gpt-5-mini` over `api.openai.com`, at `--effort low` and
`--effort high`.

| Item | What | Effort |
|------|------|--------|
| **`gorefactor lint --json` panics** | `panic: ast.Walk: unexpected node type <nil>`, from `analyzer/block_naming.go:180`: a `for` statement with no condition (`for {`) gives `s.Cond == nil` and `loopCollection` walks it. Reproduced on gorefactor v0.16.0 against Redline's own tree. `redline/lint` reads `failed` on every run in this repository, so the lint delta is silently absent from every dogfood review. Upstream, and it blocks the compile-evidence row below. | S (upstream) |
| **Language-fact false positives survive both stages** | `for turn := range opts.MaxTurns` reported as "invalid Go, will not compile" in four runs out of four, under `go 1.26` where range-over-int has been legal since 1.22, in a tree whose build and full suite are green. The verifying pass kept it every time. Naming the failure in the prompt (#36) did not stop it: no lookup can refute a false belief about the language, because no line in the repository speaks to it. | M |
| **Send the evidence that the tree compiles** | The refutation for the row above is already observed and not sent: `golangci-lint` cannot run without a successful build, so a lint pane that ran is proof the code compiles. Pass that fact to both calls, and say nothing when the pane failed. Unmeasurable in this repository until the gorefactor panic is fixed, which is the pane that keeps failing. | S |
| **The ruling re-reads everything to rule on a handful of findings** | The second call carried 274,471 characters to rule on six findings with two answers: 123,803 of context block and the whole diff again, 123,611 input tokens against stage one's ~20,000. Prefix caching hides the cost but not the effect: a judge weighing two answers under 120k tokens of unrelated context. Send the findings, the hunks they anchor to, and the answers. | M |
| **High effort asks no questions, so verification never runs** | `--effort high` chose the `diff` kind for every finding in both runs, asked zero questions, and the ruling decided alone. `--effort low` asked two. Effort silently switches the verifying pass off. Worth an eval arm before any default changes; `REDLINE_EVAL_EFFORT` already exists. **Refined 2026-09-11**: on a 196k-token request at `--effort high` the reviewer asked one question against three findings — one `caller`, two `diff`. So effort thins verification rather than switching it off, and which findings get a lookup is the part that matters: the one that was checked against the repository is the one that reached the pull request. | S |
| **The scout is not a cheap model** | Both defaults are still `claude-sonnet-5`, and the prose no longer says otherwise. The reviewer's prompt, the ruling's prompt and the fragment the scout's own envelope carries each called the lookups the work of a cheap model, which told the judge to discount evidence gathered by its equal; they name the pass now rather than its price. `DefaultModel` says what is true: the split is by job, nothing has been measured about what a smaller model costs in the quality of the fetching, and `--model` is how a repository tries one. The guarantee that would make a weak model safe there is stated where it belongs, on the rule that the scout writes locations and the program reads the bytes. | done |
| **The answerer passes no model, effort, or cost cap** | `cmd/redline/review.go` built `scout.Options` with root, diff, questions and credentials only, so `--model` and `--effort` could not reach the lookups. Both now pass through. The cost cap was never off: the scout applies its own default of a quarter to a zero `MaxCostUSD`, and it still does. | done |
| **No test covers a review that asks nothing** | `TestAReviewThatAsksNothingRunsNoLookupsAndIsStillRuled` covers it: a review whose every finding says the diff settles it calls the scout zero times, and the ruling is still sent, because the diff those findings point at is what it rules on. | done |

## Measured defects (dogfood, 2026-09-11)

Found by diagnosing a review that produced nothing, then reviewing the packet
from the run that replaced it (this repository, PR #37 at `b3a9302`). Every
row was reproduced against `.redline/debug`, `reviews.jsonl`, or the tree —
none is read off a report.

| Item | What | Effort |
|------|------|--------|
| **Reasoning can spend the whole output cap and return nothing** | `--effort xhigh` on a 196,233-token request emitted exactly 64,000 output tokens, every one of them reasoning, and produced no text block at all: `stop_reason` `max_tokens`, zero findings, $1.07, 10m35s. The same request at `--effort high` used 32,463 of the same cap and produced three. xhigh did not degrade here, it failed outright, and the error told the caller to raise a cap that was already at the model's ceiling. The message now separates the two failures the stop reason covers: a review cut off mid-finding still asks for a bigger cap, and a response with no content at all names reasoning and the effort that produced it. | done |
| **A streamed call printed nothing for ten and a half minutes** | `Progress` ran between stages and the stage was one call, so a working run and a hung one were indistinguishable from outside for the whole of it. A heartbeat now reports elapsed time, reasoning tokens and whether the answer has started, and warns once when reasoning has taken three quarters of the cap with nothing written — the moment the call is already lost. Tokens are estimated from characters, because the counts on the wire arrive with `message_delta` at the end of the turn. | done |
| **The captured response could not tell three failures apart** | `Capture` was handed the bare body and ran only after the error check, so a refusal, a truncation and a model that answered with nothing were three identical zero-byte files, and the failing paths wrote nothing at all. It now writes stop reason, usage, cost, duration and error alongside the body, on every path. Four of the findings below were only measurable because of it. | done |
| **Stage-one confidence gates posting; the ruling's verdict does not** | The gate reads the ruling now. `sanitizeRulings` records on a kept verdict whether its quote is really in the material the ruling was shown, which is the grounding check the suppressing verdicts already had to pass, and `effectiveConfidence` reads a grounded keep as medium whatever stage one guessed. So the defect this was written about, rated `low` before verification and then confirmed against a line of the diff, posts. A kept verdict is still never demoted for a quote nobody can find: the finding was written before this pass ran and stage one's confidence carries it as it always did. What is left is the other half of the sentence, stage-one confidence as a hint for the scout. | done |
| **The reviewer cannot contradict a deterministic finding** | Two `sibling-missing-file` findings claimed a capability was tested on one side and not the other; `internal/scout/brief_test.go` — in the diff the reviewer read — tests exactly that capability on the other wire. The reviewer can write a `correlation` finding that *builds on* a pane finding but has no way to say one is wrong, so both false positives reached the report unchallenged and made packet precision 78% against 3/3 on the agent's own findings. Wants a rebuttal in the review schema, verified by the scout like any finding, never over an ERROR gate. | M |
| **Parity read a test file as a missing capability** | A `_test.go` file is named after the unit it tests, not after a capability a sibling ought to match, so filename comparison inverts there. `internal/pane/parity` now skips test files; source parity, which is the pane's purpose, is unchanged. | done |
| **An ERROR one line over a limit outranks a logic bug** | The packet's only ERROR was `file-size`: 501 lines against a limit of 500. The behavioural defect was INFO. `lint/delta.go:535-547` passes the tool's severity through deliberately and should keep doing so — gating is what severity is for — but the report and the pull request body order by it, so a reader meets the line count before the bug. Order presentation by finding class first, severity second. | S |
| **Model-written prose reaches the report unvalidated** | The ruling emitted `…the finding claims.dependencies.python.org.(placeholder) (internal/scout/scout.go diff: …)` into a verdict. Traced through the captures: absent from both requests and from the review response, present only in `05-ruling.response.json`, so the model generated it — and byte-identically on the previous run. It rendered into `report.md` and would have reached a pull request comment had that finding been inline. A tripwire on bare domains and placeholder-shaped fragments in `source: "llm"` text is cheap and stops posting garbage under the repository's name. | S |
| **The ruling writes the prefix again instead of reading it** | The review call wrote 171,690 cache tokens; the ruling thirteen seconds later wrote another 170,601 and read **zero**. Not divergence: the response schema is part of the cached prefix and sits in front of the system block, so a stage that sends its own schema cannot read another's entry however the prompts are arranged. Measured on the wire — one system, one prompt, review schema then ruling schema: wrote 12,449 then 11,360, read nothing either time; the 1,089-token gap is the gap between the two schemas, the same figure the real run showed. A schema-less call misses too (its prefix has no schema), and `oneOf` is rejected outright (`Schema type 'oneOf' is not supported`), so one strict schema serving both shapes is not available. Samples cannot read each other either: three concurrent calls over a fresh prefix each wrote it. Nothing in a one-shot run ever read either entry, and a prompt carrying 14 timestamps and 13 SHAs never repeats across runs, so both writes were a quarter above base input for nothing. Both breakpoints removed; explore and the scout keep theirs, and that half is no longer an assumption: `TestExploreCacheProbe` sends explore's prefix twice in the shape `runExplore` builds it, and turn two read back all 5,437 tokens at 0.1x with only the 21 new tokens billed at full rate. The probe is committed and opt-in, so the next person to ask whether explore's breakpoints should come off too has the answer rather than the reasoning. Saves $0.171 of a $1.369 run at one sample and scales with `--samples`. The read itself is still on the table and costs a shared schema — see below. | done |
| **The governor prices the first turn at the wrong rate** | `overBudget` adds the write premium on any turn the wire has not reported cached tokens for, which is turn zero of every run, so the ceiling it checks is what the wire will bill. A prefix too short to cache is now overcharged by a quarter of a small prefix, which stops a run a little early rather than letting one past its allowance. The OpenAI governor is unchanged: that wire reports cache reads and never a write. | done |
| **A duplicate record costs a cap slot, not a duplicate expansion** | The scout filed `cmd/redline/post.go:341-353` twice under the same finding id. Worth recording that the obvious conclusion is wrong: the ruling prompt contains that range **once**, because the resolver dedups identical ranges into one expansion, and `TestTheRecordCapRefusesAndSaysToStop` pins that split deliberately — the cap bounds records, the dedup bounds expansions. So the cost is a wasted turn and a consumed cap slot, not doubled evidence. Whether a no-op record should count against a cap that exists to bound the reviewer's ceiling is a real question, and a design decision rather than a bug. | S |
| **The ledger records counts, not findings** | **Partly done.** `reviews.jsonl` now also keeps the stop reason, whether the output cap truncated the run, and whether it was billed at the batch tier, so a run cut off mid-finding is no longer indistinguishable from a clean pass and half-price rows no longer drag the interactive mean. What is still missing is the per-finding half: `reviews.jsonl` keeps cost, duration and a finding count; `review.json` is overwritten each run. Calibration could therefore only be assessed at n=3 on one run, which is not enough to justify the gate change above on evidence rather than argument. Append per-finding rows — id, confidence, ruling verdict, and eventually whether anyone acted on it — and "low confidence predicts nothing" becomes a number. | S |
| **The answering scout was given no changed paths** | `answerOptions` built `scout.Options` without `Changed`, and `guidelines()` finds package-nested `AGENTS.md` and its kin by walking up from each changed path. With it empty the answering scout saw root-level rules only, so a question about a rule was answered against the most general rules in the tree. `changedPaths` already existed in the same package and the standalone scout already passed it. Latent in this repository, which has no nested guideline files, and not in one that does. | done |

## Measured defects (dogfood, 2026-09-12)

Found by running `redline review` over this repository's own PR #38 and then
checking all seven of its findings against the code. Every row is reproduced
against `.redline/debug` or the tree. The review scored 4/7 correct with no
false confirmations, and every one of the three it could not settle traces to
scout evidence that never arrived — so the rows below are mostly about the
lookup half, not the reviewer.

| Item | What | Effort |
|------|------|--------|
| **The scout's question kinds are not its record roles** | The brief teaches five question kinds — precedent, caller, rule, history, type — and `record` accepts a different seven roles. Three words are in both lists, so tagging a record with the kind it answers works for those and is refused for `precedent` and `rule`. On PR #38 the scout answered a precedent question, said so out loud in the transcript, filed two records under role `precedent`, was refused twice, and ran out of turns. `tools.go:211` and `tools.go:250` are absent from `04-ruling.request.json` and the finding came back `unverifiable` on evidence that had already been found. This silently breaks the kind the scout's own brief calls "the kind that matters most". `envelopeRole` now maps `precedent` to `neighbor` and `rule` to `guideline`. | done |
| **A refused record costs a turn like an accepted one** | A closing turn whose every tool call came back an error buys one more filing turn, once, on both wires: the toolset counts the calls of the turn being dispatched and both drivers read `refusedEveryCall` after it. The granted turn is a second closing turn rather than a second closing message, so the tools stay `record` and `done` and the brief is not sent twice. Refusals on earlier turns still cost a turn, which is what they are for. | done |
| **The scout cannot see anything outside the tree** | **Partly done.** The honest negative shipped: an empty search answers with what it searched, that this repository is the whole of it and a dependency's code is outside it, the tool description says so before the call is made, and the answering brief tells the scout to put which it is in the note. So a dependency question reaches the ruling as "outside the tree, not checkable here" rather than as a silence. The module cache is still not in scope, so the answer to the PR #38 question is still that nobody can check it here. | M |
| **The scout answers the letter of a caller question** | The answering brief says it now, on the caller kind: when the question is what the called thing does with what it is given, the answer is in the callee and the call site only says where to look, so record the body that uses it. | done |
| **`Truncated` was spelled in one API's vocabulary** | `Entry.Truncated` was computed as `r.StopReason == string(anthropic.StopReasonMaxTokens)`, so a run over `--api openai` that hit its cap (`finish_reason` `length`, which `openai.go:234` already sets) recorded `Truncated: false` — silently defeating the bookkeeping the field was added for. The completion already carried `truncated` from both paths; `Result` now carries it too and the ledger reads it instead of re-deriving it from a string. Caught by the model, missed by hand. | done |
| **A canceled batch item reported no reason** | Only the errored variant of `MessageBatchResultUnion` carries a message, so a canceled or expired item produced "came back expired:" with nothing after the colon. Worth recording that the finding which caught this claimed a nil-dereference panic and was wrong — the SDK's `Error` is a value struct — but the empty message underneath it was real. | done |
| **`read_lines` and `grep` restated their caps by hand** | `record` builds its description from `ts.limits` precisely so the stated limit cannot drift from the enforced one; the other two tools spelled `200` and `60` as bare literals in both the description and the call site. Now `maxReadLines` and `maxGrepMatches`, used by both. | done |
| **Effort is a cost dial, not a quality dial** | Three arms over ten fixtures, byte-identical but for `output_config.effort`: low $0.0476/review and 4/32 caught, medium $0.0667 and 2/32, high $0.1126 and 5/32, with zero false positives throughout. 2.4x the price buys 4 -> 5, and the middle arm caught fewer than the cheapest; a monotonic quality curve does not look like 4/2/5. The whole spend is reasoning — one recorded run billed 23,853 output tokens against 8,877 bytes of visible response, ~90% of it never returned. No default was changed on this: n=1 cannot separate the effect from noise on the two fixtures carrying 29 of the 32 labelled defects. What it does settle is the negative — there is no case for `high`. `--samples 3` (about $7 at the batch tier) is what would settle the rest. | — |
| **Sweeps now cost half** | The Message Batches tier takes 50% off every token in both directions. Its twenty-four-hour window is an expiry rather than a promise, which rules it out for `redline review`, and fits the eval sweep exactly. `RunBatch` refuses what has no batched form — explore, `--samples`, the checking pass — rather than discovering it on the wire, and prices the tripwire at the tier that will bill it. The effort sweep above cost $2.27 where it would have cost $4.54. This is what makes the repeated sweeps affordable that every other cost question needs. | done |
| **The batch path is the least-tested code in the tool** | A fake batch endpoint over `Options.BaseURL` covers the path past the guards. Results returned in reverse are placed by `custom_id`, and the test fails on a positional pairing rather than passing either way; a canceled item reports in its own slot, with a reason, without failing the requests that were paid for beside it; an unknown `custom_id` refuses the whole pairing; and a slot with no result is named rather than read as a clean review. `verifyCeilingCost` is covered from both sides of a cap that fits stage one and not the pass after it. | done |

## Measured defects (dogfood, 2026-09-13)

Two `redline review` runs over this repository's own PR #41 — the caching
change — traced end to end in `postmortem.json`. Six findings produced, **none
posted**: three withdrawn, one justified, two unverifiable, at $0.89 and $0.32
a run. The refutation half did its job and the rows below are not about it: no
false finding reached the pull request, and the ruling withdrew the same claim
twice on lookups that genuinely disproved it. What neither run produced was a
finding about the change's subject. Every comment was about whether the SDK
does what the diff assumes; the live 400 on forced `tool_choice`, the
invalidation table in `staged-review.md` contradicting its own step 2, and
`Result.Prompt` being one concatenated block that no breakpoint can split were
all missed, and the first two were found by hand within minutes.

| Item | What | Effort |
|------|------|--------|
| **A `diff` question is self-certified and never checked** | `rule.go:266` reads `QuestionDiff` as "you said the material already shown settles this" and skips the lookup — `asked=false` in the postmortem, no scout turn spent. Nothing validates that claim against what was actually shown. On c3 the reviewer asked, as kind `diff`, whether `outputSchema()` and `ruleSchema()` close every object and require every property; neither function is touched by this change, so neither was in the diff it said would settle it. The ruling then had nothing, reached for evidence it had not been given, and the #35 guard demoted it to `unverifiable` — the correct end to a question that a two-line grep answers, and that the scout had answered from the same repository on the previous day's run. A `diff` question whose subject does not appear in the shown material should be promoted to a lookup, not trusted. | S |
| **Every finding was a conditional, not a claim** | Six findings over two runs, all six of the form "if the SDK does X then this breaks": *if* the batch type does not honour forced `tool_choice`, *if* a schema has an open object, *if* `ToolInputSchemaParam` has no implicit type. That shape is what the verify pass is for, but it also means stage one is spending its budget enumerating what it cannot check rather than asserting what the change does. Worth measuring as a column: the fraction of findings whose body is conditional on a fact the producer could not reach, per run. A reviewer that only produces conditionals has moved the whole burden onto a lookup pass that is priced at a sixth of it. | S |
| **Nothing in the pipeline can run the code** | Three of the four questions on the second run were settled in minutes by marshalling `anthropicParams` and printing the JSON, and by sending one probe request: `type: object` is emitted by the SDK's `constant.Object` default, `strict: true` with closed objects returns 200 on four models, and a switched `tool_choice` reads a breakpointed prefix back whole. Read and grep cannot reach any of that. The worktrees under `~/.redline/worktrees` are already checkouts at the reviewed revision, so a bounded execution step — build, run one test, marshal one request — is a reachable class of evidence and is the class this change's real risks lived in. | M |
| **The ruling settles on precedent when the answer is one file away** | c2 asked whether dropping the schema's `type` key loses `"type": "object"` on the wire. Ruled `justified` because `internal/scout/tools.go:212-214` builds the same SDK type the same way — a repository habit, offered as proof of an API's behaviour. The definitive answer is `anthropic-sdk-go@v1.71.0/message.go:9436`: `Type constant.Object json:"type" default:"object"`, with the comment saying it marshals its zero value as `object`. Right verdict, wrong reason, and a reader of the report learns the wrong reason. The answering brief already ranks evidence against a finding above evidence for it; it does not rank a declaration above a sibling call site. | S |

## Review quality (Claude comparison, this repository's PR #46)

Both reviewers were given the same change at the same revision: sixteen files,
`8a53554..a1ae729`, the staged-review fan-out. Claude read it as an agent
session with the repository open. Redline ran `--pipeline staged --ceiling
150000`. Claude posted eleven inline comments carrying **eight distinct
defects, all eight real and all eight since fixed**: an `index out of range`
on `failures[0]` when the partition comes back empty, stage one told a bound
the run would not honour, cohort calls priced without their own tails, stage
one priced twice under `--synopsis`, a truncation lost because the merge read
`kept[0]`, no `RunBatch` guard for the staged shape, the fallback dropping the
billing for the call it had already paid for, and cohorts matched by a name
nothing makes unique.

Redline found none of them.

| | Claude | Redline, as it ran | Redline, after the changes below |
|---|---|---|---|
| Findings produced | 11 (8 distinct) | 15 | 8 |
| Of the 8 known defects | **8** | **0** | **1** (in 1 of 3 samples) |
| Severity `info` | — | 14/15 | 7/8 |
| Confidence `low` | — | 13/15 | 7/8 |
| Comments actually posted | 11, all actionable | 3, **all lint** | 1, **a false positive** |
| Cost | unmeasured | $0.66 | $0.26 at three samples |

The yardstick is biased towards Claude and the bias cannot be removed here:
the labels in `testdata/fixtures/staged-empty-partition` were written from
Claude's comments, so it scores three of three there by construction. What is
not a matter of scoring is that every one of the eight was reproduced against
the code and fixed.

Three separate mechanisms produced that result, and they are worth keeping
apart because two are cheap and the third is the actual problem.

| Item | What | Effort |
|------|------|--------|
| **Lint outranked the review in its own review** | `post` posts a deterministic finding unconditionally and withholds a low-confidence one, so on a change where the reviewer hedged everything the three comments that reached the pull request were all `redline/lint`, at `Warning`, above the reviewer's `Info`. The author's own linter says the same thing at the same moment CI does. The lint pane is now counted and not posted: evidence table keeps the number, the body says how many went to the report, and `redline/lint` opens no threads. | done |
| **The reporting rules asked for the wrong thing, at length** | `what is worth reporting` opened with ten lines on the change not doing what its own description says, and six of the fifteen findings were duly sentences to reword. Removed, and the intent block is now framed as orientation — what the change is for, not a document to audit. Measured on a re-run over the same diff: prose-versus-code findings went from six of fifteen to **zero of eight**. | done |
| **Severity had no definition in the contract** | `commentSchema` shipped `severity` as a bare `enum {error, warning, info}` with no description, so the model called everything `info`. It now says what the word means: what happens if the finding is right, not how sure you are. | done |
| **`confidence: low` was sold as free** | The schema said low confidence "is folded away on the report, not discarded" and the prompt said an uncertain finding "costs the reader nothing". Both are false: `post.lowConfidence` withholds it. Both now say so, and the model went on marking seven of eight low anyway — but that is not what suppressed the review, and the first draft of this row said it was. Counted off the run's own `review.json`: of fifteen findings, four were low, kept and grounded, so `effectiveConfidence` promoted them and they reached the body along with the one medium. The ten that did not reach it were not withheld for hedging. **Nine came back `unverifiable` from the ruling**, and six of those nine carried `question.kind: diff` — the reviewer certified that the shown lines settled its own claim, so no lookup ran, so the ruling had nothing to rule against. The gate is not the constraint and neither is the wording. The constraint is two rows above this table, at "A `diff` question is self-certified and never checked", and it is now measured rather than argued: it costs two thirds of a review. | S |
| **Nothing ever measured whether a review catches a crash** | All thirty-five labelled defects in the fixture set were wrong answers, swallowed errors or inverted comparisons. The class Claude opened with had no fixture, so no arm could ever have reported it missing. `testdata/fixtures/staged-empty-partition` is the first one that carries a crash, frozen from this very change with three of Claude's findings labelled. | done |
| **Naming the crash class in the prompt did not buy it** | The class was added to `what is worth reporting`, in the same pass as the fixture, with the note that being unsure of the input is not a reason to lower the severity. Four runs have now been shown `failures[0]` on the changed lines of a diff they were reading — two before the change, two after — and none of them flagged it. The prompt is not the binding constraint. An index into a slice a branch can leave empty is decidable without a model, which puts this in a pane and not in a prompt. | M |
| **The producer describes diffs; it does not trace paths** | Under every configuration measured here, what comes back is arithmetic consistency between things visible in the same window: this constant versus that comment, this estimate versus that cap. What Claude did was follow `repairPartition` into `fanOut` and ask what the slice holds when the branch above it took the early exit. No amount of topic listing has moved that, and it is the whole gap. The instruments to work on it now exist — a crash fixture, a walkthrough-completeness column, and a shape-split ledger — so this is the next thing the eval should be pointed at. | L |

## The premise, measured (2026-09-13)

The design says the packet is the product and the agent writes the prose. That
claim had never been scored. Everything the eval measures is `redline review`
against fixtures; nothing measured whether **an agent given the packet reviews
better than the same agent given the diff**.

It costs nothing to find out. `internal/eval.Score` takes a `findings.Review`
and an annotation and does not care who wrote it, which is the boundary the
design already draws. So: eleven fixtures, both arms assembled by
`review.Assemble` so the instructions are byte-identical, one arm with
`Envelopes`, pane findings and coverage and one with an empty `Report` and no
expansions. Twenty-two isolated agents, one read of one file each, no
repository access. Scored by the product's own scorer.

| Arm | Caught | Unlabelled | Quiet violations |
|---|---|---|---|
| diff alone | **14 / 37** | 37 | 3 |
| the packet | **18 / 37** | 37 | 3 |

The packet wins by four, at the same false-positive rate, and it never loses a
fixture. Two of the four gains have a named mechanism rather than a coin
flip: `not-null-against-the-struct-field` carries `relates_to_rules`, and
`baseline-not-relocked-for-the-new-rules` carries `needs_history` — evidence
the bare arm provably did not hold. The other two are resolvable from the diff
and could be noise. Two fixtures cannot discriminate at all, having no
envelope or pane content to remove. This is n=1 per arm on a producer whose
variance is known to be total, so four is a direction and not a size.

The uncomfortable half is the comparison nobody asked for. On
`staged-empty-partition` **both** agent arms scored 3 of 3, including the
`failures[0]` panic — the defect `redline review` was shown on changed lines
four times and never once reported, and which its own sweep catches at 1 of 3
across three samples.

An earlier line here said a plain agent at one sample beats the staged
pipeline's 16/35 at three. That is not a comparison: 18/37 is one sample
against a union of three, on a denominator three expectations smaller, and
the union of three samples inflates recall by construction. What the numbers
support is narrower — the agent reached a specific defect the staged pipeline
has never reported — and the equal-samples comparison has not been run.

| Item | What | Effort |
|------|------|--------|
| **The packet earns its cost; the in-process reviewer does not** | Measured above. The observation half is the part with no competition and it is the part that received zero added lines across the last twenty-four commits, against 4,011 for `internal/review` and 1,817 for `internal/scout`. The conclusion the numbers support is to stop extending the reviewer and spend on what only this tool can do: more panes, more of the change observed, and the eval arms that say which expansions are worth their tokens. `redline review` keeps its place as the no-agent path — CI, or a repository with no harness driving it — and stops being where the work goes. | — |
| **Score the agent, not only the producer** | The scoring path for this ran as a throwaway and was deleted. It should not have to be rebuilt: a `review.json` written by any agent scores through `Score` today, so an `--arm agent` that emits the two prompt files and scores whatever comes back is a small, permanent addition to the eval and the only way the premise stays measured as the packet changes. | S |
| **Measure per expansion, not per envelope** | The A/B above and the context sweep before it both compare all-or-nothing. What a reader of the roadmap actually needs is which roles pay: history bought `baseline-not-relocked-for-the-new-rules` here, and the neighbour expansions bought nothing measurable on ten fixtures at 60% more input. Score by role and the expansion budget becomes an evidence-backed setting rather than a ceiling. | M |

## Review quality (Copilot comparison, MKT-1360)

Held against GitHub Copilot on the same PR (NetApp/marketplace-cp #1360), the
deterministic half of Redline covered a class Copilot has nothing for (lint
delta, coverage on added lines, structural parity, assertion drop), and the
agent half was out-reviewed. Copilot found two verified correctness bugs Redline
missed, both violations of the PR's own stated invariant that expansion counts
and drill-through lists evaluate at one `asOf`. Full write-up in
[../dogfood-reports/2026-09-10-copilot-comparison-mkt-1360.md](../dogfood-reports/2026-09-10-copilot-comparison-mkt-1360.md).

| Item | What | Effort |
|------|------|--------|
| Invariant hypotheses from the PR body | Extract stated invariants from the description and rationale (here: "shared evaluation timestamp so counts and lists agree") and task the reviewer to falsify each against the code. Copilot did this by default; Redline read the description as prose and never tested it. **Unblocked**: the description was not in the prompt at all when this was written. The pull request's title and body and the commit bodies now render under `## The change`, framed as a claim to judge the code against, so what remains is asking the reviewer to falsify each stated invariant rather than read them as background. | S |
| As-of provenance trace | When a change introduces a snapshot or as-of value, follow it through the call graph and flag every sibling time source not derived from it (`now`, `CURRENT_DATE`, `UTCStartOfDay(now)`). One check catches both missed bugs: `offers.go` derives `EndDateBoundary` from `now` while `AttentionAsOf` comes from `evaluationAt`, and the entitlement usage-window predicates hardcode `CURRENT_DATE - days` regardless of the threaded `asOf`. | M |
| Ruling loop generates, not just adjudicates | The two-stage ruling spent budget confirming a non-bug (test-products predicate, ruled kept) and marked two low-confidence infos unverifiable after empty lookups. An empty lookup should re-query with a different phrasing, not conclude unverifiable. Reward hypothesis breadth over defending the first guess. See [two-stage-review.md](two-stage-review.md). **Contradicted by measurement**: on four runs over two frozen sessions the ruling did the opposite, keeping findings whose lookups came back with nothing at all, including two that were provably false. Re-querying would buy a second lookup for a failure that is not happening, and the binding problem is a pass that keeps on absent evidence. Revisit only if an arm shows empty lookups concluding unverifiable. | M |
| Route pane blind spots to the agent | Two Copilot findings (openapi 403-vs-404, UI cache staleness) landed exactly in Redline's own "what could not be determined" list: the api pane follows no `$ref`, the ui pane checks 15-16 are unbuilt. Feed that list to the agent reviewer as targeted tasks instead of only printing it for the human. | S |
| Response-shape detector | A collection field whose size scales with entity count and has no pagination (Copilot #2: required member-ID arrays unbounded by buyer size, consumed only for a `count`). Structural heuristic on the OpenAPI schema and its consumers, no LLM. | M |
| React Query cache-key hygiene | Flag a new `useQuery` key that is not nested under the parent prefix the page's refresh invalidates (Copilot #4/#5: attention and summary keys outside `['buyers', organizationId]`, stale up to 10 min after a view refresh). A UI-substrate structural check. | M |
| Agent-comment coverage skew | On this PR the agent left 5 comments across 4 files, skewed to two SQL predicate files, while Copilot spread across the diff. Check whether the reviewer is capped per file or per finding-count in a way that starves later files. **Taken up**: one review per cohort gives each group of files its own attention budget, and comment distribution across files is one of the columns the arm is scored on. See [staged-review.md](staged-review.md). | S |
| Report revision-sync integrity | `report.md` claimed no agent review merged (wrong base revision) while `review.json` recorded the change under review and the report printed its five comments. The staleness note and the merged findings must not be able to disagree. | S |

## What the review request carries

| Item | What | Effort |
|------|------|--------|
| Test code held back | Changed test files are named with their line counts and not pasted, and `test` expansions are dropped before budgeting. Coverage and mutation already answer whether the tests assert enough, and the reviewer is told not to comment on it. A test-only change is the exception. | done |
| Graph provider beside the Go one | A standing cross-language graph as a second context provider, for the neighbors a Go type checker cannot see. Worked out in [graph-context.md](graph-context.md). | M |
| Measure what the exclusions bought | The budget summary now counts held-back and redundant expansions. Nothing reads those counts back across runs; the ledger could, and the clean rate is what would settle whether the trade was right. | S |

## Setup, CLI, and distribution

| Item | What | Effort |
|------|------|--------|
| Setup skill: worktree steps | `/redline-setup` proposes `harness.worktree` entries when linters need generated or installed paths (embed dirs, `node_modules`, sqlc output). | S |
| Setup skill: harness profiles | Detect `make test-coverage`, `make mutate`, and suggest profiles with correct `scope` and `when`. | S |
| `redline doctor` | Validate `.redline.yml`, tool presence, worktree produce dry-run, and print what would run for the current diff. | M |
| Version skew warning | Compare skill pin vs binary version on `run` (install doc already warns; enforce in CLI). | S |
| Publish findings schema | Document `findings.json` version field and migration notes for consumers (agents, merge gates). | S |

## CI integration and posting

| Item | What | Effort |
|------|------|--------|
| Artifact upload recipe | Standard GitHub Actions step: upload `.redline/report.html` + `findings.json`, pass URL to `redline post --report-url`. Document in README. | S |
| Merge gate profiles | More examples beyond `redline-review.yml.example`: require mutation section present, require coverage profile, allow missing UI lint when no `ui/**` in scope. | S |
| PR head reuse | When CI runs Redline on PR head and a developer runs locally, detect matching SHA and skip re-lint if artifacts are fresh (optional cache protocol). | M |
| Post agent + observed in one review | `post` today is observed findings; agent comments are separate publish scripts in some repos. Unify or document the two-step flow clearly. | S |

## Gorefactor upstream (blocks unified line view)

| Gap | Impact |
|-----|--------|
| Smells / duplicate-block without positions | Findings cannot anchor to a line in the drawer. |
| `untested-*` module-qualified paths | Redline strips via `go.mod`; repo-relative emission would be cleaner and more reliable. |
| Orphaned-config-path on gitignored dirs | Consumers need placeholder dirs (`.keep` pattern) for literal skip paths. Document in setup skill. |

## Dogfooding notes (Marketplace Core and similar repos)

Items surfaced while wiring `.redline.yml` on a large Go + React + OpenAPI repo:

- ESLint and tsc need `ui/node_modules` in detached PR worktrees; only `stub-ui` is wired today.
- Gorefactor runs in Redline lint delta but not in the repo's fast `verify-lint` path, so Redline is stricter than the agent default. Document that intentional gap for adopters.
- `mutation-quality-check` (CI baseline compare) is faster than local `make mutate` but is outside Redline; could be a harness `path` to a downloaded CI artifact. MCT pins gomutants v0.6.0 for **INFRA_ERROR** and stable mutant ids; Redline should surface those when ingesting `mutants.json` (see Mutation section above).
- oasdiff + built-in OpenAPI pane overlap slightly; report should dedupe or label "structural diff" vs "oasdiff breaking" so reviewers do not read the same break twice.
- Shellcheck scope is often narrower than `make lint-deploy-scripts` (severity, path filters). Setup skill should copy the repo's real shell gate, not `**/*.sh`.

## Suggested near-term batch

If picking a small set that improves most adopter repos without runtime infrastructure:

1. **gomutants v0.6.0 report ingest:** `INFRA_ERROR` → unknowns, mutant `id` on survivors, repro hint for `--run-mutant-id`.
2. **`ui/node_modules` worktree produce step** — confirmed failing live on MKT-1415, not just a theoretical gap; same shape as the shipped `ui-embed` entry.
3. Built-in or shared `tsc` JSON wrapper.
4. Setup skill proposals for worktree install steps and harness profiles (mutation profile + v0.6.0 pin).
5. Diff-coverage-vs-baseline as a gated finding, and verdict vocabulary for coverage and lint findings (not only suppressions); mutation verdicts keyed by `id`.
6. Generated / verify-generated drift pane (even if Go-only first).
7. Provider-parity pane — cheap, deterministic, fills the gap gorefactor's interface-only sibling expansion structurally can't reach.
8. Document CI artifact + `--report-url` posting pattern.

Three from the 2026-09-11 table belong beside these, because they change what
a reviewer is shown rather than what any adopter has to configure:

9. **Gate posting on the ruling's verdict, not stage-one confidence** —
   done. A finding the ruling kept on a line quoted out of the material it
   was shown now reaches the author whatever stage one guessed about it
   beforehand, and a keep on a quote nobody can find changes nothing.
10. **A tripwire on model-written prose** — cheap, and it is the difference
    between a garbled verdict sitting in a local report and one posted to a
    pull request under the repository's name.
11. **Decide whether the ruling should read the review's prefix** — the free
    half is done: neither one-shot stage writes an entry nothing can read, so
    a run no longer pays a quarter above base input twice ($0.171 of $1.369,
    more with `--samples`). The remaining $0.22 needs the two stages to send
    byte-identical schemas, because a schema is part of the cached prefix.
    `oneOf` is rejected by the API, so that means one flat schema holding both
    shapes with `required` relaxed to their intersection: each call is then
    shown the other's fields and neither has its own enforced. Cost against
    prompt clarity on the quality-critical call — a judgement, not a bug.
    Worth weighing against sending the ruling a focused prompt instead, which
    drops its input from 170k to about 20k and needs no shared schema at all.

And two from the 2026-09-12 table, both about the lookup half rather than the
reviewer, because a question the scout cannot answer costs the same as one it
can and reaches the ruling as silence:

12. **Give a refused record a turn to be corrected** — done. A closing turn
    spent entirely on refusals buys one more filing turn, so a correction the
    scout was told to make has somewhere to go.
13. **Let the scout say "outside the tree"** — the honest negative is done: an
    empty search says what it searched and the brief asks for a note naming
    what is outside the tree, so the ruling reads an answer where it used to
    read a silence. Putting the module cache in scope is still open, and it
    is the half that would actually answer the question.

## The pipeline against the same model in an agent loop (2026-09-13)

The premise test showed an agent reading the packet beat the diff alone. The
obvious next question was whether it also beat `redline review`, and whether
the difference was the model: the agent arms ran `claude-opus-5`, and
`DefaultModel` is `claude-sonnet-5`.

Holding the model at `claude-opus-5` and the packet and prompt constant —
both arms assemble through `review.Assemble`, so the system text is the same
bytes — on the same eleven fixtures, one sample each:

| arm | model | caught | cost/review | clean rate |
| --- | --- | --- | --- | --- |
| agent loop | claude-opus-5 | 18/37 | ~$0.26 (token estimate) | - |
| `redline review` one-shot | claude-opus-5 | 2/38 | $0.2797 | 64% (7/11) |
| `redline review` one-shot | claude-sonnet-5 | 6/38 | $0.1031 | 18% (2/11) |

Opus through the pipeline scores worse than Sonnet through the pipeline, at
2.7x the price, while the same Opus reading the same bytes in an agent loop
scores nine times higher. So the gap is not the model, not the packet, and
not the prompt.

What it looks like from inside: on `gorefactor-changectx`, which carries
fourteen annotated defects, the Opus one-shot returned fourteen file
summaries, zero comments, and an overview whose last sentence was "The
findings below concern parsing of diff hunk headers, ref quoting for `git
log -L`, and a couple of paths where an expansion can be emitted with a
nil-derived or wrong span." The tool call was complete and well formed. It
announced findings and emitted an empty array. It did not take the sanctioned
silence path either, which the prompt describes as saying so in the overview.

Reasoning before the emission is not the difference either. Opus at
`effort=high` across the same eleven fixtures caught 2/38 at $0.2649 a
review, with the clean rate rising from 64% to 73%: the dial made it quieter,
not better.

### The ladder: what each layer is worth

Four paid sweeps eliminated the model, the prompt, the describing split and
the reasoning dial without naming a cause, which is the cost of debugging a
pipeline by subtraction. `TestLadder` builds one up instead, a layer at a
time, scored by `eval.Score` on the same eleven fixtures at one sample on
`claude-sonnet-5`:

| rung | layers | caught | unlabelled | false positives |
| --- | --- | --- | --- | --- |
| 0 | raw diff + a forty-line instruction | **12/38** | 25 | 0 |
| 1 | the shipped packet + the same instruction | **11/38** | 16 | 1 |
| shipped | packet + system prompt + strict tools | 6/38 | 15 | 0 |

A forty-line instruction and the raw diff, with no panes, no envelopes, no
context and no schema, catches twice what the **one-shot** arm catches on the
same model, the same fixtures and the same sample count.

That bound matters, because one-shot is the weakest shipped arm and the
rungs were never run against the strongest. The best measured configuration
is `--pipeline staged` at three samples, 16/35, which is 46% against rung 0's
32% — so the pipeline leads wherever the comparison is allowed to include
its fan-out, and the equal-samples comparison (rung 0 at ×3 against staged at
×3) has not been run. Nor did any rung include the cohort partition, the
scout's lookups or the ruling: `opts.Verify` is off unless
`REDLINE_EVAL_VERIFY` is set, and it was never set. The denominators differ
too, 38 against 35, because `staged-empty-partition` was added after those
sweeps.

The rungs are also unpriced. `TestLadder` records no usage, so nothing here
supports a claim that a rung is cheaper than an arm, only that it caught
more than one-shot did.

What the ladder does establish is narrower than "simple wins": between the
raw diff and the shipped one-shot call there are layers that cost recall
rather than add it, and the one-shot default is the shape that loses most.

### Which layer, and why shortening the prompt did not fix it

Rung 2 ran once the output contract described the `question` object the
shipped prompt requires: the shipped prompt with the packet, free-form,
caught **5/35 against rung 1's 11/38** and wrote 13 comments against 26. On
`gorefactor-changectx` the long prompt caught none of fourteen where the
short one caught five. With the model, the packet and the output shape held
constant and only that block varying, the 195-line prompt is what costs the
recall, and the strict grammar costs about one point.

The obvious move from there is to put the short prompt in the product. It was
built as `Options.Brief`, swept, and reverted, because it does not transfer:

| prompt | emission | caught | unlabelled |
| --- | --- | --- | --- |
| brief, 40 lines | free-form | **11/38** | 16 |
| shipped, 195 lines | free-form | 5/35 | 9 |
| shipped, 195 lines | strict tools | 6/38 | 15 |
| brief, 40 lines | strict tools | **2/38** | 24 |

Brief and free-form is the only cell that wins. Inside the tool path the
short prompt wrote *more* comments than the long one, 24 unlabelled against
15, and hit fewer annotated defects. The two variables interact rather than
add, so "shorten the prompt" is not the lever and neither is it prompt length
at all: the gain sits with the free-form emission, and the long prompt only
looks harmless there because the grammar was already holding recall down.

That leaves the honest next experiment as free-form output inside the
product - parse the JSON out of a text reply rather than compile a grammar
for it - which is a real change to `anthropicTools` and `absorb`, not a flag.
It is also the one the agent arms have been passing all along, since an agent
writing `review.json` is exactly a free-form emission scored by `Score`.

#### That experiment ran, and the table above did not survive it

Free-form shipped behind `Options.Brief` and was then measured against the
same prompt under the grammar, three samples over all eleven fixtures:

| prompt | emission | caught | false positives | mean cost |
| --- | --- | --- | --- | --- |
| brief, 40 lines | free-form | 17/38 | 2 | $0.1432 |
| brief, 40 lines | strict tools | 15/38 | 1 | $0.1499 |

Two points at 38 expectations and three samples is inside a noise floor of
about 3.6. The whole difference sat in one fixture,
`staged-empty-partition`, which was rerun on both arms at five samples and
caught 1/3 against 0/3: one hit in fifteen trials. The 2/38 cell in the
table above did not reproduce, and at one sample it was never separable
from the 6/38 beside it.

So the short prompt is what the recall follows, and nothing measurable
attaches to the emission. That settles the emission on a different question.
`completeOpenAI` sends the tool catalogue on every call and has no way to be
told otherwise, so free-form only ever existed on the Anthropic wire, and
`--brief` over an OpenAI-protocol proxy was silently a different review from
`--brief` against the API. Brief now runs the short prompt over the grammar
and both wires send the same request.

Three defects fell out of pairing the two, none of which any test covered
because none paired `Brief` with `APIOpenAI`: the context budget zeroed its
tool reservation for every brief run while one wire went on sending the
catalogue, the output format added to bound free-form replies was ignored by
gateways on the wire that most needed it, and the reply-narrowing step keyed
on `Brief` rather than on whether the body arrived in a tool call.

Every number in this section is one sample. The one-shot arm has measured 4,
5, 6 and 2 across today's sweeps on nearly the same material, so a two-point
difference is inside the noise and only the large gaps - 11 against 5, and
the 2x against one-shot - are worth reasoning from.

Rung 1 is the honest measurement of the packet on this set, and it is not the
premise test's: 12 caught against 11 is no recall gain, while unlabelled
comments fall from 25 to 16. The packet buys quiet, not catches. The premise
test's 18 against 14 was one sample on a different model through an agent
loop, so one of the two numbers is sampling noise and the fixture set cannot
say which.

Rung 2 — the shipped system prompt with the packet, free-form — is not
reported here because four of eleven fixtures failed to parse: the shipped
prompt requires a `question` object per comment, which the rung's output
contract did not describe, so the model emitted `question` as a string. That
is a harness gap, not a result, and it leaves the prompt's own contribution
the one layer still unmeasured.

Two things found while building the rungs are worth keeping:

- The shipped system prompt never says to reply with JSON. That contract
  lives entirely in the tool schema, so a prompt sent without its tool
  answers in markdown. The prompt is not self-sufficient; it only works
  welded to the grammar.
- Thinking is on by default. On the larger fixtures it spent the entire 32k
  output budget before emitting one text token and returned
  `stop=max_tokens blocks=[thinking]` — an empty reply that reads as a silent
  model and is not one. Non-streaming calls made it worse by returning no
  content and no error at all.

### What this does not support

Splitting the describing call off is not the fix, and an earlier reading here
that said it was compared `--synopsis` on Sonnet against one-shot on Opus,
which is a model difference wearing a pipeline label. With the model held at
`claude-sonnet-5` across all eleven fixtures at one sample:

| arm | caught | cost/review | unlabelled | walkthrough |
| --- | --- | --- | --- | --- |
| one-shot | 6/38 | $0.1031 | 15 | 52/66 (+10 unsent) |
| `--synopsis` | 6/38 | $0.1175 | 13 | 66/66 |

A tie on recall. The describing split buys a complete walkthrough and two
fewer unlabelled comments for 14% more, which is worth having and is not a
recall fix, so the default stays where it is on this evidence.

The two arms catch nearly disjoint sets, though: one-shot took four
caller/test-role items on `changectx` and nothing on `nil-rules`,
`harness-injected-config` or `staged-empty-partition`, while `--synopsis`
took `empty-roles-are-silent`, `condition-checked-before-facts-invalidated`,
`nil-prepared-map` and `empty-partition-indexes-failures` — the panic. Their
union is about 11/38, near double either arm. At one sample the sampling
noise is larger than the shape effect, which is the same limit the ×3 sweeps
ran into and the reason neither arm can referee the other.

### Fixed here

The system prompt still told the model that "an uncertain finding costs the
reader nothing", the same false claim corrected in `commentSchema()` on
2026-09-12 — the correction landed in the schema description and missed the
prompt, which says the same thing twice. `post.go:172` withholds
low-confidence findings, so an uncertain finding costs the reader the
finding. Both now say so.

### A two-fixture probe, and what it says about relaxing one pass

Eleven fixtures at one sample costs roughly twenty minutes and a dollar an
arm, which is how four arms went by without a cause. Two fixtures carry 31 of
the 38 expectations - `gorefactor-changectx` 14 and `gorefactor-nil-rules`
17 - so a probe over those two holds 82% of the signal for a sixth of the
calls. It reproduces the full sweeps' ordering, which is what makes it usable
as the first instrument rather than the last:

| arm | changectx | nil-rules | /31 | emission |
| --- | --- | --- | --- | --- |
| rung 0, diff + 40 lines | 8 | 2 | **10** | free-form |
| rung 1, packet + 40 lines | 5 | 3 | **8** | free-form |
| rung 2, packet + long prompt | 0 | 4 | 4 | free-form |
| default one-shot | 4 | 0 | 4 | strict tools |
| `--mode explore`, multi-turn | 0 | 2 | 2 | strict tools |
| `--brief` in-product | 0 | 0 | 0 | short prompt + tools |

`--mode explore` had never been scored: it ships, it has a turn budget, and
no arm had ever measured it. It comes last but one, at $0.8614 on
`gorefactor-nil-rules` against the default's roughly $0.10, and on
`gorefactor-changectx` it fetched context and then wrote no comments at all
on a change carrying fourteen annotated defects.

So relaxing the one-pass rule does not help by itself. Explore withholds the
expansions until the model asks, which means its first turn sees *less* than
the default arm does, and what the loop bought was a smaller prompt rather
than more thought. Every tool-path arm sits at the bottom of the table
whatever its turn count, and every free-form arm sits above them.

That leaves the version of the idea this does not test. Explore's
`fetch_context` indexes `kept[n]`, the expansions the packet already
resolved; it cannot grep the tree. Letting the review run the scout's
lookups itself - a real search tool, answers in context, no separate ruling
to reconcile - is untested, and it is the one shape that addresses the
documented binding constraint, the self-certified `diff` question at
`rule.go:266`. The cache makes it affordable in principle: a 170k prefix
re-read at the cached rate is about $0.034 a turn on `claude-sonnet-5`, so
five turns of self-service lookup is cents, not dollars. What it would
collapse is `internal/scout` (1,817 lines) and the ruling stage that exists
to reconcile the scout's answers with findings written before they arrived.
