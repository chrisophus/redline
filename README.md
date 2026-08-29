# Redline

The reviewer's screen.

Redline assembles, in one browser view, what a reviewer actually looks at — the
ticket, the UI screens, the `openapi.yaml` diff, the migrations, a coverage
number, and a skim of what the reviewers found — and suppresses what they skip.
Generated code, test bodies, and lint CI already gates never appear.

It is pre-push and non-gating, and it works at both moments: on your own
uncommitted work, and on an open pull request. It composes no judgment of its
own — the prose and the judgment come from the agent driving it or from a
reviewer you asked it to run. It never posts to GitHub; `--pr` fetches via `gh`
(read-only).

See `redline-design.md` for the design, and `.planning/PROJECT.md` for what the
screen is for.

## The pass

The page is ordered as the review, not as a findings dump:

1. **Orientation** — the ticket, the pull request, and whether this is the right
   thing built the right way
2. **UI screens** — before / after for the routes the change touches
3. **API contract** — the `openapi.yaml` diff, with breaking changes named
4. **Schema** — the migrations
5. **Coverage** — the number, never the tests
6. **What the reviewers found** — grouped by reviewer, so a skim shows each one
7. **What Redline observed** — the deterministic findings

## Status

| Check | State |
|-------|-------|
| Diff modifies a migration that exists at merge-base | shipped |
| New migration's version prefix already exists upstream | shipped |
| OpenAPI breaking-change diff (removed operations, removed response codes, newly required inputs) | shipped |
| Diff coverage from an existing profile | shipped |
| Generated-file suppression | shipped |
| Migration execution against a real Postgres | not started |
| sqlc staleness, spec-vs-handler agreement, vacuum linting | not started |
| Deterministic UI capture (checks 15–16) | not started — agent walks the UI instead |

## Install

```
make install          # build, symlink the binary onto PATH, symlink the skill
```

Both are symlinks into this checkout, so `make install` once and a later
`make build` is all it takes to keep the skill and the binary in step. A
binary older than the skill driving it is the failure mode this prevents:
it produces packets missing fields the skill expects and reads as a tool bug.

`BINDIR` (default `~/.local/bin`) and `SKILLDIR` (default `~/.claude/skills`)
override where they go. `make uninstall` removes both.

That installs the skill for every repository on this machine. To commit it
into one repository instead — for teammates without this checkout:

```
make install-repo REPO=/path/to/repo    # writes .claude/skills/redline/SKILL.md
```

A project skill overrides the personal one. Teammates still need the binary.

## Use

```
go build ./cmd/redline
./redline run                     # observe; markdown report on stdout
./redline run --format json       # findings schema on stdout
./redline review                  # packet of facts for the agent to judge
./redline review --with claude    # run Claude Code's /code-review and merge it
echo '<review JSON>' | ./redline ingest   # merge judgments; open the HTML report
./redline open                    # serve and open http://127.0.0.1:8765/report.html
./redline serve --stop            # stop the server for this .redline
```

When the packet's `uiTouched` is true, the skill walks the changed UI with
`agent-browser` and ingest embeds those screenshots in the HTML Interface
section (labelled as the agent's walk, not checks 15–16).

`run` and `review` also write `.redline/findings.json`, `.redline/packet.json`,
`.redline/report.md`, `.redline/report.html`, and any captured artifacts under
`.redline/evidence/`.

Target (all subcommands; pass only one): the working tree by default,
`--commit REF` for that commit against its parent (`HEAD` for the latest),
`--range A..B` for a set of commits, `--branch REF` for a branch tip,
`--pr N|URL` for a GitHub pull request.

## Reviewers

`--with NAME` runs an external code reviewer and folds its findings into the
report. Redline composes no judgment of its own: the adapter invokes the tool's
*own* review command — `claude` runs Claude Code's `/code-review` — so what you
get is that tool's review, labelled with its name and its stated confidence.

Repeat the flag or comma-separate to run several. Two reviewers from different
vendors is a second opinion, which is what a second review round used to be when
one arrived on the pull request already.

```
./redline review --with claude --commit HEAD
./redline review --with claude --with cursor      # both, each under its own name
./redline review --with claude,cursor             # same thing
./redline review --with none                      # observed evidence only (the default)
```

A reviewer can take minutes and most print nothing until they finish, so
progress goes to stderr: what started, a tick carrying elapsed time, how much
the reviewer has written, and its own most recent line, then what it produced.
On a terminal the tick rewrites one line; in a pipe it prints every 30 seconds
so a log stays readable.

```
redline: claude is reviewing…
redline: claude reviewing — 45s elapsed, 2 KB out · reading internal/run/run.go
redline: claude finished in 1m32s — 6 findings
```

Elapsed time against bytes written is what separates slow from stuck, which is
why both are on the line. A reviewer that times out reports what it managed to
write first, and one that leaves a child process holding its output open is cut
off rather than allowed to hang past its own deadline.

Findings are grouped by reviewer and never merged across them. Redline cannot
tell whether two differently worded sentences describe the same defect without
guessing, and a wrong guess deletes a finding you never learn existed. Exact
duplicates collapse on fingerprint; the rest sit side by side for you to compare.

Built-in adapters: `claude`, `cursor`. Add or override one in
`<out>/reviewers.json` — no Redline release required:

```json
{"reviewers": {"bugbot": {
  "command": ["my-reviewer", "--json", "{out}", "{target}"],
  "prompt": "review {target}",
  "timeout": "10m"
}}}
```

Placeholders: `{target}` the revision under review, `{out}` where to write
findings, `{schema}` the findings contract, `{prompt}` the adapter's prompt with
those already expanded.

The reviewer runs inside the worktree Redline checked the target out into, so
it reads exactly the tree the evidence describes. A reviewer that is missing,
crashes, or times out is recorded as a substrate that **failed** and listed
under unknowns — a review that did not run never renders as a clean one.

Flags: `--base REF` (default: commit parent, range start, PR base, else origin/main), `--upstream REF`
(default: same as base), `--migrations DIR`, `--out DIR`, `--open`, `--no-open`,
`--port N` (report server, default 8765).

`review` and `ingest` print `Report: <url>` on stderr. That URL works in Cursor
and Claude; `file://` often does not. Read the port from that line rather than
assuming 8765: the server identifies itself, so a port already held by another
repository's report — or by anything else — is skipped for the next free one,
and a server already serving *this* `.redline` is reused. It shuts down after
30 minutes idle, or on `redline serve --stop`.

Serving and opening are best effort. If the port cannot be had or no browser
can be launched, `run` and `ingest` warn and still exit 0: the report is on
disk either way, and a completed review must not report failure.

`ingest` merges into the session `review` recorded — it never re-observes the
tree. If you pass a target flag it must name that same session; ingesting
`--pr 456` into a run of `--pr 123` is an error, not a silent merge.

## What never reaches the screen

Generated files leave the change before any pane or the review packet sees them,
so the coverage denominator counts files a reviewer would actually read and the
reviewing agent does not spend its context on `oas_*_gen.go`. Detection prefers
what generators say about themselves — `git check-attr linguist-generated`, then
`Code generated ... DO NOT EDIT` and `@generated` markers, then filenames only a
generator produces.

Every exclusion is **named** on the report. Hiding a hand-written file is the one
way this can go wrong, and listing them is what makes that recoverable.

Test file contents are not rendered either; they are counted, and the coverage
number stands in for reading them.

## What use taught it

Dogfooding rung 1 on its own repository changed three things, all the same
mistake in different clothes — a report that reads better than the facts
support:

- A change no pane covered rendered as `Findings: None` with "every applicable
  check ran". Reports now carry a `coverage` count and lead with a banner when
  Redline examined none of the change.
- A confirmation said "1 migration file is byte-identical" while its sibling
  was flagged as modified. Confirmations now require the whole set to hold;
  a partial result is an unknown with the denominator stated.
- Check 1 reported `blob a007a0ec → 933f33a1`. It now captures the unified SQL
  diff and shows it beside the finding.

## Layout

```
cmd/redline           CLI
internal/cover        diff coverage from an existing profile
internal/findings     wire format — doctor's schema, reimplemented and extended
internal/gitx         git layer (observe; fetch/worktrees for PR/branch)
internal/graph        graphify threads through changed code
internal/instructions repository review rules for the agent packet
internal/packet       contract between Redline and the reviewing agent;
                      generated-file detection
internal/pane         the observe/diff pane interface
internal/pane/migrations   migration hygiene
internal/pane/openapi      contract breaking-change diff
internal/report       markdown and self-contained HTML
internal/reviewer     adapters that run a vendor's own review command
internal/run          dispatcher
internal/target       working tree, branch, or PR
skills/redline        the agent skill that drives the binary
```
