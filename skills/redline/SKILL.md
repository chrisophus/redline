---
name: redline
description: Review a change — the working tree, the latest commit, a commit range, a branch, or a GitHub PR — combining Redline's observed evidence with your own reading of the code, a diff-scoped UI walk when the interface changed, and open a report UI when done. Use when asked to review a commit, range, branch, or PR, before pushing, or whenever a change touches migrations, an API contract, or the UI.
---

# redline

Redline observes a change and reports **evidence**. You supply the reading and
the judgment. Both halves go into one report, labelled so the reviewer always
knows which is which.

This skill is the driver. It works the same in Claude Code and in Cursor:
`redline` is on PATH, this file is the brief, the binary never calls a model.

## When to use

- "Review this PR" / "review branch X" / "review the latest commit" /
  "review these commits" / "review what I've got".
- Before a push or opening a PR.
- Any change touching migrations, an API spec, or the interface.

## The loop

Do not skip the review step — Redline's own checks cover a narrow slice today,
and a report with no agent review is mostly empty. When `uiTouched` is true,
do not skip the UI walk either.

**1. Get the packet.**

```
redline review                   # working tree (uncommitted work included)
redline review --commit HEAD     # latest commit only
redline review --range HEAD~3..HEAD
redline review --branch feat/x
redline review --pr 123
```

This prints JSON: the target, the commits, every changed file with its diff,
`uiTouched`, the repository's own instruction files, what Redline established
by observation, and graph threads through the touched code. It makes no model
calls — you are the model.

**2. Review the packet.** Read `guidance` and follow it. In short:

- Follow `instructions[]` — the repo's `.github/copilot-instructions.md`,
  `.github/instructions/*.instructions.md`, `AGENTS.md`, Cursor rules. Cite the
  file in `instruction` when a finding rests on a house rule.
- Do not re-report anything in `deterministic[]`. Those are observed and are
  already in the report.
- Read the actual files when the diff is not enough. The packet gives you
  paths; open them.
- Correctness, contracts, error paths, concurrency, security, missing tests.
  Not style — the repo's linters own that.
- Volume is the failure mode. Ten real findings beat forty padded ones. Use
  `confidence` rather than either suppressing a genuine concern or asserting a
  shaky one.
- Write a one-sentence summary for **every** path in the packet's `files[]`.
  That walkthrough is the report's file list — Copilot-style, required even
  when you have no findings. It is not a finding and does not restate hunks.

**2b. Walk the UI when `uiTouched` is true.**

This is a ce-dogfood-style pass, **diff-scoped**: only the journeys this
change actually touches. It is *your* walk, recorded as `source: "llm"`. It
does **not** satisfy Redline checks 15–16 (deterministic before/after and
console capture). Leave those unknowns in place; do not claim the UI pane ran.

Use **`agent-browser` only** — the direct binary, never `npx agent-browser`,
never Cursor/Chrome MCP browser tools. Check:

```
command -v agent-browser >/dev/null 2>&1 && echo "Ready" || echo "NOT INSTALLED"
```

If it is not installed, **stop**. Tell the user to install it via `/ce-setup`,
then retry with `/redline`. Do not improvise another browser driver.

Then:

1. Detect the port (explicit `--port` if the user passed one, else the project's
   dev script / `.env` `PORT=` / default `3000`). Reuse a server already
   listening; otherwise start the project's dev command in the background and
   wait until the port accepts connections.
2. Map the user journeys the *diff* touches (not every page). Cover the happy
   path and the obvious branches (validation error, empty, permission).
3. Drive each journey with `agent-browser`: `open`, `snapshot -i`, click/fill,
   `screenshot` to a temp dir (`mktemp -d "${TMPDIR:-/tmp}/redline-ui-XXXXXX"`),
   `errors`. Judge correctness *and* experience.
4. OAuth, real email, payments, SMS: do not fake them. Record an `unknowns`
   entry that a human must verify, and continue.
5. Put PNG paths in the ingest JSON as `screenshots`. Category of UI defects:
   `ui`. In `context`, say you walked the route in a browser.

If `uiTouched` is false, skip this step. Do not crawl an unrelated frontend.

**3. Hand back your review.**

```
echo '<your review JSON>' | redline ingest --pr 123
```

Same target flags as step 1 — ingest merges into the session that `review`
recorded and errors if the flags name a different one, rather than merging
your review into someone else's change. This copies screenshots under
`.redline/evidence/ui/`, writes the report, and prints `Report: <url>` (then
opens that URL). Do not pass `--no-open` unless the user asked. If ingest did
not print a URL, run `redline open`.

Read the port off that line; it is 8765 only when 8765 was free. Lead with the
URL exactly as printed.

Review JSON shape:

```json
{
  "summary": "One or two sentences: what this change does, in its own domain.",
  "files": [
    {"path": "internal/foo.go", "summary": "What this file does in the change, one sentence."}
  ],
  "apiChanges":    [{"title": "...", "detail": "...", "file": "...", "breaking": true}],
  "schemaChanges": [{"title": "...", "detail": "...", "file": "...", "breaking": false}],
  "findings": [{
    "file": "path.go", "line": 42, "rule": "kebab-case-slug",
    "category": "schema|contract|cover|ui|review",
    "severity": "error|warning|info",
    "message": "The defect, in one sentence.",
    "context": "The concrete failure: given these inputs, this goes wrong.",
    "fix": "What to do instead.",
    "confidence": "high|medium|low",
    "instruction": ".github/copilot-instructions.md — the rule this rests on"
  }],
  "unknowns": ["What you could not determine, and why."],
  "screenshots": [{
    "route": "/threads",
    "path": "/tmp/redline-ui-xxx/reply.png",
    "before": "/tmp/redline-ui-xxx/reply-before.png",
    "caption": "Reply form after submit"
  }]
}
```

`summary` and `files` are what the report leads with. Cover every path in
the packet — a reviewer who has not opened the diff should still know what
each file does. `apiChanges` and `schemaChanges` sit beside that. Write all
of them in the domain of the change. `screenshots[].before` is optional; a
walk of the current tree usually has only `path`.

## Presenting it

Lead with the `Report:` URL from stderr so the user can click it. Then keep
the terminal short: the summary, the findings that matter, and **what went
unexamined**. Check `coverage` — if
`examinedFiles` is less than `changedFiles`, say which parts nobody checked.
An empty findings list from a pane that does not exist is not a pass, and the
reviewer will read it as one unless you say otherwise.

The report also carries `unknowns` naming check families Redline specifies but
has not built (OpenAPI diffing, **checks 15–16 screenshots/console**, diff
coverage). An agent UI walk does not retire those. Do not present those areas
as reviewed by Redline.

## Reviewer comments come back to you

The report UI lets the reviewer comment on diff lines and click **Copy comments
for the agent**. When they paste that JSON in, treat each comment as a request
against the file and line it names, and act on it.

## Do not

- Do not post anything to GitHub. Redline is read-only there.
- Do not claim Redline executed, ran, or tested anything. Your findings are
  `source: "llm"`. A UI walk you drove is your work — say so, attach
  screenshots, and do not call it checks 15–16.
- Do not treat Redline as a gate. It blocks nothing, by design.
- Do not drive the browser through anything except `agent-browser`.
