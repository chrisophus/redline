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

## Use

```
go build ./cmd/redline
./redline run                     # observe; markdown report on stdout
./redline run --format json       # findings schema on stdout
./redline review                  # packet of facts for the agent to judge
echo '<review JSON>' | ./redline ingest   # merge judgments; open the HTML report
./redline open                    # serve and open http://127.0.0.1:8765/report.html
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

`review` and `ingest` print `Report: http://127.0.0.1:8765/report.html` on
stderr. That URL works in Cursor and Claude; `file://` often does not.

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
