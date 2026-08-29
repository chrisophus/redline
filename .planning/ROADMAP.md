# Roadmap: Redline Interactive Briefing

## Overview

Grow the existing review loop — packet, ingest, loopback HTML — into the locked briefing: intent plus three surface tiles, a drawer that does not leave the page, and a local Fix queue the agent skill can pick up. Observation rungs 2–6 and a React compile path stay out.

## Phases

- [ ] **Phase 1: Briefing contract** - Ingest carries intent and surface lines; merge rules match `summary`
- [ ] **Phase 2: Briefing page** - Opening split (001-B) and drawer (002-B) in `report.html`
- [ ] **Phase 3: Fix queue** - Queue JSON, copyable brief, skills fill the new fields

## Phase Details

### Phase 1: Briefing contract
**Goal:** The agent can send why the change exists and what each surface did, and a second ingest does not blank those fields.
**Mode:** mvp
**UI hint:** no
**Depends on:** Nothing (first phase)
**Requirements:** AGT-01, AGT-03, INT-01, INT-02, INT-03, INT-04
**Success Criteria** (what must be TRUE):
  1. Ingest JSON can include goal, optional PR, optional ticket, and one line each for Interface / API / Schema
  2. A follow-up ingest that omits those fields keeps the previous values (same merge rule as `summary`)
  3. `redline ingest` still never calls a model; malformed extra fields fail closed
  4. Existing file walkthrough, screenshots, and findings ingest still work
**Plans:** 2 plans

Plans:
- [ ] 01-01-PLAN.md — Persist intent and surfaces through parse and omit-keep merge
- [ ] 01-02-PLAN.md — Fail closed on bad types; empty objects and omitted links keep prior values

### Phase 2: Briefing page
**Goal:** Opening the report orients in about ten seconds; drill is a drawer, not a new page.
**Mode:** mvp
**UI hint:** yes
**Depends on:** Phase 1
**Requirements:** SURF-01, SURF-02, SURF-03, SURF-04, DRL-01, DRL-02, DRL-03, DRL-04, COV-01, COV-02, COV-03
**Success Criteria** (what must be TRUE):
  1. The opening page is a split command: intent left, Interface / API / Schema tiles right
  2. Idle tiles read as "did not move," not as a pass; coverage still leads when examined is zero
  3. Clicking a tile or an intent link opens a drawer with evidence and findings; Escape / scrim / Keep briefing closes it
  4. Observed vs judged labels remain visible on the briefing and in the drawer
  5. `redline open` still serves one self-contained HTML file over loopback
**Plans:** TBD

Plans:
- [ ] 02-01: Render briefing from ingest + packet (locked 003 layout)
- [ ] 02-02: Drawer interactions and coverage honesty
- [ ] 02-03: Serve/open still works in Cursor and Safari

### Phase 3: Fix queue
**Goal:** Fix starts agent work without the browser spawning an IDE.
**Mode:** mvp
**UI hint:** yes
**Depends on:** Phase 2
**Requirements:** FIX-01, FIX-02, FIX-03, AGT-02
**Success Criteria** (what must be TRUE):
  1. Fix this writes `.redline/queue/<id>.json` for that finding
  2. A paste-ready brief is copied; the page does not attempt to launch Cursor or Claude
  3. Cursor, Claude, and Copilot skills tell the agent to fill intent and surface lines, and to address a queued item
**Plans:** TBD

Plans:
- [ ] 03-01: Queue write + copy brief from the drawer
- [ ] 03-02: Update agent skills and default guidance

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Briefing contract | 0/2 | Not started | - |
| 2. Briefing page | 0/3 | Not started | - |
| 3. Fix queue | 0/2 | Not started | - |
