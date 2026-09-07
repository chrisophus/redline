# Redline

Gates say pass or fail. Redline shows what they saw.

A repository's harness answers each question with one bit: the tests pass,
coverage clears the threshold, the linter is quiet. That bit is the right
shape for a merge queue and the wrong shape for a reviewer, because it drops
the detail a decision needs. Which added lines does nothing execute? Which
lint findings did this change introduce, and which did it inherit? Where did
the author tell the linter to be quiet? Did the number move, and which way?
Redline measures the change and keeps that detail, in one browser view,
beside the facts no gate computes at all: the migrations, the `openapi.yaml`
diff, and a plain account of what was examined and what was not. It hides
what reviewers skip: generated code, test bodies, findings CI already gates.

Redline itself runs no model. Judging the facts is the reviewer's job, and
the reviewer is whoever is reading: a person in the browser, or an agent
reading `findings.json`. They see the same facts and write their decisions
onto the same report, and every finding and every ruling says which of the
two produced it. See `redline-design.md` for the design and the plan.

Reviewing is read-only; posting is not, and never happens on its own.
`redline post` is the one command that writes to GitHub: it submits the
session's observed findings as one pull request review, led by a
one-line-per-pane account of what was checked. `run` never posts.

It is pre-push and non-gating, and it works at both moments: on your own
uncommitted work, and on an open pull request. `--pr` fetches via `gh`
(read-only).

## The page

1. **Interface** - captures when the pane ships; until then, a gap banner,
   present only when the change touches UI
2. **Coverage** - the diff-coverage number, its profile, and its staleness,
   present only when the change has a file a profile could cover
3. **Files** - the walkthrough, with finding counts per file
4. **What Redline observed** - the findings, each with its evidence
5. **Drill in** - per-area diffs with click-a-line comments and viewed state
6. **What could not be determined** - the section most tools omit

## Status

| Check | State |
|-------|-------|
| Diff modifies a migration that exists at merge-base | shipped |
| New migration's version prefix already exists upstream | shipped |
| OpenAPI breaking-change diff (removed operations, removed response codes, newly required inputs) | shipped |
| Diff coverage from an existing profile | shipped |
| Generated-file suppression | shipped |
| Lint delta (golangci-lint, eslint at base and head) | shipped |
| Suppression triage (directives the diff adds) | shipped |
| Lint config drift (rules disabled, downgraded, excluded) | shipped |
| Measured coverage (fresh profile, per-function findings, coverage delta) | not started |
| Migration execution against a real Postgres | not started |
| sqlc staleness, spec-vs-handler agreement, vacuum linting | not started |
| Deterministic UI capture | not started |

## Install

```
make install          # build, symlink the binary onto PATH, symlink the skills
```

Two skills come with it. `/redline` drives a review. `/redline-setup` is
run once per repository: it walks the repo for the analysis tools it
already runs, says which ones Redline picks up on its own, writes the
`.redline.yml` entries for the rest, and suggests tools that apply to the
repo but are not in use. It installs nothing and commits nothing.

The binary and the skills are symlinks into this checkout, so `make install`
once and a later `make build` is all it takes to keep them in step. A
binary older than the skill driving it is the failure mode this prevents.

`BINDIR` (default `~/.local/bin`) and `SKILLDIR` (default `~/.claude/skills`)
override where they go. `make uninstall` removes both.

That installs the skill for every repository on this machine. To commit it
into one repository instead, for teammates without this checkout:

```
make install-repo REPO=/path/to/repo    # copies both skills into .claude/skills/
```

A project skill overrides the personal one. Teammates still need the binary.

## Use

```
go build ./cmd/redline
./redline run                     # observe; markdown report on stdout
./redline run --format json       # findings schema on stdout
./redline run --pr 123            # observe an open pull request
./redline post --pr 123           # post the session's findings as one PR review
./redline post --pr 123 --profile .github/redline-review.yml
./redline post --pr 123 --dry-run # print the review payload instead of posting
./redline open                    # serve and open http://127.0.0.1:8765/report.html
./redline serve --stop            # stop the server for this .redline
./redline gc                      # remove this repo's cached review worktrees
```

`run` writes `.redline/findings.json`, `.redline/report.md`,
`.redline/report.html`, and any captured artifacts under
`.redline/evidence/`. `findings.json` plus git is the whole machine
interface: an agent reviewing the change reads it so it does not re-derive
what Redline measured, and runs git for anything else.

Reviewing a `--commit`, `--branch`, `--range`, or `--pr` checks that revision
out in a detached worktree, cached under `~/.redline/worktrees` and reused
across runs so a second review of the same commit is instant. The cache is
left in place on purpose; `redline gc` reclaims it for the current repository
(`--older-than 168h` keeps recent worktrees).

Target (all subcommands; pass only one): the working tree by default,
`--commit REF` for that commit against its parent (`HEAD` for the latest),
`--range A..B` for a set of commits, `--branch REF` for a branch tip,
`--pr N|URL` for a GitHub pull request.

Flags: `--base REF` (default: commit parent, range start, PR base, else
origin/main), `--upstream REF` (default: same as base), `--migrations DIR`,
`--out DIR`, `--open`, `--no-open`, `--port N` (report server, default 8765).

## Lint

Three panes, scoped to the change, none of which re-reports what CI gates:

- **Lint delta.** When the repository carries a `.golangci.*`, eslint, or
  `.gorefactor.*` config, the linter runs at the base revision (in a cached
  detached worktree) and at head, and only the findings the change
  introduces are reported, each anchored to its head line. Findings the
  change resolves are counted as a confirmation. Identity is file, rule, and
  digit-normalized message with no line number, so moved code does not
  read as new violations. A configured linter that is missing or fails at
  head fails the pane; a base revision that cannot be linted degrades the
  delta to added-line findings and says so.

  Other tools are added in `.redline.yml`, with no code change: a command,
  the globs it covers, and how to read its output (`sarif`, `spectral`, or a
  `json` field mapping). `/redline-setup` writes these entries from what
  the repository already runs. A `kind: differ` tool (oasdiff) is run once
  comparing the file at base and head directly rather than linting one
  snapshot; a `baseline: {mode: file}` tool reads a committed accepted-debt
  file as its base set instead of a second run. See `.redline.yml.example`.
- **Suppressions.** Every silencing directive the diff adds - `//nolint`,
  `eslint-disable` in its forms, `@ts-ignore`, `@ts-expect-error`,
  `# noqa`, `# type: ignore`, `pylint: disable`, `#[allow(...)]` - becomes
  an info finding naming the rule it silences, anchored to the added line.
  Directives that merely moved are not reported.
- **Config drift.** A changed lint config is read on both sides: rules the
  change disables, downgrades, or newly excludes are named individually
  for golangci YAML, eslintrc JSON, and `.eslintignore`; formats Redline
  cannot parse still produce a finding with the config diff as evidence.
  The lint delta's summary also notes when part of the delta may be
  configuration rather than code.

## Posting

`post` submits one review (event `COMMENT` - it reports, it never requests
changes or approves) against the PR the session observed. The body opens
with a verdict derived from finding severities and an evidence table: one
line per pane saying whether it ran and what it found, the diff-coverage
number with its provenance, and how many changed files any pane examined. A
pane that applied and did not run is named there in bold, which is what
makes the review's scope verifiable from the pull request alone.

A finding becomes a line-anchored comment only when its `file:line` is on a
changed line in the PR's diff; findings off the diff (or with no line) go in
the review body, so one stray line can never make GitHub reject the whole
review. It refuses unless the loaded session is that same PR. Posting is
idempotent per `(PR, head SHA)`: every finding carries a hidden marker
naming the commit it was said for, so a re-post never duplicates one and a
re-run with no new findings on an already-reviewed commit posts nothing,
while a new push still gets comments for findings that are still live. `gh`
supplies the credentials; Redline never handles a token. `--report-url`
links the full report (e.g. a CI artifact) from the review body.

`--profile PATH` is how a repo's merge gate reads the review. The YAML names
the hidden markers, which severities fail (default: error and warning), and
whether posting is author-only and must match current PR HEAD. A pane that
applied but did not run fails the gate unless the profile says otherwise: a
missing check must not read as pass. `post` still uses event `COMMENT` and
never approves. A `pass` review with no blocking findings still posts, so a
later HEAD can clear a previous `fail`. Info findings stay visible but do
not get the gate's finding marker. See `redline-review.yml.example`.

## The report server

`run` prints `Report: <url>` on stderr. That URL works in Cursor and
Claude; `file://` often does not. Read the port from that line rather than
assuming 8765: the server identifies itself, so a port already held by
another repository's report is skipped for the next free one, and a server
already serving *this* `.redline` is reused. It shuts down after 30 minutes
idle, or on `redline serve --stop`.

Serving and opening are best effort. If the port cannot be had or no
browser can be launched, `run` warns and still exits 0: the report is on
disk either way, and a completed run must not report failure.

## What never reaches the screen

Generated files leave the change before any pane or the report sees them,
so the coverage denominator counts files a reviewer would actually read.
Detection prefers what generators say about themselves: `git check-attr
linguist-generated`, then `Code generated ... DO NOT EDIT` and `@generated`
markers, then filenames only a generator produces.

Every exclusion is **named** on the report. Hiding a hand-written file is the
one way this can go wrong, and listing them is what makes that recoverable.

Test file contents are not rendered either; they are counted, and the
coverage number stands in for reading them.

A pane the repository has no files for is not mentioned. A report on a
repository with no migrations says nothing about migrations, one with no
API spec says nothing about the contract, and a change with no coverable
file gets no coverage section, because naming an absence the reader already
knows about is noise and it buries the line that matters: a pane that did
apply and could not run, which is always stated. A pane the repository does
have that this change did not reach is listed, folded away, so the reader
can tell "not touched" from "not checked". `findings.json` keeps every pane
with its state (`ran`, `skipped`, `failed`, `not-applicable`) so an agent
reading it knows which panes exist.

## What use taught it

Dogfooding rung 1 on its own repository changed three things, all the same
mistake in different clothes - a report that reads better than the facts
support:

- A change no pane covered rendered as `Findings: None` with "every applicable
  check ran". Reports now carry a `coverage` count and lead with a banner when
  Redline examined none of the change.
- A confirmation said "1 migration file is byte-identical" while its sibling
  was flagged as modified. Confirmations now require the whole set to hold;
  a partial result is an unknown with the denominator stated.
- Check 1 reported `blob a007a0ec → 933f33a1`. It now captures the unified SQL
  diff and shows it beside the finding.

A fourth lesson reshaped the tool itself. Redline once ran the agent's
review: reviewer adapters, a packet of facts for the agent to judge, a
context brief, a Copilot-shaped posted review built from agent prose. None
of it made reviews better than the agents produce on their own, and it was
cut. The agent came back on the other side of the line, as a reviewer that
reads `findings.json` and writes `review.json`, which Redline renders beside
its own facts. The reasoning and the removal inventory are in
`redline-design.md`.

## Layout

```
cmd/redline           CLI
internal/change       the change under review: files, diffs, classification;
                      generated-file detection
internal/cover        diff coverage from an existing profile
internal/findings     wire format: doctor's schema, reimplemented and extended
internal/gitx         git layer (observe; fetch/worktrees for PR/branch)
internal/pane         the observe/diff pane interface
internal/pane/lint         lint delta, suppression triage, config drift
internal/pane/migrations   migration hygiene
internal/pane/openapi      contract breaking-change diff
internal/post         the PR review payload and merge-gate profile
internal/report       markdown and self-contained HTML
internal/run          dispatcher
internal/target       working tree, branch, or PR
skills/redline        the agent skill that drives the binary
skills/redline-setup  the skill that wires a repository's tools into it
```
