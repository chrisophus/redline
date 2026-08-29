---
name: redline
description: Review a change — the working tree, a branch, or a GitHub PR — combining Redline's observed evidence with your own reading of the code, and open a report UI when done. Use when asked to review a branch or PR, before pushing, or whenever a change touches migrations, an API contract, or the UI.
---

# redline

Redline observes a change and reports **evidence**. You supply the reading and
the judgment. Both halves go into one report, labelled so the reviewer always
knows which is which.

## When to use

- "Review this PR" / "review branch X" / "review what I've got".
- Before a push or opening a PR.
- Any change touching migrations, an API spec, or the interface.

## The loop

Three steps. Do not skip step 2 — Redline's own checks cover a narrow slice
today, and a report with no agent review is mostly empty.

**1. Get the packet.**

```
redline review --pr 123          # or --branch feat/x, or nothing for the working tree
```

This prints JSON: the target, the commits, every changed file with its diff,
the repository's own instruction files, what Redline established by
observation, and graph threads through the touched code. It makes no model
calls — you are the model.

**2. Review it.** Read `guidance` in the packet and follow it. In short:

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

**3. Hand back your review.**

```
echo '<your review JSON>' | redline ingest --pr 123
```

Same target flags as step 1. This merges your findings in, writes the report,
and opens the HTML UI in the browser.

Review JSON shape:

```json
{
  "summary": "One or two sentences: what this change does, in its own domain.",
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
  "unknowns": ["What you could not determine, and why."]
}
```

`summary`, `apiChanges` and `schemaChanges` are what the report leads with.
Write them for someone who has not read the diff.

## Presenting it

The UI opens on its own. In the terminal, keep it short: the summary, the
findings that matter, and **what went unexamined**. Check `coverage` — if
`examinedFiles` is less than `changedFiles`, say which parts nobody checked.
An empty findings list from a pane that does not exist is not a pass, and the
reviewer will read it as one unless you say otherwise.

The report also carries `unknowns` naming check families Redline specifies but
has not built (OpenAPI diffing, screenshots, diff coverage). Do not present
those areas as reviewed.

## Reviewer comments come back to you

The report UI lets the reviewer comment on diff lines and click **Copy comments
for the agent**. When they paste that JSON in, treat each comment as a request
against the file and line it names, and act on it.

## Do not

- Do not post anything to GitHub. Redline is read-only there.
- Do not claim to have executed, run, or tested anything. Everything you emit
  is recorded as `source: "llm"` and rendered apart from observed findings —
  keep that honest.
- Do not treat Redline as a gate. It blocks nothing, by design.
