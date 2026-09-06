---
name: redline
description: Observe a change with Redline — the working tree, the latest commit, a commit range, a branch, or a GitHub PR — and fold its measured evidence into your review. Use when asked to review a commit, range, branch, or PR, before pushing, or whenever a change touches migrations, an API contract, or coverage-sensitive code.
---

# redline

Redline measures a change and reports evidence: migration hygiene, API
contract breaking changes, diff coverage, what was examined and what was
not. It never invokes a model and it composes no judgment. The review is
yours; Redline exists so you do not spend your context re-deriving what a
deterministic check already established.

## When to use

- "Review this PR" / "review branch X" / "review the latest commit" /
  "review these commits" / "review what I've got".
- Before a push or opening a PR.
- Any change touching migrations, an API spec, or test coverage.

## The loop

**1. Run it.**

```
redline run                   # working tree (uncommitted work included)
redline run --commit HEAD     # latest commit only
redline run --range HEAD~3..HEAD
redline run --branch feat/x
redline run --pr 123
```

This writes `findings.json`, `report.md`, and `report.html` under
`.redline/`, prints the markdown report, and prints `Report: <url>` on
stderr.

**2. Read `findings.json`, then review the change yourself.**

- Do not re-report anything in `findings[]`. Those are observed facts and
  are already on the report.
- Check `coverage`: `examinedFiles` versus `changedFiles`, `diffCoverage`,
  and `unknowns[]`. These tell you what nobody has measured, which is where
  your own reading matters most.
- Review with your own skills and tools: read the diff, follow the callers,
  weigh the change against the repo's rules. Redline has no opinion on any
  of that.

**3. Present it.**

Lead with the `Report:` URL from stderr, exactly as printed — the port is
8765 only when 8765 was free. Then keep the terminal short: your review,
the observed findings that matter, and what went unexamined. If
`examinedFiles` is less than `changedFiles`, say which parts no check
covered; an empty findings list from a pane that does not exist is not a
pass, and the reviewer will read it as one unless you say otherwise.

Two numbers on the report are absences rather than results, and both read
as a pass if you let them. `diffCoverage` is null when no coverage profile
was found — that is "nobody measured", not "nothing is tested".
`coverage.generated` lists files excluded as machine output; if something
there looks hand-written, say so.

## Reviewer comments come back to you

The report lets a human click any diff line, leave a comment, and press
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
4. **Re-run `redline run`** when you are done, so the report reflects the new
   state. Use the same target flags.

## Judge the suppressions

Redline extracts the facts; the reading is still yours. For a finding it
cannot judge — a `//nolint` or `eslint-disable` the change adds — you can
record a verdict that rides on the same card, marked `source: llm`, beside
Redline's deterministic fact.

After a run, for each finding in `findings.json` with rule
`suppression-added`, decide why the author silenced the linter and write
`.redline/review.json`, keyed by the finding's own `fingerprint`:

```json
{"verdicts": {
  "<fingerprint from findings.json>": {
    "ruling": "justified",
    "rationale": "test fixture seed, not a real suppression"
  }
}}
```

- `ruling` is one of **justified** (the silencing is warranted),
  **should-fix** (fix the code rather than hide the finding), or
  **rule-noisy** (the rule fires too often and should be tuned or scoped).
- `rationale` is one line: why.
- Key on the exact `fingerprint` string Redline emitted. A verdict that
  matches no finding is dropped.

Then re-run `redline run` with the same target flags: it merges the verdicts
onto the findings and renders them. Redline never writes `review.json` and
never overwrites it, so your judgments survive a re-run the way
`comments.json` does.

Judge only the suppressions here. This is triage of Redline's own evidence,
not a second pass over the whole diff.

## Do not

- Do not post anything to GitHub on your own. `redline post` is the only
  command that writes, and only the person reviewing runs it. `run` is
  read-only, so nothing you do reaches the pull request.
- Do not claim Redline executed, ran, or tested anything it did not. Your
  own findings are your reading of the code; say so.
- Do not treat Redline as a GitHub approval. `post` is always COMMENT. A
  `--profile` file stamps pass/fail markers a *separate* merge gate can
  read; error and warning fail, info does not. Redline never approves or
  requests changes.
