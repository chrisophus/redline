# Redline

Evidence from execution, not inference from source.

Redline observes a change at two revisions, diffs the observations, and reports
what it saw. It is pre-push, non-gating, and makes no network calls — the prose
and the judgment come from the agent driving it.

See `redline-design.md` for the full design.

## Status

**Rung 1** of the build order: migration hygiene, no infrastructure at all.

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
./redline run                     # markdown report on stdout
./redline run --format json       # findings schema on stdout
```

Both forms also write `.redline/findings.json`, `.redline/report.md`, and any
captured artifacts under `.redline/evidence/`.

Flags: `--base REF` (default: origin/main, else main), `--upstream REF`
(default: same as base), `--migrations DIR`, `--out DIR`.

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
internal/gitx         read-only git layer
internal/pane         the observe/diff pane interface
internal/pane/migrations   checks 1 and 2
internal/report       four-section markdown renderer
skills/redline        the agent skill that drives the binary
```
