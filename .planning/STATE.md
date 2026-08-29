---
gsd_state_version: 1.0
current_phase: 1
current_phase_name: The screen from data that exists
status: in_progress
stopped_at: Phase 1 in progress — orientation, pass order, suppression
last_updated: "2026-08-29"
last_activity: 2026-08-29
last_activity_desc: Rewrote the planning set to the reviewer's-screen north star
progress:
  total_phases: 6
  completed_phases: 0
  percent: 0
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-08-29)

**Core value:** Know what the change is, what moved, and what the reviewers found — without reading the source, and without mistaking silence for a pass.
**Current focus:** Phase 1 — the screen from data that exists

## Current Position

Phase: 1 of 6 (The screen from data that exists)
Status: In progress

## Accumulated Context

### Decisions

Logged in PROJECT.md Key Decisions. The ones driving current work:

- Redline is the reviewer's screen, not a source reader.
- Copilot review access on GitHub is going away, so reviewers run locally via `--with`. This makes the adapter registry foundational.
- The screen opens at both moments: pre-push with no PR or ticket, and on an open PR with both.
- Suppression is a feature — generated code, test bodies, and CI-gated lint are absent, not collapsed.
- Agreement between reviewers is marked only on identical fingerprints. Fuzzy matching was built, reverted, and must not return: a loose threshold eats a real finding silently.
- "Evidence from execution" is demoted to v2. Nothing in the reviewer's pass requires executing anything.

### Notes

- Planning is prose from here on. Structured GSD phase plans are no longer generated.
- `.planning/phases/01-briefing-contract/` predates this north star. Its ingest work (intent and surface lines) is folded into Phase 1; the files are kept for history and are not the plan of record.
- Repo requires Go 1.26+ per `go.mod`; developed and tested against Go 1.27.

### Blockers/Concerns

- The report template had a stray `/div>` and an extra `</div>` after the tiles block, closing `.wrap` early so everything below rendered outside the page layout. Fixed in Phase 1.

## Deferred Items

| Category | Item | Status |
|----------|------|--------|
| v2 | Apply migrations to a real database and diff schemas (EXE-01) | deferred |
| v2 | Diff live API responses (EXE-02) | deferred |
| v2 | Deterministic UI capture as a pane (EXE-03) | deferred |
| v2 | Remaining catalog rungs (CAT-01) | deferred |

## Session Continuity

Last session: 2026-08-29
Stopped at: Planning set rewritten; Phase 1 implementation underway
