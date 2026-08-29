# Redline

Evidence from execution, not inference from source.

Redline observes a change at two revisions, diffs the observations, and reports
what it saw. It is pre-push and non-gating. It makes no model calls of its own
and never posts to GitHub — the prose and the judgment come from the agent
driving it. `--pr` fetches via `gh` (read-only).

See `redline-design.md` for the full design.

## Status

**Rung 1** of the check catalog: migration hygiene, no infrastructure at all.
The review loop (packet → agent → HTML report) ships on top of that.

| # | Check | State |
|---|-------|-------|
| 1 | Diff modifies a migration that exists at merge-base | shipped |
| 2 | New migration's version prefix already exists upstream | shipped |
| 3–8 | Migration execution against a real Postgres | not started |
| 9–13 | sqlc staleness, OpenAPI contract | not started |
| 14 | Diff coverage | not started |
| 15–16 | UI pane | not started |

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
internal/findings     wire format — doctor's schema, reimplemented and extended
internal/gitx         git layer (observe; fetch/worktrees for PR/branch)
internal/graph        graphify threads through changed code
internal/instructions repository review rules for the agent packet
internal/packet       contract between Redline and the reviewing agent
internal/pane         the observe/diff pane interface
internal/pane/migrations   checks 1 and 2
internal/report       markdown and self-contained HTML
internal/run          dispatcher
internal/target       working tree, branch, or PR
skills/redline        the agent skill that drives the binary
```
