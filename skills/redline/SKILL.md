---
name: redline
description: Review a pending change with evidence rather than by reading the diff. Use before pushing, when asked to review a branch or working tree, or whenever a change touches database migrations. Runs `redline run` and presents its report.
---

# redline

Redline observes a change and reports **evidence**, not inference from source.
You supply the prose and the judgment; Redline supplies the facts.

## When to use

- The user asks for a review of the working tree or the current branch.
- The user is about to push or open a PR.
- A change touches database migrations.

## Invoke

```
redline run --format json
```

`run` is the entry point. **Do not choose which checks to run** — applicability
is computed from the diff. Choosing yourself turns coverage into a sample from
a distribution, and a check can be silently skipped without anyone noticing.

Useful flags: `--base REF` (defaults to origin/main), `--upstream REF` (branch
new migrations must not collide with), `--migrations DIR`.

Every run also writes `.redline/findings.json` and `.redline/report.md`.
`redline sql` drills into one pane after the fact; reach for it only when the
user asks about that pane specifically.

## Read the output

The report has four sections and **you must present all four**:

1. **What changed** — the change in its own domain (which migration versions
   this branch adds, which merged ones it touches).
2. **What was checked and held** (`confirmations`) — questions the reviewer no
   longer has to ask. Summarize these; do not drop them. A clean check is the
   deliverable, not silence.
3. **What could not be determined** — start here, before the findings. (`unknowns`, plus any substrate whose
   `state` is `failed`) — **never omit this**. A report that silently covers
   60% of a change reads exactly like one that covers all of it. Say plainly
   which checks did not answer and why.
4. **Findings** — lead with these. Each carries `expected` and `observed`; the
   gap between them is the point. `context` explains why it matters.

**Check `coverage` first, every time.** It reports `changedFiles` and
`examinedFiles`. If `examinedFiles` is 0, Redline looked at none of the change
and an empty findings list means nothing — say so plainly and review by hand.
If it is less than `changedFiles`, name what went unexamined; the reviewer's
attention belongs there.

A substrate with `state: skipped` did not apply to this change — that is
normal. A substrate with `state: failed` is a **dark sensor**: it applied and
did not run, and must be reported loudly, never read as a pass.

## Present

Every finding carries `source`. `deterministic` means Redline observed it and
it reproduces. Anything you add from reading the code yourself is your own
inference — label it as such and keep it visually separate from Redline's
findings. The determinism guarantee is worthless if the reviewer cannot tell
which is which at the point of use.

Findings carry `evidence` — observation IDs resolving to artifacts under
`.redline/evidence/`, such as the actual SQL a migration edit changed. Show the
artifact beside the claim. A finding the reviewer cannot check for themselves
is inference wearing evidence's clothes.

Do not restate the JSON. Lead with what the reviewer must act on, then the
confirmations in one line, then the gaps.

## Do not

- Do not treat Redline as a linter to satisfy — it is non-gating by design and
  blocks nothing.
- Do not re-run a pane hoping for a different answer.
- Do not fill a gap in section 3 by guessing. Say it is unknown.
