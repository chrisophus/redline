# Redline — The Reviewer's Screen

## What This Is

Redline assembles, in one browser view, exactly what a reviewer looks at when they review a change — and suppresses everything they skip.

Almost nothing on that list has to be invented. It has to be collected and laid out for a reviewer's mindset rather than a terminal's. GitHub has the PR description. Jira has the ticket. The reviewer you ran writes the assessment and the comments. CI computes coverage and gates lint. Git has the `openapi.yaml` and migration diffs. The one artifact nobody hands you is the UI screens.

Prequel is the interaction reference: a local browser view of a review, inline comments on any line, comments handed back to the agent. Not a hosted app.

## Core Value

Open the report and know what this change is, what moved on the surfaces that matter, and what the reviewers found — without reading the source, and without mistaking silence for a pass.

## The Pass, In Order

The screen exists for this sequence and nothing else:

1. **Orientation** — the ticket, the PR description when one exists, and the reviewer's account of what changed
2. **UI screens** — before / after for the routes this change touches
3. **API contract** — the `openapi.yaml` diff
4. **Schema** — the SQL migration files
5. **Coverage** — the number, never the tests
6. **Review comments** — skimmable, to get a flavor of what was found, grouped by reviewer
7. **Info lint** — info level only

## What The Screen Never Shows

- **Generated code** (ogen, sqlc, lockfiles, `go.sum`, build output) — absent, not collapsed
- **Test file contents** — the coverage number stands in for them
- **Warning and error lint** — CI gates those; they are fixed before review begins
- **A file-by-file walkthrough of source as the lead** — reachable, never first

Suppression carries equal weight to inclusion. It is what makes the screen skimmable.

## Requirements

### Validated

- ✓ Observe a change at two revisions and emit findings JSON — `redline run`
- ✓ Closed packet for the agent; the binary authors no judgment — `redline review`
- ✓ Merge a review and open an HTML report — `redline ingest`
- ✓ Serve over loopback so Cursor and Claude can load it — `redline open` / `serve`
- ✓ Target a worktree, `--pr`, `--branch`, `--commit`, or `--range`
- ✓ Run an external reviewer's own review command — `--with claude` / `--with cursor`
- ✓ Coverage count and an honest banner when no pane claimed the change
- ✓ Observed findings and judged findings stay labelled and distinguishable
- ✓ Line-addressable diff comments, copyable for the agent
- ✓ Rung 1 migration hygiene (checks 1–2)

### Active

- [ ] Orientation block: ticket, PR when present, the reviewer's account of the change
- [ ] Generated code excluded from the packet and the screen
- [ ] Test file contents suppressed; coverage number in their place
- [ ] The screen ordered as the pass, not as a findings dump
- [ ] Two reviewers in one session, grouped by reviewer, agreement marked only where it is certain
- [ ] `openapi.yaml` diff as a deterministic pane
- [ ] Diff coverage number, absent when no profile exists
- [ ] UI screens as a first-class section with honest absence
- [ ] Comment-back loop closed: the agent consumes what the screen copies

### Out of Scope

- Reading the review off a GitHub PR — Copilot review access is going away; reviewers run locally
- Posting to the GitHub review API — comments are local and handed to an agent
- Asking the human to read Go or TypeScript — the reviewers own that
- Hosted or multi-user app — a local CLI and an embedded page
- Executing migrations against a live database, driving a live API — later deepening, not the north star
- CDN or network fetch when the report opens

## Context

Brownfield. Module `github.com/ccason/redline`, GitHub `chrisophus/redline`, branch `redline-branch`. Go 1.26+ (developed against 1.27).

Origin: Copilot reviews on GitHub PRs worked well, and at least two passes ran before a merge. That access is going away. Prequel showed that seeing a review in a browser puts you in a reviewer's mindset that a terminal does not. CodeRabbit showed the workflow shape. What decided the design was introspecting on the actual pass — the seven things above, and the four things deliberately skipped.

Dogfooding taught the constraint that overrides everything else: **a report must never read better than the facts support.** A change no pane covered once rendered as "Findings: None" beside "every applicable check ran." So coverage leads, an untouched surface reads as absence rather than a pass, a partial confirmation becomes an unknown with its denominator stated, and a reviewer that crashed is recorded as failed rather than clean.

`file://` is blocked in Cursor. Reports open at `http://127.0.0.1:<port>/report.html`.

## Constraints

- **Tech stack**: Go CLI, `embed` for one self-contained `report.html`. No CDN, no network at open time.
- **Determinism**: same tree, same observed evidence. Reviewer output is `source: "llm"` and never claimed reproducible.
- **Judgment boundary**: the binary composes no judgment. It runs a vendor's own review command and normalizes the result, or it takes a review on stdin. It never decides that two differently worded findings are the same defect.
- **Local only**: `.redline/` is gitignored. Comments live in the browser and are copied out by hand.
- **Mutual exclusion**: pass only one of `--pr`, `--branch`, `--commit`, `--range`.
- **Both moments**: the screen works pre-push with no PR and no ticket, and on an open PR with both.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Redline is the reviewer's screen, not a source reader | The reviewer does not read Go or TypeScript. They look at the ticket, the UI, the contract, the migrations, a coverage number, and a skim of what the reviewers found. | ✓ Locked |
| Suppression is a feature | Generated code, test bodies, and CI-gated lint are noise on this screen. Absent, not collapsed. | ✓ Locked |
| Reviews come from reviewers run locally | Copilot review access on GitHub is going away, so the trusted-review pane cannot read a PR. `--with` is the foundation, not a convenience. | ✓ Locked |
| The screen adapts to both moments | Opened pre-push on own work and on an open PR. No PR description pre-push; no Copilot comments ever. | ✓ Locked |
| Agreement is marked only when mechanically certain | Two reviewers reporting one defect is the strongest signal available, but deciding two differently worded sentences are the same defect is a judgment. Only identical fingerprints count. | ✓ Locked |
| Never fuzzy-match findings | A threshold slightly too loose eats a genuine finding and the reviewer never learns it existed. The failure is asymmetric. | ✓ Locked (reverted once already) |
| "Evidence from execution" is demoted | Nothing in the pass requires standing up Postgres or driving a live API — the migration *files* are what gets looked at. | ✓ Locked |
| Binary may execute a reviewer CLI | Redline writing its own review reinvents what Claude and Cursor already do. Executing their review command and normalizing the output composes no judgment of its own. | ✓ Good |
| Grow `report.html` plus `serve`; no React runtime | One file, no login, loads in agent browsers. | ✓ Good |
| Coverage honesty on every surface | Empty findings after examining nothing is not a pass. | ✓ Good |

## Evolution

**After each phase transition:**
1. Requirements invalidated? → Out of Scope with a reason
2. Requirements validated? → Validated with a reference
3. New requirements emerged? → Active
4. Decisions to log? → Key Decisions
5. Is "What This Is" still accurate? → update if drifted

**After each milestone:** full review of all sections, Core Value check, audit Out of Scope, refresh Context.

---
*Last updated: 2026-08-29 — rewritten to the reviewer's-screen north star; Copilot access loss and both-moments recorded*
