---
name: redline-setup
description: Set up Redline in a repository. Walk the repo for the analysis tools it already runs (linters, coverage, OpenAPI checks, migration and SQL tools), say which Redline picks up on its own, wire the rest into .redline.yml, and suggest tools that apply to the repo but are not in use. Use when asked to set up, configure, or onboard Redline, or when `redline run` says no pane examined the change.
---

# redline-setup

Redline shows the detail behind a repository's gates: the lines a linter
flagged, the added lines no test ran, the rules a change silenced. It can
only show what it can run. This skill finds what the repository already
runs, connects it, and names what is missing.

The result is a `.redline.yml` that reflects the tools this repository
actually uses, an inventory the owner can read, and a short list of tools
worth adding. Nothing is installed and nothing is committed by this skill.

## 1. Inventory what the repository runs

Read, in this order, and note every analysis tool each one mentions:

- CI: `.github/workflows/*.yml`, `.gitlab-ci.yml`, `Jenkinsfile`,
  `.circleci/config.yml`, `.buildkite/`.
- Task runners: `Makefile`, `Taskfile.yml`, `justfile`, `package.json`
  scripts, `pyproject.toml` tool sections, `tox.ini`, `noxfile.py`.
- Hooks: `.pre-commit-config.yaml`, `lefthook.yml`, `.husky/`,
  `.githooks/`.
- Tool configs at the root and one level down: `.golangci.*`,
  `.eslintrc*`, `eslint.config.*`, `biome.json`, `.gorefactor.*`, `.gomutants.yml`,
  `.spectral.*`, `redocly.yaml`, `.sqlfluff`, `.squawk.toml`, `atlas.hcl`,
  `sqlc.yaml`, `.hadolint.yaml`, `.shellcheckrc`, `ruff.toml`, `mypy.ini`,
  `.tflint.hcl`, `buf.yaml`.
- A committed `.redline.yml`, if there is one already. Extend it; do not
  replace entries the owner wrote.

For each tool found, record where it is invoked, the exact command and
flags, and whether it is on PATH here (`command -v <tool>`). A tool the
repository configures but this machine lacks matters: Redline reports a
configured tool that will not run as a pane that failed, which is what
the owner should see rather than a silent gap.

Then look at what the repository is made of, because that decides which
tools apply: languages by file count, an `openapi*` or `swagger*` spec,
migration files named `<version>_<name>.up.sql`, `Dockerfile`s, shell
scripts, GitHub workflows, `.proto`, Terraform.

## 2. What Redline picks up with no configuration

Say this plainly in the inventory so the owner does not wire what is
already wired:

- golangci-lint, when `.golangci.yml`, `.yaml`, `.toml`, or `.json` exists.
- eslint, when any `.eslintrc*` or `eslint.config.*` exists.
- gorefactor, when `.gorefactor.yaml` or `.yml` exists.
- Diff coverage, from a Go profile at `coverage.out`, `cover.out`,
  `coverage.txt`, `c.out`, `coverage/coverage.out`, or
  `.coverage/coverage.out`. If the test target writes its profile
  somewhere else, suggest pointing it at one of these names; Redline
  reads the file the harness leaves behind and does not run the tests.
  lcov and istanbul output are not read yet; say so if the repository
  produces them.
- The OpenAPI breaking-change diff, for any `.yaml`, `.yml`, or `.json`
  file with `openapi` or `swagger` in its name.
- Migration hygiene (an existing migration edited, a version prefix that
  collides with upstream), for files in the golang-migrate naming
  convention. Pass `--migrations DIR` when the repository has SQL files
  in that shape that are not migrations.
- Suppression triage and lint config drift need nothing.

For coverage, also add a `harness:` section when the repository has a make
target or script that writes the profile. `redline run --prepare` runs
`produce` when the artifact is missing or stale relative to this change.
See `.redline.yml.example` for the schema (`when: stale|missing|always`,
optional `scope` globs, optional `env.from` script sourced before each
produce step). Point `path` at `coverage.out` (or where the suite writes)
and `produce` at the command CI uses, written `{command, args}` (for example
`{command: make, args: [test-coverage]}`).

When the repository embeds built assets for Go compile (for example
`ui/embed.go` with `//go:embed dist`), add `harness.worktree` steps that
run in the tree under review before linters execute. Detached PR worktrees
do not carry gitignored build dirs, so the same precondition Make uses
(`make stub-ui`, `_ensure-ui-embed`) must run there. Use `when: missing`
and point `path` at the file the embed needs (for example
`ui/dist/index.html`). Do not put these in `profiles:`; they are not
artifacts Redline reads from the origin checkout.

Mutation testing (gomutants), when `.gomutants.yml` exists or the
repository has a `make mutate` target: add a `harness.profiles` entry
with `path: mutants.json` (or `mutation-report.json`) and no `produce`
when you want the report required for matching changes. Redline never runs
gomutants and `--prepare` does not produce mutation reports. Ensure the
repository's mutate command passes `-o mutants.json`. Run mutate by hand
before `redline run` when the profile is configured.

For each of these, confirm the binary is on PATH and, for coverage, that
the profile exists and is fresh. Those are the two ways a built-in pane
fails in practice.

## 3. Wire everything else through `.redline.yml`

Every other tool becomes an entry under `tools:`. The schema is in
`.redline.yml.example` next to the binary's source, and the rules that
matter when writing an entry:

- Prefer `format: sarif` when the tool can emit SARIF; it needs no field
  mapping. Use `format: spectral` for Spectral and Spectral-compatible
  OpenAPI linters. Otherwise `format: json` with `resultsPath` and a
  `fields` mapping; each field is a dotted path and may index an array
  (`messages[0].Note`).
  A per-file report that nests results under each file (sqlfluff's default,
  for one) needs `itemsPath`: the dotted path to the inner array, fanned out to
  one finding per item with fields read from the item then its group.
- `scope` is the globs this tool speaks for. A changed file outside every
  tool's scope is reported as unexamined, so scope should match what the
  tool actually reads.
- `kind: differ` for a tool that compares two revisions itself, like
  oasdiff. `{{base}}` and `{{head}}` in `args` become the two file paths.
- `lineOffset: 1` for a tool that numbers lines from zero.
- `okExitCodes` when the tool exits non-zero on findings with a code other
  than 1.
- `baseline: {mode: file, file: ...}` when the repository commits the
  tool's own accepted-debt file, so Redline reads that instead of running
  the tool a second time at the base revision.
- Redline runs a `linter` entry at the merge base too, in a detached
  worktree with no build step. The command must work from a bare checkout.
  A tool that needs `node_modules` or a compiled binary from the tree
  needs that noted in the entry's comment, and the owner told.

Never guess a tool's output shape. Run it once on this repository with
the flags you intend to use, read the output, and write the mapping from
what you saw. Check flags against `<tool> --help`; the recipes below are
starting points and versions drift.

### Recipes

OpenAPI. vacuum and oasdiff are in `.redline.yml.example` as written.
Spectral itself: `spectral lint <spec> -f json` with `format: spectral`
and `lineOffset: 1`. Redocly: `redocly lint <spec> --format=json`, then
map its nested output with `format: json`.

Postgres migrations. squawk lints migration files for operations that
lock or fail on a live table. Its JSON reporter emits one object per
finding with `file`, `line`, `level`, `rule_name`, and a `messages` array
whose first element carries the note. Redline runs a `linter` entry's
command directly, with no shell, so a wildcard in `args` does not expand
and nothing substitutes file paths. Give the tool a directory it walks
itself, or commit a one-line wrapper script that expands the glob:

```sh
#!/bin/sh
# scripts/squawk-json: lint every migration, JSON on stdout.
exec squawk --reporter json db/migrations/*.sql
```

```yaml
  - name: squawk
    command: scripts/squawk-json
    scope: ["db/migrations/*.sql"]
    format: json
    resultsPath: ""
    fields: {file: file, line: line, rule: rule_name, message: messages[0].Note, severity: level}
    severityMap: {Error: error, Warning: warning}
```

The wrapper has to exist at the merge base too, or the base run fails and
the delta degrades to added-line findings. Say so in the report when the
wrapper is new.

eugene does the same static analysis and can also trace real locks against
a database; the static mode is the one that fits a `linter` entry. atlas
`migrate lint` needs a dev database URL and compares against a base the
way oasdiff does, so it is a `differ` if the repository uses Atlas.

SQL style. sqlfluff with `--dialect postgres` and its flat GitHub
annotation output (`--format github-annotation`), mapped with
`format: json`: `file`, `start_line`, `message`, `annotation_level`, and
`title` as the rule. Its default JSON nests violations under each file; map
that with `itemsPath: violations`, `file` reading `filepath` from the group and
`line`, `rule`, `message` reading `line_no`, `code`, `description` from each.

Other linters that emit SARIF or flat JSON and slot in with a scope glob:
semgrep (`--sarif`), gosec (`-fmt sarif`), trivy (`--format sarif`),
ruff (`--output-format sarif`), hadolint (`-f sarif`), shellcheck
(`-f json`), actionlint (`-format '{{json .}}'`), tflint
(`--format sarif`), buf lint (`--error-format json`), pylint
(`--output-format=json`), mypy (`--output json`).

## 4. Suggest what is missing

Suggest a tool only when the repository has the files it reads and does
not already run something in the same family. Prefer the family the
repository already chose: a repo on vacuum does not need Spectral
suggested. The pairings that earn a suggestion:

- Go: golangci-lint if absent; a coverage profile written by the test
  target if none is.
- TypeScript or JavaScript: eslint or biome; `tsc --noEmit` through a
  wrapper that emits JSON.
- Python: ruff; mypy if the code is typed.
- An OpenAPI spec: a linter (vacuum or Spectral) and oasdiff for
  breaking changes, since Redline's own contract diff covers removed
  operations, removed response codes, and newly required inputs only.
- Postgres migrations: squawk or eugene. Migration tests prove a
  migration applies; these say whether it locks a table while it does.
- sqlc: `sqlc vet` if `sqlc.yaml` exists and no vet rules are configured.
- Dockerfiles: hadolint. Shell scripts: shellcheck. GitHub workflows:
  actionlint. Terraform: tflint. Protobuf: buf lint.

For each suggestion say what it would show on the report that the
repository's current gates do not, in one sentence, and give the install
command. Do not install it.

## 5. Validate

After writing `.redline.yml`, run `redline run --prepare` on the working
tree (or `--commit HEAD` in a clean tree) when harness profiles are
configured, else `redline run`, and read `.redline/findings.json`:

- Every tool you wired appears in `tools[]` with `status: ran` (or `degraded`
  when its base run or baseline failed and the delta fell back to added-line
  findings). A tool missing from `tools[]`, or the whole lint pane failing in
  `substrates[]`/`unknowns[]`, means the command, flags, or mapping is wrong;
  fix it before reporting the tool as wired.
- `coverage.examinedFiles` against `coverage.changedFiles` tells you
  whether scope globs cover what changed.
- When the repository uses gomutants, run its mutate target once (with any
  env it needs, e.g. database DSNs), confirm `mutants.json` or
  `mutation-report.json` exists at the repo root, then re-run redline and
  check `findings.json` for `mutation` and the Mutation section on the report.
- If the change touched no file any tool covers, make a throwaway edit to
  a file in scope, run again, and revert it, so the validation exercised
  the tool.

## 6. Report

Lead with a table, one row per tool: name, where the repository runs it,
on PATH here, and its Redline status (built in, wired now, suggested, or
does not apply). Below it, the `.redline.yml` you wrote, anything the
owner must install or change in their Makefile or CI for a pane to run,
and the suggestions with their one-sentence justification.

## Do not

- Do not install tools or change CI, the Makefile, or a linter's own
  config. Propose the change and show it.
- Do not commit. The owner reviews `.redline.yml` and commits it.
- Do not report a tool as wired until a run showed its pane ran.
- Do not write a field mapping from memory. Run the tool and read its
  output first.
