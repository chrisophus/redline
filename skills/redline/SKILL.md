---
name: redline
description: Observe a change with Redline — the working tree, the latest commit, a commit range, a branch, or a GitHub PR — and fold its measured evidence into your review. Use when asked to review a commit, range, branch, or PR, before pushing, or whenever a change touches migrations, an API contract, or coverage-sensitive code.
---

# redline

Redline measures a change and reports evidence: migration hygiene, API
contract breaking changes, diff coverage, what was examined and what was
not. It runs no model itself; it measures, and it carries your review —
comments and verdicts you write merge into the same report, marked as
yours. Redline exists so you do not spend your context re-deriving what a
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
redline run --prepare         # run harness produce steps first (coverage, etc.)
redline run --allow-missing-coverage  # continue without a configured coverage profile
redline run --commit HEAD     # latest commit only
redline run --range HEAD~3..HEAD
redline run --branch feat/x
redline run --pr 123
```

Harness profiles in `.redline.yml` are required when the change matches
their scope. `--prepare` runs produce for profiles that declare it.
Pass `--allow-missing-coverage` to continue without a configured coverage
profile; the report still records it as unknown. Profiles without produce
(for example mutation) must be produced by hand. When a profile is not
configured, redline ignores that artifact entirely.
For mutation (gomutants), add a harness profile with `path: mutants.json`
when you want it required; run the repository's mutate target first (`make
mutate` in MCT; needs `eval $(make db-dsns)` when data packages are in
scope). Mutation is never part of `--prepare`.

This writes `findings.json`, `report.md`, and `report.html` under
`.redline/`, prints the markdown report, and prints `Report: <url>` on
stderr.

**1b. Optionally, let Redline review it.**

```
redline review              # one model call over the run you just did
redline review --dry-run    # print the prompt and its estimated price, call nothing
redline review --stats      # the cost distribution of the reviews recorded so far
```

This is the one Redline command that calls a model. It reads the session
`run` wrote, sends one request with no tools, writes `.redline/review.json`,
and re-renders the report. It observes nothing itself, so it sees exactly
what you can see in `findings.json`.

Use it when you want a second reading beside your own, or when you are
driving Redline unattended. Skip it when you are reviewing the change
yourself anyway: it costs money, and the loop below is your own review.
It needs `ANTHROPIC_API_KEY` or an `ant auth login` profile; without one,
every other command still works. To send the call through an
OpenAI-compatible proxy instead, pass `--api openai` with `--base-url`
(or `OPENAI_BASE_URL`) and `OPENAI_API_KEY`, plus `--api-user` or
`OPENAI_USER` when the proxy wants the caller in the bearer token; that
path is one shot only.

Its findings are advisory, marked `source: llm`, and never reach the merge
gate whatever severity they carry. Verdicts already in `review.json` are
preserved, so running it does not discard judgments you recorded.

**2. Read `findings.json`, then review the change yourself.**

- Do not re-report anything in `findings[]`. Those are observed facts and
  are already on the report. Reference one by its `id` if you want to build
  on it.
- Connecting two findings is a finding in its own right, and the most
  valuable kind: a migration that adds a non-nullable column and a struct
  field that cannot express absence are unremarkable alone and a bug
  together. Write it as a comment with `"category": "correlation"` and the
  ids of both in `"relatedFindings"`.
- Check `coverage`: `examinedFiles` versus `changedFiles`, `diffCoverage`,
  and `unknowns[]`. These tell you what nobody has measured, which is where
  your own reading matters most.
- If a gomutants report is configured in `harness.profiles` and present,
  `mutation` carries the survivors on the changed lines: lines a test runs but nothing fails when they
  change, each naming the `original -> replacement` that went uncaught. That is
  the assertion a test is missing. Produce one with the repository's mutate
  target (`make mutate` in MCT) or `gomutants --changed-since <base> -o
  mutants.json <packages>`. Redline reads the report from the checkout you
  run in, not from a detached PR worktree. Use gomutants v0.6.0 or later:
  older reports carry no mutant ids and no `INFRA_ERROR` status, so a mutant
  whose test run died on the runner is indistinguishable from one the tests
  caught. When the report does carry them, `unknowns[]` names the lines whose
  mutants could not be run. Those lines are unmeasured, not asserted.
- Review with your own skills and tools: read the diff, follow the callers,
  weigh the change against the repo's rules. That reading is your half of
  the report — write it to `review.json` (below) and Redline renders it
  beside its own findings.

**3. Present it.**

Lead with the `Report:` URL from stderr, exactly as printed — the port is
8765 only when 8765 was free. Then keep the terminal short: your review,
the observed findings that matter, and what went unexamined. If
`examinedFiles` is less than `changedFiles`, say which parts no check
covered; an empty findings list from a pane that does not exist is not a
pass, and the reviewer will read it as one unless you say otherwise.

Two numbers on the report are absences rather than results, and both read
as a pass if you let them. `diffCoverage` is null when no coverage profile
was found — that is "nobody measured", not "nothing is tested". When
`coverage.coverableFiles` is 0 the change has no file a profile could
cover, and coverage is left off the report on purpose; do not raise it.
Likewise a pane in `substrates[]` with state `not-applicable` means the
repository has no files it reads; say nothing about it.
`coverage.generated` lists files excluded as machine output; if something
there looks hand-written, say so.

## Reviewer requests come back to you

The report lets a human act on any diff line or finding without writing code:
comment on it, ask you to **Explain** it, or ask you to **Apply** a change. They
collect these and press **Copy for the agent**. What they paste looks like this:

```json
{"redlineReviewComments": {
  "instruction": "...",
  "review": "abc12345:def67890",
  "target": "PR #123 · owner/repo",
  "comments": [
    {"action": "comment", "file": "internal/foo.go", "line": 42, "side": "new",
     "code": "\treturn nil", "text": "this swallows the error"},
    {"action": "explain", "file": "internal/foo.go", "line": 51, "side": "new",
     "code": "\tgo drain(ch)"},
    {"action": "apply", "on": "finding", "file": "internal/foo.go", "line": 42,
     "rule": "err-unchecked", "message": "error is discarded",
     "fix": "wrap the call with %w and return it"}
  ]
}}
```

When you receive it:

1. **Check `target` is the change you are working on.** These items were written
   against one tree. If the session has moved on, a different branch, a rebase,
   new commits, say so and stop rather than applying them to code the reviewer
   never saw.
2. **Do what each item's `action` says.**
   - `comment`: address the feedback in `text`. Make the change, or reply saying
     why you did not.
   - `explain`: explain that line or finding and the code around it. Do not
     change code.
   - `apply`: make the change the item asks for. When it carries a `fix`, apply
     that fix.
   `code` is the line as the reviewer saw it; use it to find the right place when
   line numbers have shifted.
3. **Do not re-review the rest of the diff.** They asked about these lines.
4. **Re-run `redline run`** when you are done, so the report reflects the new
   state. Use the same target flags.

## Bring your review into the report

If you produce a review of the change, an overview, a per-file summary, and line
comments, write it to `.redline/review.json` and Redline folds it into the
report on the next run. This is the same file MCT publishes to GitHub via
`scripts/publish-agent-review.sh --reviewer …` and the same file the verdicts
below live in. Redline renders what you wrote, marked as yours.

```json
{
  "overview": "One or two paragraphs on what this change is and why it exists.",
  "files": [
    {"path": "internal/foo.go", "summary": "Reworked the retry loop to bound attempts."}
  ],
  "comments": [
    {"file": "internal/foo.go", "line": 42, "severity": "warning",
     "confidence": "high",
     "body": "This can loop forever if the server keeps returning 503."},
    {"file": "internal/store/user.go", "line": 8, "severity": "warning",
     "category": "correlation", "relatedFindings": ["f2076e74fbd"],
     "body": "TenantID is a plain string and the migration makes tenant_id NOT NULL with no default, so any insert that omits it writes an empty string."}
  ]
}
```

- `overview` renders as a Review section at the top of the report. `what_it_does`
  is accepted as an alias.
- `files` is a list of `{path, summary}` (preferred) or a map of path to
  one-line summary. MCT's publisher fills any PR diff path missing from the list
  with "No notes."
- `comments` become findings on their line, marked `source: llm`. `findings` is
  accepted as an alias; `path` aliases `file`. `severity` is `error`, `warning`,
  or `info` (aliases `high`, `medium`, `low` accepted on ingest).
- `confidence` is `high`, `medium`, or `low`, and the report folds the low ones
  away. Report a finding you are unsure of with low confidence rather than
  withholding it: an uncertain finding costs the reader nothing, a withheld one
  costs them the finding.
- `category` accepts `correlation` for a finding that connects two others.
  `relatedFindings` holds the `id` of each prior it builds on, and the report
  renders the connection against both rather than duplicating the text.
- Zero comments is a valid and expected result. Roughly three in ten changes
  deserve none. Do not pad.

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
    "ruling": "should-fix",
    "rationale": "the error path is real, this is not a fixture",
    "fix": "wrap the call with %w and drop the nolint"
  }
}}
```

- `ruling` is one of **justified** (the silencing is warranted),
  **should-fix** (fix the code rather than hide the finding), or
  **rule-noisy** (the rule fires too often and should be tuned or scoped).
- `rationale` is one line: why.
- `fix` is your recommendation for how to resolve the finding, one or two lines.
  Optional: leave it out when the ruling stands on its own.
- Key on the exact `fingerprint` string Redline emitted. A verdict that
  matches no finding is dropped.

Then re-run `redline run` with the same target flags: it merges the verdicts
onto the findings and renders them. Redline never writes `review.json` and
never overwrites it, so your judgments survive a re-run the way
`comments.json` does.

Judge only the suppressions here. This is triage of Redline's own evidence,
not a second pass over the whole diff.

## Judge the mutation survivors

When a gomutants report is present, `mutation.survived[]` in `findings.json` lists
the mutants a test ran but did not catch. Each carries a `key`: gomutants' own
mutant `id` when the report has one (v0.6.0 and later), and
`<path>:<line>:<mutator>` when it does not. The id survives a rebase that moves
the line, so use whatever `key` says rather than rebuilding it. A survivor with
an `id` also carries the command that re-runs that one mutant. Rule on the ones
worth judging and write them to the same `.redline/review.json`, under
`mutationVerdicts`, keyed by that `key`:

```json
{"mutationVerdicts": {
  "internal/foo.go:42:CONDITIONALS_BOUNDARY": {
    "ruling": "needs-test",
    "rationale": "the boundary is a real off-by-one; add a case at the edge"
  }
}}
```

- `ruling` is one of **needs-test** (a test should kill this mutant, write it),
  **equivalent** (no test can, the mutant does not change behavior), or
  **acceptable** (the survivor is fine as is, for example a performance heuristic).
- `rationale` is one line: why.
- Key on the exact `key` string from `mutation.survived[]`. A verdict matching no
  survivor is dropped.

Re-run `redline run` with the same target flags and the verdict renders beside
the survivor in the Mutation section.

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
