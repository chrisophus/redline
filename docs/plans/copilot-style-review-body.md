# Copilot-style review body on `redline post`

Make bot-posted Redline reviews on GitHub read like the author-published
Copilot / Bugbot reviews teams already recognize: stated intent, what the
change does, a collapsible walkthrough of **every** changed file, then
findings. Keep Redline's evidence table and gate markers; do not weaken
attestation or idempotency.

Status: **shipped**. `body_style: walkthrough` renders the Copilot-order body;
`body_include` (coverage, lint, confirmations, unknowns) dials how much of the
report rides along, an extension past the original plan so a repository decides
how much reaches the pull request. Consumer: Marketplace `/review-bot review`
(`marketplace-review-bot[bot]`), which sets the profile.

## The gap

Two post paths exist in Marketplace today, and they produce different review
bodies from the same `review.json` shape.

**Author path** — `scripts/publish-agent-review.sh` (Cursor Bugbot, Claude
Code):

- `### Review findings` or `### Review complete`
- `**Reviewed by.** … on \`<sha>\``
- `**Stated intent.**` — PR title plus sanitized PR body
- `**What it does.**` — `overview` from `review.json`
- `<details><summary>Walkthrough</summary>` — table of **all** paths from
  `gh pr diff --name-only`, merged with per-file summaries; missing paths get
  `No notes.`
- Inline comments from `comments` / `findings`
- Checksum-bound `mct-agent-review:v2` attestation

**Bot path** — `redline post --profile …` (Redline CI):

- `### Changes recommended` (verdict from pane findings, not finding count)
- Evidence table (panes, coverage) — always visible
- Optional `### What this change does` when `Agent.Overview` is non-empty
- Optional `<details>Summary per file (N), written by the agent</details>` —
  **only files the model summarized**, not the full diff
- `### Findings not shown inline`
- `mct-agent-review:v1 verdict=pass|fail head=<sha>` (not v2 checksum)

On large PRs (e.g. Marketplace PR 1284), the bot body often has **no**
walkthrough at all: the LLM truncates or sparse-fills `files`, and
`fileSummarySection` in `internal/post/post.go` deliberately omits paths
without a non-empty agent summary. Readers see evidence and a bullet list of
info findings, not the orientation block Copilot gives them.

The report and HTML drill-in already render agent `overview` + per-file
summaries (`report-roadmap.md`, shipped). The **posted PR review** is the
piece that lagged.

## Goal

When a profile asks for it (default for Marketplace bot profile), `redline
post` assembles a body that matches the Copilot layout closely enough that
authors do not need a mental model switch between Bugbot and Redline on the
same PR.

Readers should be able to:

1. See who reviewed and which commit, without opening the report.
2. Read stated intent and a short “what it does” before any finding.
3. Expand one walkthrough table listing every changed file and a one-line
   summary (or `No notes.`).
4. Expand evidence (pane table, coverage) when they care about scope.
5. Read inline threads and body-only findings as today.

## Non-goals

- Replacing `scripts/publish-agent-review.sh` for author-published reviews
  (v2 attestation, author-only gate stay there).
- Changing gate semantics: panes still gate; LLM findings still advise only
  (`GateVerdict` unchanged).
- Requiring the LLM to summarize every file in one turn (walkthrough fills
  gaps deterministically from the diff).
- Copied Bugbot v2 checksum markers on bot posts (bot stays on v1
  `verdict=pass|fail`).
- Auto-approving or changing review `event` (still `COMMENT`).

## Target body shape

Order and labels match `publish-agent-review.sh` where possible. Redline-specific
sections stay, but collapsed so the fold reads like Copilot first.

```markdown
### Review findings

**Reviewed by.** marketplace-review-bot[bot] on `11fb99f64dae`.

**Stated intent.** MKT-1462: Populate AWS CPPO channel partner from Offer data feed

- Ingest AWS Offer SDDS feed…
  (sanitized PR body, truncated if huge)

**What it does.** One or two paragraphs from `Agent.Overview`, or omitted when
empty (no empty heading).

<details>
<summary>Walkthrough</summary>

| File | What changed |
|---|---|
| `internal/data/aws_offer_feed.go` | Staging and partner lookup for offer feed rows. |
| `docs/runbooks/admin-backfill-workflows.md` | No notes. |

</details>

<details>
<summary>Evidence</summary>

| Evidence | Result |
|---|---|
| redline/sql | ran — 0 finding(s) |
| diff coverage | 79% of 584 added line(s) (`coverage.out`) |
| files examined | 50/57 |

</details>

### Findings not shown inline

- **Info** — … _(redline/parity)_ <!-- redline:fp:… -->

<!-- redline:review:<sha> -->
<!-- mct-agent-review:v1 verdict=pass head=<sha> -->
```

Heading rules:

| Condition | Heading |
|---|---|
| Gate would fail (blocking pane/ finding) | `### Review findings` |
| No blocking findings | `### Review complete` |

Map from current `verdictFor()` (“Changes recommended” / “Pass” / …) only
inside the evidence-first layout; the walkthrough layout uses the table
above.

## Locked decisions

1. **Walkthrough rows = full diff file list**, not agent-only. Source:
   `res.Change.Files` from the session (same paths the report walkthrough
   uses). Merge agent summaries from `rep.Agent.Files`; default cell text
   `No notes.` when absent or blank. Sort paths lexicographically.

2. **Stated intent from PR metadata**, not from the model. Fetch via existing
   `gh pr view` (title + body) when posting a PR session. Sanitize embedded
   marker lines the same way Marketplace's `agent_review_sanitize_embedded_text`
   does — port the small rule into Redline or document a shared contract; do
   not paste raw PR bodies that contain fake `mct-agent-review` lines.

3. **Reviewed by from posting login** (`ghLogin` / GraphQL viewer), not hard-coded.
   Short SHA (12 chars) matches author publisher.

4. **Evidence table moves under `<details>`** in walkthrough mode only.
   Default post (no profile flag) keeps today's layout for backward
   compatibility.

5. **Profile opt-in**, not a breaking default for all repos:
   `body_style: walkthrough` in YAML (name TBD). Marketplace sets it in
   `.github/review-bot-gate-profile.yml`.

6. **Body budget unchanged** (`maxBody`, `maxNarrative`). Walkthrough may
   truncate with a visible “N more files on the full report” note — same
   pattern as `fileSummarySection` today. Prefer truncating walkthrough rows
   before dropping gate markers or the not-shown findings list.

## Implementation

### 1. Profile field

`internal/post/profile.go`:

```yaml
body_style: evidence   # default — current behavior
body_style: walkthrough
```

Parse in `LoadProfile`; default `evidence`.

### 2. Pass change set into body builder

`BuildAttest` today only receives `*findings.Report`. Extend the build path
so `cmdPost` passes `res.Change` (or `[]string` changed paths) into
`buildBody`. Session already loads `Change` in `run.LoadSession`.

### 3. Walkthrough builder

New function in `internal/post/post.go` (or `walkthrough.go`):

- Input: changed paths, `Agent.Files`, budget remainder.
- Output: `<details>…Walkthrough…</details>` block.
- Reuse `escapeCell` for table cells.
- Unit tests: all diff paths present; agent summary wins; blank → `No notes.`;
  pipe in path/summary escaped; truncation message when budget exceeded.

Port the merge logic from Marketplace
`scripts/publish-agent-review.sh` lines 153–162 as the spec; implement in Go
so `redline post --dry-run` needs no `jq`.

### 4. PR metadata for stated intent

In `cmdPost` (or a small `prmeta` helper):

- When `tgt.PR != nil` and not dry-run (or dry-run with gh available): `gh pr
  view` for title, body.
- Cache on `buildBody` input struct to avoid duplicate calls (head check
  already calls `ghPRHead`).

Dry-run without gh: omit stated intent or use PR fields already on
`target.PullRequest` if extended (optional follow-up: stash title/body in
session at `run` time to make dry-run offline-complete).

### 5. Walkthrough body assembly

Refactor `buildBody` into layout strategies:

- `buildBodyEvidence` — current behavior.
- `buildBodyWalkthrough` — Copilot block order, evidence in details,
  `verdictHeading` per table above, then `notShownSection`, tail markers.

`overviewSection` / `fileSummarySection` are not duplicated in walkthrough
mode; walkthrough replaces `fileSummarySection`. Keep a single “What it does”
from overview.

### 6. Tests

`internal/post/post_test.go`:

- Golden body fragment tests for walkthrough layout (profile on).
- Ensure gate markers and `redline:review:` still appended.
- Ensure `mct-agent-review:v1` unchanged.
- Regression: default profile nil / `body_style: evidence` matches old golden.

Optional: fixture session under `testdata/fixtures/` with `Agent` + `Change`
for integration-style `post --dry-run` snapshot.

### 7. Docs

- `README.md` posting section: describe both body styles.
- `redline-review.yml.example`: document `body_style`.
- Marketplace plan cross-link only (consumer sets profile); no requirement to
  edit MCT in the same Redline release.

## Marketplace rollout (consumer, separate PR)

After Redline release tag (e.g. v0.4.0):

1. Bump `REDLINE_VERSION` in MCT `Makefile`.
2. Add `body_style: walkthrough` to `.github/review-bot-gate-profile.yml`.
3. Re-run `/review-bot review` on a large PR; confirm walkthrough lists all
   diff files and evidence is collapsed.
4. Confirm merge gate still parses v1 markers (no change expected).

Do **not** change `publish-agent-review.sh` in the same effort unless extracting
a shared markdown spec both sides cite.

## Done when

- [ ] `body_style: walkthrough` produces Copilot-order body with collapsible
      walkthrough (all changed files) and collapsible evidence.
- [ ] Default / missing `body_style` preserves current post body (tests green).
- [ ] Stated intent and Reviewed by render on PR post with gh available.
- [ ] Gate markers, fingerprint idempotency, inline comment rules unchanged.
- [ ] Marketplace bot profile enables walkthrough; sample PR body reviewed by
      a human as “matches Copilot layout”.
- [ ] `redline post --dry-run` documents walkthrough truncation when body
      exceeds budget.

## Sequencing

| Step | Work |
|---|---|
| 1 | Profile field + tests |
| 2 | Walkthrough table builder from `Change` + `Agent.Files` |
| 3 | Walkthrough body layout + evidence `<details>` |
| 4 | PR title/body for stated intent |
| 5 | README + example profile |
| 6 | Tag Redline; consumer bumps pin |

Steps 2–4 can land in one PR if tests cover each piece.

## Relation to other plans

- **`two-stage-review.md`** — quality of findings (scout, precedent, thread
  memory). This plan is **presentation only**; better findings still benefit
  from a readable body.
- **`report-roadmap.md`** — report already shows agent overview + per-file
  summaries; walkthrough post closes the loop to GitHub.
- **MKT-1386 (Marketplace)** — explicitly chose `redline post` over the shell
  publisher for the bot; this plan aligns bot UX with Copilot without merging
  attestation paths.

## Open questions

1. **Heading when gate fails but zero inline comments** — use `Review findings`
   (match publisher) or keep `Changes recommended` for fail? **Proposal:**
   match publisher (`Review findings` / `Review complete`).

2. **PR body in session at run time** — worth storing on session for offline
   dry-run and to avoid a second `gh pr view` at post? **Proposal:** defer;
   post-time fetch is enough for CI.

3. **Duplicate walkthrough on re-post** — idempotency is per-finding, not per
   body section. Re-post with new findings replaces the whole review body.
   Accept (same as today).

4. **Extract shared body spec to a markdown fragment** both MCT shell and
   Redline tests cite — **Proposal:** optional follow-up; Go implementation
   is source of truth for the bot path first.
