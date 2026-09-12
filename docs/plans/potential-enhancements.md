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
| **The scout is not a cheap model** | `scout.DefaultModel` and `review.DefaultModel` are both `claude-sonnet-5`. Three doc comments and `scout/prompt.go:153` — which is in the reviewer's prompt — tell the reader and the model that a "smaller"/"cheap" model chose the context. `scout.go:19` guarantees the model writes only locations while the program reads the bytes, which is exactly what makes a weak model safe there. | S |
| **The answerer passes no model, effort, or cost cap** | `cmd/redline/review.go` built `scout.Options` with root, diff, questions and credentials only, so `--model` and `--effort` could not reach the lookups. Both now pass through. The cost cap was never off: the scout applies its own default of a quarter to a zero `MaxCostUSD`, and it still does. | done |
| **No test covers a review that asks nothing** | Stage one returning zero answerable questions is reachable (measured twice) and untested: no fixture, no unit test. It is the path where the verifying pass costs nothing and does nothing, and today it reports the same way as a pass that ran. | S |

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
| **Stage-one confidence gates posting; the ruling's verdict does not** | `post.go:117-121` withholds a finding "because they said they were unsure", reading the reviewer's own pre-verification confidence. On this run the only genuine logic defect in the packet — `record()` checking its cap before the retag dedup — was rated `low` by stage one, *confirmed by the ruling with a quoted line from the diff*, and withheld from the pull request anyway. The checking pass is paid for and then ignored at the gate. Confidence at the gate should come from the ruling: verified-with-evidence posts, could-not-verify withholds, and stage-one confidence becomes a hint for the scout. | S |
| **The reviewer cannot contradict a deterministic finding** | Two `sibling-missing-file` findings claimed a capability was tested on one side and not the other; `internal/scout/brief_test.go` — in the diff the reviewer read — tests exactly that capability on the other wire. The reviewer can write a `correlation` finding that *builds on* a pane finding but has no way to say one is wrong, so both false positives reached the report unchallenged and made packet precision 78% against 3/3 on the agent's own findings. Wants a rebuttal in the review schema, verified by the scout like any finding, never over an ERROR gate. | M |
| **Parity read a test file as a missing capability** | A `_test.go` file is named after the unit it tests, not after a capability a sibling ought to match, so filename comparison inverts there. `internal/pane/parity` now skips test files; source parity, which is the pane's purpose, is unchanged. | done |
| **An ERROR one line over a limit outranks a logic bug** | The packet's only ERROR was `file-size`: 501 lines against a limit of 500. The behavioural defect was INFO. `lint/delta.go:535-547` passes the tool's severity through deliberately and should keep doing so — gating is what severity is for — but the report and the pull request body order by it, so a reader meets the line count before the bug. Order presentation by finding class first, severity second. | S |
| **Model-written prose reaches the report unvalidated** | The ruling emitted `…the finding claims.dependencies.python.org.(placeholder) (internal/scout/scout.go diff: …)` into a verdict. Traced through the captures: absent from both requests and from the review response, present only in `05-ruling.response.json`, so the model generated it — and byte-identically on the previous run. It rendered into `report.md` and would have reached a pull request comment had that finding been inline. A tripwire on bare domains and placeholder-shaped fragments in `source: "llm"` text is cheap and stops posting garbage under the repository's name. | S |
| **The ruling writes the prefix again instead of reading it** | The review call wrote 171,690 cache tokens; the ruling thirteen seconds later wrote another 170,601 and read **zero**. Not divergence: the response schema is part of the cached prefix and sits in front of the system block, so a stage that sends its own schema cannot read another's entry however the prompts are arranged. Measured on the wire — one system, one prompt, review schema then ruling schema: wrote 12,449 then 11,360, read nothing either time; the 1,089-token gap is the gap between the two schemas, the same figure the real run showed. A schema-less call misses too (its prefix has no schema), and `oneOf` is rejected outright (`Schema type 'oneOf' is not supported`), so one strict schema serving both shapes is not available. Samples cannot read each other either: three concurrent calls over a fresh prefix each wrote it. Nothing in a one-shot run ever read either entry, and a prompt carrying 14 timestamps and 13 SHAs never repeats across runs, so both writes were a quarter above base input for nothing. Both breakpoints removed; explore and the scout keep theirs, and that half is no longer an assumption: `TestExploreCacheProbe` sends explore's prefix twice in the shape `runExplore` builds it, and turn two read back all 5,437 tokens at 0.1x with only the 21 new tokens billed at full rate. The probe is committed and opt-in, so the next person to ask whether explore's breakpoints should come off too has the answer rather than the reasoning. Saves $0.171 of a $1.369 run at one sample and scales with `--samples`. The read itself is still on the table and costs a shared schema — see below. | done |
| **The governor prices the first turn at the wrong rate** | `overBudget` (now `internal/scout/cache.go`) discounts the prefix once the wire reports cached tokens, which is right, but prices the whole input at 1× on turn zero — the turn that always *writes* the cache at 1.25× (`review/cost.go:98`). The ceiling for the first turn is understated by a quarter of the prefix. The inverse of the error PR #37 fixed, in the function it rewrote. Small in dollars, wrong in the same way. | S |
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
| **A refused record costs a turn like an accepted one** | Both refusals above landed on the scout's last turn, and the transcript ends there: no retry, no `done`, no note saying what went unanswered. A rejection is a correction the scout is meant to act on, which it cannot do with no turn left. Either refusals should not draw on the turn budget, or the last turn should be reserved for filing. | S |
| **The scout cannot see anything outside the tree** | The question that mattered most on PR #38 — does this line panic on a canceled batch item — was about `anthropic-sdk-go`. The scout's grep returned `no matches` because the module cache is outside the root it walks, and the ruling recorded `unverifiable`. A dependency type is a `type` question like any other, and answering it needs either the module cache in scope or an honest "outside the tree, cannot check" that the ruling can read as an answer rather than a silence. | M |
| **The scout answers the letter of a caller question** | Asked whether `Assemble()` builds the system prompt for explore mode, the scout recorded the dispatch site (`review.go:474-485`) rather than what the callee does. The answer was `explore.go:155`, which builds its own system and never sends `res.System`. The ruling said so exactly: "neither snippet shows what runExplore does with res.System." A caller question is usually answered by the callee, and the brief should say so. | S |
| **`Truncated` was spelled in one API's vocabulary** | `Entry.Truncated` was computed as `r.StopReason == string(anthropic.StopReasonMaxTokens)`, so a run over `--api openai` that hit its cap (`finish_reason` `length`, which `openai.go:234` already sets) recorded `Truncated: false` — silently defeating the bookkeeping the field was added for. The completion already carried `truncated` from both paths; `Result` now carries it too and the ledger reads it instead of re-deriving it from a string. Caught by the model, missed by hand. | done |
| **A canceled batch item reported no reason** | Only the errored variant of `MessageBatchResultUnion` carries a message, so a canceled or expired item produced "came back expired:" with nothing after the colon. Worth recording that the finding which caught this claimed a nil-dereference panic and was wrong — the SDK's `Error` is a value struct — but the empty message underneath it was real. | done |
| **`read_lines` and `grep` restated their caps by hand** | `record` builds its description from `ts.limits` precisely so the stated limit cannot drift from the enforced one; the other two tools spelled `200` and `60` as bare literals in both the description and the call site. Now `maxReadLines` and `maxGrepMatches`, used by both. | done |
| **Effort is a cost dial, not a quality dial** | Three arms over ten fixtures, byte-identical but for `output_config.effort`: low $0.0476/review and 4/32 caught, medium $0.0667 and 2/32, high $0.1126 and 5/32, with zero false positives throughout. 2.4x the price buys 4 -> 5, and the middle arm caught fewer than the cheapest; a monotonic quality curve does not look like 4/2/5. The whole spend is reasoning — one recorded run billed 23,853 output tokens against 8,877 bytes of visible response, ~90% of it never returned. No default was changed on this: n=1 cannot separate the effect from noise on the two fixtures carrying 29 of the 32 labelled defects. What it does settle is the negative — there is no case for `high`. `--samples 3` (about $7 at the batch tier) is what would settle the rest. | — |
| **Sweeps now cost half** | The Message Batches tier takes 50% off every token in both directions. Its twenty-four-hour window is an expiry rather than a promise, which rules it out for `redline review`, and fits the eval sweep exactly. `RunBatch` refuses what has no batched form — explore, `--samples`, the checking pass — rather than discovering it on the wire, and prices the tripwire at the tier that will bill it. The effort sweep above cost $2.27 where it would have cost $4.54. This is what makes the repeated sweeps affordable that every other cost question needs. | done |
| **The batch path is the least-tested code in the tool** | 115 of `batch.go`'s lines are unexecuted by the suite: everything past the guards, including the invariant its own comment names — results arrive out of order and are placed by `custom_id`. `Options.BaseURL` and the package's existing `httptest` helpers make a fake batch endpoint returning reversed results reachable without new machinery. `verifyCeilingCost` is likewise uncovered, and it is the function that changed what the tool refuses. | S |

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
| Agent-comment coverage skew | On this PR the agent left 5 comments across 4 files, skewed to two SQL predicate files, while Copilot spread across the diff. Check whether the reviewer is capped per file or per finding-count in a way that starves later files. | S |
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

9. **Gate posting on the ruling's verdict, not stage-one confidence** — the
   highest-leverage item measured so far. A verified finding was withheld
   from a pull request because the model that wrote it, before any
   verification, said it was unsure.
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

12. **Give a refused record a turn to be corrected** — the cheapest fix
    measured so far. The role-vocabulary bug is fixed, but the shape that made
    it invisible is not: a rejection landed on the last turn, so a correction
    the scout was told to make had nowhere to go, and the finding came back
    unverifiable on evidence already in the transcript.
13. **Let the scout say "outside the tree"** — a dependency question currently
    returns `no matches`, which the ruling cannot distinguish from "searched
    and found nothing". The honest negative is an answer; the silence is not.
