# Roadmap: The Reviewer's Screen

## Overview

Turn the existing review loop — packet, reviewer adapters, ingest, loopback HTML — into the screen a reviewer actually uses: orientation, UI, API contract, schema, coverage, review comments, info lint. Suppress generated code, test bodies, and CI-gated lint. Reviews come from reviewers run locally, because Copilot review access on GitHub is going away.

Planning is tracked here in prose. Structured per-phase GSD plan files are no longer generated; the phases below are the unit of work.

## Phases

- [ ] **Phase 1: The screen from data that exists** — orientation, pass order, suppression
- [ ] **Phase 2: Two reviewers** — run several in one session, group by reviewer, mark certain agreement
- [ ] **Phase 3: API contract pane** — deterministic `openapi.yaml` diff
- [ ] **Phase 4: Coverage number** — diff coverage, absent when unknown
- [ ] **Phase 5: UI screens** — first-class section with honest absence
- [ ] **Phase 6: Comment-back loop** — close the loop to the agent

## Phase Details

### Phase 1: The screen from data that exists

**Goal:** the report is ordered as the pass, leads with orientation, and hides what the reviewer skips.
**Requirements:** ORI-01 … ORI-05, SUR-01 … SUR-04, SUP-01 … SUP-05, COV-03, COV-04
**Success criteria:**
1. Ingest carries a ticket, an optional PR overlay, a fit statement, and one line per surface; an omitted field keeps its previous value
2. A PR target fills the PR link with no agent involvement; pre-push omits both links and still renders
3. Generated files never reach the packet or the screen, and the count of what was suppressed is stated
4. Test file contents never render; their count does
5. Sections appear in pass order, and the file walkthrough is below them
6. Coverage still leads when nothing was examined

### Phase 2: Two reviewers

**Goal:** run more than one reviewer in a session and let a skim show what each found.
**Requirements:** REV-06, REV-07, REV-08
**Success criteria:**
1. `--with claude --with cursor` (or a comma list) runs both, each recorded as its own substrate
2. Review comments render grouped by reviewer
3. One reviewer failing does not suppress the other's findings, and the failure is recorded
4. Identical fingerprints across reviewers are marked as agreement; differently worded findings are never merged

### Phase 3: API contract pane

**Goal:** the `openapi.yaml` diff a reviewer always reads, computed rather than eyeballed.
**Requirements:** API-01 … API-04
**Success criteria:**
1. Added, removed, and changed operations are named by method and path
2. A removed operation, a removed response code, or a newly required request field is marked breaking
3. No spec in the change means the pane does not apply and the section reads as absence

### Phase 4: Coverage number

**Goal:** the number that stands in for reading the tests.
**Requirements:** COV-01, COV-02
**Success criteria:**
1. A Go coverage profile in the tree yields diff coverage over the changed lines
2. No profile means the number is absent and stated as absent

### Phase 5: UI screens

**Goal:** the one artifact nobody else hands you, in pass position.
**Requirements:** UI-01 … UI-04
**Success criteria:**
1. Screens render in pass position with before/after pairing when both exist
2. No captures reads as "the interface was not looked at," never as unchanged
3. Agent captures are labelled as the agent's walk

### Phase 6: Comment-back loop

**Goal:** the Prequel move — comment on the screen, hand it to the agent.
**Requirements:** CMT-03, CMT-05
**Success criteria:**
1. The copied payload names the target and review identity alongside each `path:line` comment
2. The skill tells the agent to consume a pasted payload and address each item

## Progress

| Phase | Status |
|-------|--------|
| 1. The screen from data that exists | Not started |
| 2. Two reviewers | Not started |
| 3. API contract pane | Not started |
| 4. Coverage number | Not started |
| 5. UI screens | Not started |
| 6. Comment-back loop | Not started |
