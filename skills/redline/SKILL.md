---
name: redline
description: Review a change — the working tree, the latest commit, a commit range, a branch, or a GitHub PR — combining Redline's observed evidence with your own reading of the code, a diff-scoped UI walk when the interface changed, and open a report UI when done. Use when asked to review a commit, range, branch, or PR, before pushing, or whenever a change touches migrations, an API contract, or the UI.
---

# redline

Redline observes a change and reports **evidence**. You supply the reading and
the judgment. Both halves go into one report, labelled so the reviewer always
knows which is which.

This skill is the driver. It works the same in Claude Code and in Cursor:
`redline` is on PATH, this file is the brief. The binary does not call a model
unless you pass `--with` or `--brief`.

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
redline review --pr 123 --brief brief   # also gather callers, docs, tests outside the diff
```

This prints JSON: the target, the commits, every changed file with its diff,
`uiTouched`, the repository's own instruction files, what Redline established
by observation, graph threads through the touched code, and — when `--brief`
was set — a `brief` of references, docs, and tests outside the diff. It makes
no model calls unless you asked for `--with` or `--brief`; you are the model
doing the review.

**2. Review the packet.** Read `guidance` and follow it. In short:

- Follow `instructions[]` — the repo's `.github/copilot-instructions.md`,
  `.github/instructions/*.instructions.md`, `AGENTS.md`, Cursor rules. Cite the
  file in `instruction` when a finding rests on a house rule.
- Do not re-report anything in `deterministic[]`. Those are observed and are
  already in the report.
- When `brief` is present, act on it before diving into the diffs on your own:
  follow `brief.references` to callers and callees outside the diff, read
  `brief.docsNaming` for contradictions between the docs and the code, and
  check `brief.testsCovering` for gaps. Cite the path a brief item came from
  when a finding rests on it. Put any brief item you could not check in
  `unknowns`.
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
  "actual": "What the change does, as read from the code — compared to the PR body.",
  "discrepancies": [{
    "claim": "Words from the PR that this change does not keep.",
    "actual": "What the code does instead.",
    "file": "path.go", "line": 42, "startLine": 40,
    "rule": "intent-preamble-removed"
  }],
  "intent": {
    "ticket": {"id": "REL-24", "title": "...", "url": "https://..."},
    "fit": {
      "thing": "Is this what the ticket asked for? Say so plainly.",
      "way": "Is this the right way to build it? Name the reservation if you have one."
    }
  },
  "surfaces": {
    "interface": {"line": "One line: what moved on the UI.", "moved": true},
    "api":       {"line": "No spec in this change.", "moved": false},
    "schema":    {"line": "One migration adds a nullable column.", "moved": true}
  },
  "files": [
    {"path": "internal/foo.go", "summary": "What this file does in the change, one sentence."}
  ],
  "apiChanges":    [{"title": "...", "detail": "...", "file": "...", "breaking": true}],
  "schemaChanges": [{"title": "...", "detail": "...", "file": "...", "breaking": false}],
  "findings": [{
    "file": "path.go", "line": 42, "startLine": 40, "rule": "kebab-case-slug",
    "category": "schema|contract|cover|ui|review|intent",
    "severity": "error|warning|info",
    "message": "The defect, in one sentence.",
    "context": "The concrete failure: given these inputs, this goes wrong.",
    "fix": "What to do instead.",
    "suggestion": "literal replacement for the anchored lines, single file, at most 20 lines",
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

`summary`, `actual`, `intent` and `surfaces` are the top of the screen — the
reviewer reads them before anything else. Stated intent is the PR title and
body Redline already fetched; do not paraphrase it into `intent.pr`. Put what
the code does in `actual`, and every place they disagree in `discrepancies`
(and as a finding with `category: "intent"` and the same `rule`).

- **`actual`** is required when reviewing a PR: what the change does, from the
  code. The posted review leads with stated intent then this.
- **`discrepancies`** are the gaps Copilot caught as
  `discrepancy_with_pr_description`. Name the claim, what the code does, and
  the file:line. Reuse the same `rule` on the finding so the overview can
  thread to the inline comment.
- **`suggestion`** is a literal single-file fix for the anchored lines, at most
  twenty lines. Skip it when the fix spans files; say what to do in `context`
  instead.
- **`intent.fit`** is the judgment the reviewer most wants and the diff cannot
  carry: is this the thing the ticket asked for, and is this the way to build
  it. Answer both. "Yes" is a fine answer; so is naming a reservation.
- **`intent.ticket`** only if you actually know it. Do not invent an id.
- **`surfaces`** takes all three keys every time. A surface that did not move
  still gets a line saying what you looked at — an omitted surface renders as
  *unreported*, which tells the reviewer nobody checked. `{"moved": false}`
  with no line is treated as saying nothing.

`files` covers every path in the packet: a reviewer who has not opened the diff
should still know what each file does. `apiChanges` and `schemaChanges` are
drill-in evidence beneath the surface lines. Write all of it in the domain of
the change. `screenshots[].before` is optional; a walk of the current tree
usually has only `path`.

Do not write findings about generated files or lockfiles — they are excluded
from the packet before you see it. Do not spend findings on style or on lint
that CI already gates at warning or error; an **info**-level remark worth
acting on is welcome.

## Presenting it

Lead with the `Report:` URL from stderr so the user can click it. Then keep
the terminal short: the summary, the findings that matter, and **what went
unexamined**. Check `coverage` — if
`examinedFiles` is less than `changedFiles`, say which parts nobody checked.
An empty findings list from a pane that does not exist is not a pass, and the
reviewer will read it as one unless you say otherwise.

The report also carries `unknowns` naming check families Redline specifies but
has not built (**checks 15–16 screenshots/console**, spec-vs-handler
agreement). An agent UI walk does not retire those. Do not present those areas
as reviewed by Redline.

Two numbers on the report are absences rather than results, and both read as a
pass if you let them. `diffCoverage` is null when no coverage profile was found
— that is "nobody measured", not "nothing is tested", and it is worth telling
the user which one they are looking at. `coverage.generated` lists files
excluded as machine output; if something there looks hand-written, say so.

## Reviewer comments come back to you

The report lets the reviewer click any diff line, leave a comment, and press
**Copy comments for the agent**. What they paste looks like this:

```json
{"redlineReviewComments": {
  "instruction": "...",
  "review": "abc12345:def67890",
  "target": "PR #123 · owner/repo",
  "comments": [
    {"file": "internal/foo.go", "line": 42, "side": "new",
     "code": "	return nil", "text": "this swallows the error"}
  ]
}}
```

When you receive it:

1. **Check `target` is the change you are working on.** These comments were
   written against one tree. If the session has moved on — a different branch,
   a rebase, new commits — say so and stop rather than applying them to code
   the reviewer never saw.
2. **Address every comment.** Make the change, or reply saying why you did not.
   `code` is the line as the reviewer saw it; use it to find the right place
   when line numbers have shifted.
3. **Do not re-review the rest of the diff.** They asked about these lines.
4. **Re-run and ingest** when you are done, so the report reflects the new
   state. Use the same target flags.

A comment is not a finding. Do not put it in `findings` — it is the reviewer's
instruction to you, and echoing it back as a defect you discovered is noise.

## Do not

- Do not post anything to GitHub on your own. `redline post` is the only command
  that writes, and only the person reviewing runs it. `run`, `review` and
  `ingest` are read-only, so nothing you do reaches the pull request.
- Do not claim Redline executed, ran, or tested anything. Your findings are
  `source: "llm"`. A UI walk you drove is your work — say so, attach
  screenshots, and do not call it checks 15–16.
- Do not treat Redline as a gate. It blocks nothing, by design.
- Do not drive the browser through anything except `agent-browser`.
