# Potential enhancements

Ideas for Redline itself: new panes, harness behavior, tool wiring, and
workflow. Report layout and drill-in UX live in
[report-roadmap.md](report-roadmap.md). Phased design rationale is in
[redline-design.md](../../redline-design.md) at the repo root.

Each item is a candidate, not a commitment. Effort is rough: **S** days,
**M** weeks, **L** multi-week.

## Mutation (gomutants v0.6.0+)

gomutants **v0.6.0** (latest stable; MCT pins it in `GOMUTANTS_VERSION`) adds
several features aimed at CI and review workflows. Redline already ingests
`mutants.json` and overlays **LIVED** survivors on changed lines. It does not
yet use the v0.6.0 report fields or statuses below.

### Shipped in gomutants v0.6.0

| Upstream change | Why it matters | Redline today |
|-----------------|----------------|---------------|
| **`INFRA_ERROR` status** ([PR #83](https://github.com/szhekpisov/gomutants/pull/83)) | Per-mutant classification when `go test` fails for environmental reasons (OOM, disk full, too many open files), not because the mutant was caught. Stops silent **KILLED** inflation on flaky CI runners. | **Dropped.** `internal/mutation` only counts `killed` and `lived`; other statuses are ignored. |
| **Stable mutant `id` in JSON** (#86) | Fingerprint like `internal/foo/foo.go:Double:RETURN_ZERO#1` survives rebases better than file+line+type. Enables `gomutants --run-mutant-id` for repro. | **Not read.** `Mutant` struct has no `id` field. |
| **`--changed-since` merge-base scope** (#85) | Mutants limited to lines changed vs the merge base, not an arbitrary ref tip. Matches PR review scope. | N/A (gomutants CLI). MCT `make mutate` already passes `--changed-since origin/main`. |
| **Return-value mutators** (#80) | `RETURN_ERROR_NIL`, wrong-return variants; catches error-swallowing gaps. | Surfaces as ordinary **LIVED** when on added lines (no special labeling). |
| **`--run-mutant-id`** (#87) | Re-run one mutant for debugging after reading the report. | Not wired into "Copy for the agent" or report UI. |
| **`--detect-equivalent`** (existing; MCT uses on `make mutate`) | Marks provably unkillable survivors **EQUIVALENT** after assembly compare. | **Dropped** if status is not `lived`/`killed`. |

### Redline enhancements (gomutants-aware)

| Item | What | Effort |
|------|------|--------|
| **Surface `INFRA_ERROR`** | Count infra failures separately from killed/lived. On changed lines, add `findings.Unknown` (substrate `mutation`) naming file, line, mutant `id`, and that efficacy for that line is unreliable. Mutation section summary: "N infra errors on changed lines; do not treat as killed." Never fold into **KILLED**. | S |
| **Carry mutant `id` in `findings.json`** | Parse `id` from report; expose on each survivor; use as verdict fingerprint key (see verdicts row below). Link text in HTML: "repro: `gomutants --run-mutant-id '…'`". | S |
| **Label mutator types on survivors** | Show `RETURN_ERROR_NIL`, `CONDITIONALS_BOUNDARY`, etc. in the drawer (type is already in JSON as `type`; ensure it renders in markdown/HTML). | S |
| **Report-level efficacy context** | Optional one-liner from top-level `test_efficacy`, `mutants_total`, `mutants_killed`, `mutants_lived` when present, with caveat when `infra_errors > 0`. | S |
| **`EQUIVALENT` / `NOT COVERED` nuance** | **NOT COVERED**: leave to coverage pane (today). **EQUIVALENT**: info finding or muted marker, not a red survivor stripe. | S |
| **Harness: minimum gomutants version** | Setup skill and docs: recommend **v0.6.0+** when mutation profile is enabled; note that pre-0.6.0 reports lack `INFRA_ERROR` and stable ids. | S |
| **CI ingest without local mutate** | MCT `mutation-quality-check` filters daily CI artifacts to changed files (~seconds). Redline could read the same JSON shape from a downloaded artifact path (harness profile `path` only, no produce). Complements local `make mutate`. | M |
| **Verdicts keyed by mutant `id`** | "needs test" / "equivalent" / "acceptable" per survivor; stable across line shifts when id is present. Depends on id field above. | S |

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
| Error-path nuance | Call out uncovered added lines inside error-handling branches separately from other gaps. | S |
| TypeScript / lcov profiles | Read lcov or istanbul output the same way as Go `coverage.out`, including measured mode when the UI test command produces one. | M |
| Test-delta facts (shipped) | Info findings from the diff: a Go package changed with no test in it, a `t.Skip`/`.skip`/`.only` added, or a net drop in assertion-like lines in a test. Pane `internal/pane/testdelta`. | done |
| Per-test attribution | Map a line to the tests that execute it. Needs per-test profiles and real test runs; same data mutation wants. Opt-in command, not default `run`. See report-roadmap. | L |
| CI profile ingest | Read `coverage.out` (or lcov) from a CI artifact URL or path when local produce is too heavy. Harness `from: ci` or env substitution. | M |

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
| Dependency install steps | Worktree `produce` for `node_modules`, `go generate` stubs, or other gitignored dirs linters need. MCT needs `ui/dist` (shipped) and `ui/node_modules` (not yet). | S |
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

## Agent workflow and decisions

| Item | What | Effort |
|------|------|--------|
| Verdict vocabulary expansion | Today: suppressions (justified / should-fix / rule-noisy). Extend to coverage gaps, config drift, migration findings, lint delta. Same fingerprint machinery. Design doc "Decisions" section. | M |
| Verdicts on mutation survivors | `mutants.json` overlays lines; reviewer records "needs test" / "equivalent" / "acceptable" per survivor. Key verdicts on gomutants v0.6.0+ stable `id` when present. | S |
| Post verdicts to PR | Whether human/agent decisions ride on `redline post` or stay local. Open question in design doc. | M |
| Multi-reviewer `review.json` | Merge agent + Bugbot (or two agents) into one report without overwriting. MCT publishes per-reviewer; Redline could key by `reviewer` field. | M |
| `redline serve` live loop | Replace copy-paste "Copy for the agent" with a websocket or stdin bridge. Upgrade path noted in report-roadmap. | L |
| Broader review skill context | Skill text for "why was this nolint added" and whether a rule is noisy. Docs/skills, not binary code. | S |

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
2. Built-in or shared `tsc` JSON wrapper.
3. Setup skill proposals for worktree install steps and harness profiles (mutation profile + v0.6.0 pin).
4. Verdict vocabulary for coverage and lint findings (not only suppressions); mutation verdicts keyed by `id`.
5. Generated / verify-generated drift pane (even if Go-only first).
6. Document CI artifact + `--report-url` posting pattern.
