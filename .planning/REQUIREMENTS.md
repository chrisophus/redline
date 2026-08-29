# Requirements: The Reviewer's Screen

**Defined:** 2026-08-29
**Core Value:** Open the report and know what this change is, what moved on the surfaces that matter, and what the reviewers found — without reading the source, and without mistaking silence for a pass.

**Reviewer model (locked):** The human does not read Go or TypeScript. The screen is orientation (ticket, PR, the reviewer's account of the change), UI screens, the `openapi.yaml` diff, the SQL migrations, a coverage number, a skim of review comments, and info-level lint. Locally-run reviewers own the code details. Generated code, test bodies, and CI-gated lint never appear.

## v1 Requirements

### Orientation

- [ ] **ORI-01**: The screen leads with the reviewer's account of what this change does
- [ ] **ORI-02**: A supplied ticket shows its id, title, and link
- [ ] **ORI-03**: When the target is a PR, its number, title, and link show without the agent supplying them
- [ ] **ORI-04**: Missing ticket or missing PR omits that link and does not break the screen — the pre-push case
- [ ] **ORI-05**: The agent can state whether this is the right thing the ticket asked for and the right way to build it

### Surfaces, in pass order

- [ ] **SUR-01**: The screen orders sections as the pass: orientation, UI, API, schema, coverage, review comments, info lint
- [ ] **SUR-02**: A surface that moved states in one line what moved
- [ ] **SUR-03**: A surface that did not move reads as absence, never as a pass
- [ ] **SUR-04**: The file-by-file walkthrough is reachable but is never the lead

### Suppression

- [ ] **SUP-01**: Generated files are detected and excluded from the packet and the screen
- [ ] **SUP-02**: Generated-file detection honours `.gitattributes` `linguist-generated`, plus conventions for ogen, sqlc, lockfiles, and `go.sum`
- [ ] **SUP-03**: Test file contents never render; their presence is reported as a count
- [ ] **SUP-04**: Warning and error lint findings from external linters are not the screen's content; info level is
- [ ] **SUP-05**: What was suppressed is stated, with counts — suppression is never silent

### API contract

- [ ] **API-01**: A deterministic pane diffs `openapi.yaml` between base and head
- [ ] **API-02**: Added, removed, and changed operations are named by method and path
- [ ] **API-03**: A removed operation, a removed response code, or a newly required request field is marked breaking
- [ ] **API-04**: No spec in the change means the pane does not apply, and the API section reads as absence

### Schema

- [ ] **SCH-01**: Migration files in the change are shown as the schema surface with their SQL diff
- [ ] **SCH-02**: Rung 1 hygiene holds: a modified merged migration and a colliding version prefix are findings

### Coverage

- [ ] **COV-01**: A diff coverage number is shown: of the changed lines that could be covered, how many are
- [ ] **COV-02**: No coverage profile means the number is absent and stated as absent, never zero and never a pass
- [ ] **COV-03**: The screen shows how many files Redline examined against how many changed
- [ ] **COV-04**: Zero examined files with an empty findings list is never presented as a pass

### Reviewers

- [ ] **REV-01**: `--with <tool>` runs a configured reviewer's own review command and folds its findings in; `--with none` keeps the observed-only path
- [ ] **REV-02**: Adapters are configuration — `reviewers.json` adds or overrides one with no release
- [ ] **REV-03**: An adapter invokes the tool's own review command, never a prompt Redline wrote
- [ ] **REV-04**: Reviewer findings carry the reviewer's name and its stated confidence, and stay labelled judged
- [ ] **REV-05**: A reviewer that is missing, crashes, or times out is recorded as failed and never renders as clean
- [ ] **REV-06**: Two or more reviewers can run in one session, each recorded as its own substrate
- [ ] **REV-07**: Review comments are a first-class section, grouped by reviewer so a skim shows each one's flavor
- [ ] **REV-08**: Agreement between reviewers is marked only where it is mechanically certain — identical fingerprints. Redline never decides that two differently worded findings are one defect.

### UI screens

- [ ] **UI-01**: UI screens are a first-class section in pass position, not an appendix
- [ ] **UI-02**: Before and after render as a pair when both exist; a lone after still renders
- [ ] **UI-03**: No captures reads as absence and says the interface was not looked at — not that it is unchanged
- [ ] **UI-04**: Captures from an agent walk are labelled as that agent's work

### Comment-back loop

- [ ] **CMT-01**: Any diff line can carry a comment, addressed as `path:line`
- [ ] **CMT-02**: Comments are copyable as a payload an agent can act on directly
- [ ] **CMT-03**: The payload names the target and the review identity, so the agent knows what it is fixing
- [ ] **CMT-04**: Comments persist per review identity in the browser and do not leak between reviews
- [ ] **CMT-05**: The skill instructs the agent to consume a pasted payload and address the items

## v2 Requirements

- **EXE-01**: Apply migrations to a real database and diff the resulting schema (the original "evidence from execution" thesis)
- **EXE-02**: Diff live API responses between revisions
- **EXE-03**: Deterministic UI capture as a pane rather than an agent walk
- **CAT-01**: Remaining rungs of the observation catalog
- **HOST-01**: Anything hosted or multi-user

## Out of Scope

| Feature | Reason |
|---------|--------|
| Reading a review off a GitHub PR | Copilot review access is going away; reviewers run locally |
| Posting to the GitHub review API | Comments are local and handed to an agent |
| Asking the human to read source | The reviewers own code details |
| Redline authoring its own review prose | Determinism and provenance; it runs a vendor's review instead |
| Fuzzy matching two findings as one defect | Asymmetric failure: a loose threshold eats a real finding silently |
| Running linters inside Redline | CI owns lint and already gates warning and error |
| Hosted app or accounts | Local CLI plus an embedded page |
| CDN or remote assets at open time | Must work offline from `serve` or disk |

## Traceability

| Requirement | Status |
|-------------|--------|
| ORI-01 … ORI-05 | Orientation |
| SUR-01 … SUR-04 | Screen order |
| SUP-01 … SUP-05 | Suppression |
| API-01 … API-04 | OpenAPI pane |
| SCH-01, SCH-02 | Schema |
| COV-01 … COV-04 | Coverage |
| REV-01 … REV-05 | Shipped |
| REV-06 … REV-08 | Two reviewers |
| UI-01 … UI-04 | UI screens |
| CMT-01, CMT-02, CMT-04 | Shipped |
| CMT-03, CMT-05 | Comment loop |

---
*Requirements defined: 2026-08-29 — rewritten to the reviewer's-screen north star*
