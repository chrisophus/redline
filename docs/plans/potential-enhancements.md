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
| **High effort asks no questions, so verification never runs** | `--effort high` chose the `diff` kind for every finding in both runs, asked zero questions, and the ruling decided alone. `--effort low` asked two. Effort silently switches the verifying pass off. Worth an eval arm before any default changes; `REDLINE_EVAL_EFFORT` already exists. | S |
| **The scout is not a cheap model** | `scout.DefaultModel` and `review.DefaultModel` are both `claude-sonnet-5`. Three doc comments and `scout/prompt.go:153` — which is in the reviewer's prompt — tell the reader and the model that a "smaller"/"cheap" model chose the context. `scout.go:19` guarantees the model writes only locations while the program reads the bytes, which is exactly what makes a weak model safe there. | S |
| **The answerer passes no model, effort, or cost cap** | `cmd/redline/review.go` built `scout.Options` with root, diff, questions and credentials only, so `--model` and `--effort` could not reach the lookups. Both now pass through. The cost cap was never off: the scout applies its own default of a quarter to a zero `MaxCostUSD`, and it still does. | done |
| **No test covers a review that asks nothing** | Stage one returning zero answerable questions is reachable (measured twice) and untested: no fixture, no unit test. It is the path where the verifying pass costs nothing and does nothing, and today it reports the same way as a pass that ran. | S |

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
