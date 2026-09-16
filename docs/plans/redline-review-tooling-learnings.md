# Redline review tooling — learnings and follow-up ideas

> **Ephemeral plan doc** — captures what came out of a long investigation comparing GitHub
> Copilot, redline, and Alibaba's Open Code Review (`ocr`) against PR #1360 (MKT-1297), which
> led to building a TypeScript context provider (`tsrefactor`) and two new redline flags. Archive
> once the follow-up ideas below are either built or explicitly dropped.

## Origin

PR #1360 shipped a canonical buyer-Attention read model and a production Customer Focus
expansion panel. The real GitHub Copilot review on that PR (commit `a41ed8e5`) found 7 real
issues, including a stale React Query cache key spanning two files
(`CustomerFocusBuyerExpansionPanel.tsx`'s query keys vs. `BuyersPage.tsx`'s refresh invalidation
prefix). Two independent redline runs against the same commit missed it, and mostly missed the
frontend entirely. That gap is what the rest of this doc is downstream of.

## What we found and built

### 1. Redline's context packet was Go-only

Redline's LLM prompt is built from per-language **context providers** (`docs/context-envelope.md`
in the redline repo): `gorefactor` resolves real call-graph/type context for Go via `go/types`.
Nothing equivalent existed for TypeScript, so every `ui/**` file got reviewed from the raw diff
alone — `CustomerFocusBuyerExpansionPanel.tsx` had **zero** expansions of any role, in either
redline run.

### 2. graphify (tree-sitter) is not a substitute for a real resolver

We tested adding a `graphify`-backed context provider (tree-sitter parsing, ~37 grammars). It
never captured that `BuyersPage.tsx` imports/calls `useInvalidateViewQueries` at all — resolving
a destructured hook return needs real symbol binding, which tree-sitter's syntax-only parsing
doesn't do. graphify's actual value is the language-agnostic tail neither `gorefactor` nor a
TS-specific resolver will ever cover (SQL, YAML, Terraform, shell, Postman JSON) — not frontend
call-graph resolution.

### 3. Built `tsrefactor` — a TypeScript sibling to `gorefactor`

New repo, `ts-morph`-backed, mirrors `gorefactor`'s `internal/changectx` pipeline exactly (same
envelope contract, same roles, same numeric caps, same determinism guarantee — verified
byte-identical across repeat runs). Wired into `.redline.yml` scoped to `ui/**/*.ts(x)`.

Verified it closed the actual gap: `CustomerFocusBuyerExpansionPanel.tsx` went from zero
expansions to 14 real ones, including the function containing its `useQuery` calls. Redline's own
maintainer added a new `removal` role to redline's core vocabulary in direct response to what
`tsrefactor` introduced — now rank 2, ahead of `type`/`sibling`/`test`/`history`.

### 4. Fixing context coverage did not fix the miss

This is the most important finding. With `tsrefactor` wired in, both files' relevant code —
the panel's `useQuery` calls and `BuyersPage.tsx`'s `useInvalidateViewQueries` call — were
confirmed present and **unclipped** by the token ceiling in the actual reviewed prompt. The
reviewer still didn't catch the bug.

We ran a `--debug --thinking` diagnostic pass and read the actual reasoning trace. The model
applied the exact right rule — *"a query key is a contract... a new or renamed key that no
invalidation matches serves stale data without an error"* (already present in `tsrefactor`'s
prompt fragment, written independently before we ever found this bug, drawing on general React
Query idiom knowledge) — **correctly, multiple times, in the same trace**: it checked whether
`attentionQuery`'s key correctly excludes `asOf`, traced `offersFilterCacheKey` propagation,
checked a retry button's `refetch()` semantics. It never happened to ask the one specific
cross-file question that catches this bug: *what does `BuyersPage`'s refresh button actually
invalidate, and does it cover this component's queries?*

**Conclusion:** this class of bug needs a targeted check, not more context or more idiom
knowledge in the prompt. Neither is sufficient on its own.

### 5. Comparison with `ocr` (Alibaba Open Code Review)

Live agentic tool (`code_search` loop per file-group) vs. redline's single context-envelope +
one model call. Findings:

- `ocr` independently reproduced one real Copilot finding and found one genuinely novel bug
  (`OffersPage.tsx` `clearAllFilters` missing `productIds`), but also produced two confidently
  asserted **false positives** (`IncludeTestProducts: true` hardcoded — verified safe by reading
  the redundant outer clause both times) at "high" severity.
- No generated-file classification at all (unlike redline's `gorefactor`, which classifies
  `.pb.go`/`oas_*`/anchored `Code generated` headers and summarizes rather than deep-reads them).
  A `--rule` exclude list fixed the specific waste (120 tool calls on one generated OpenAPI file)
  but didn't fix the underlying gap.
- `--max-tokens-budget` did not hold reliably — two separate runs blew past a 5M-token cap by
  150–235% before self-stopping.
- Cost: ~$20+ and climbing for one medium-effort run vs. redline's ~$0.87–1.41 per run.

Net: redline is cheap and precise where it has a resolver; `ocr` has real reach but an unreliable
cost/precision profile that doesn't hold up at this repo's scale.

### 6. Cost mechanics of the staged/cohort pipeline

Built and shipped in redline `v0.10.1` (`--plan`, `--only-cohorts`). Key things learned running it
for real, worth remembering before reaching for it again:

- **Splitting into cohorts doesn't shrink input.** Every call — stage one and every cohort call —
  carries the *whole* diff+context prefix, because that's what the prompt cache is keyed on.
  Narrowing only shrinks the *output task* (which files this call has to judge), not the tokens it
  reads.
- **`--plan` isn't a free preview.** It still pays the full cache-write for the whole prefix
  (~$0.72–0.95 in our runs). It only pays off if you follow through with at least one cohort call
  that reads that cache. Run it standalone repeatedly and you're paying full price for nothing.
- **Cohort names are non-deterministic across separate invocations** — stage one is a live model
  call, so the same change can get described and split differently every time. A selector from an
  earlier `--plan` is not guaranteed to match a later real run's names. This is why
  `--only-cohorts` falls back to matching a file path inside a cohort when no name matches — the
  one identifier that's actually stable.
- **`--thinking` is a large, uncertain-ROI multiplier**, not a small one. A single 8-file cohort
  with `--thinking` on took 10m26s and $1.41 (62k reasoning tokens) vs. 8.6s and $0.056 without —
  and produced 3 findings (2 genuinely good, extending a known bug's scope; 1 false positive on
  investigation) rather than something qualitatively better.
- **Narrowing does buy real precision on the assigned territory** — the single-cohort run found
  a `cloud`/`showTest` extension of the `clearAllFilters` bug that no full-43-file run (redline,
  `ocr`, or Copilot) surfaced. It just isn't a guaranteed fix for a specific known miss; it's a
  different, real recall improvement on whatever *is* in that cohort.

## Verified findings against `main` (post-merge audit)

Cross-checked every "valid" finding from every review pass in this investigation directly against
current `main` source. Full table and Jira draft are in the conversation this doc summarizes;
short version:

| Status | Count | Examples |
|---|---|---|
| Fixed before/at merge | 5 | stale cache key, `EndDateBoundary`/`evaluationAt` drift, unbounded Attention payload, 403/404 doc mismatch, chip-lock wrong-rule-for-page |
| **Still open on `main`** | 2 | `clearAllFilters` scope leak (`cloud`/`productId(s)`/`showTest` not guarded behind the attention lock); no-usage-14d rule missing `UsageHistory: Has` (silently broadened) |
| False positive (verified, not real) | 2 | `ocr`'s `IncludeTestProducts` hardcoded-true claims (×2); a Rules-of-Hooks concern on `BuyerSubRowRenderer` (verified safe by reading the actual BXP `Table` source — it renders via `React.createElement`, proper fiber) |
| Unresolved / low confidence | 1 | a possible missing beneficiary-key substring guard on a scoped offer-attribution SQL path |

The two still-open issues are drafted as a Jira ticket (pending — Jira MCP wasn't connected this
session; full issue text is in the conversation, ready to file).

## Follow-up ideas (not yet built)

### A. Incremental cohort review across PR review rounds

**The idea:** on round 2+ of reviewing the same PR, only review the cohort(s) whose files changed
since the last review round, instead of re-drawing the partition and re-reviewing everything.

**Why it's feasible now, not just in theory:** CI already uploads the *entire* `.redline/`
directory (`session.json`, `review.json`, `findings.json`, `evidence/`) as a GitHub Actions
artifact per run — `redline-pr-<PR>-<run_id>`, 14-day retention
(`.github/workflows/review-bot-redline.yml`). A later CI run for the same PR could fetch the most
recent prior artifact via the GitHub Actions API before running redline again.

**What's actually missing:**
- `Cohort`/partition persistence in `session.json` isn't wired up for reuse — today a staged run
  always redraws it live.
- A lookup for "what was the head SHA of the last review of this exact target" — the ledger
  already records `revision: "baseSHA:headSHA"` per review, but nothing queries it by target
  (PR number) today.
- Mapping `git diff <lastSHA>..<newHEAD>` onto the saved partition's file lists (reusing
  `selectCohorts`'s matching logic, driven by file paths rather than a typed selector), with a
  fallback for files that are new or moved since the saved partition was drawn (similar to
  `repairPartition`'s existing "unmatched files join the first/largest cohort" behavior).
- A CI step to restore the downloaded prior artifact into the workspace before `redline run`.

**Real cost mechanism, stated correctly:** this does *not* save the cache-write (the prompt cache
TTL is 5m/1h, far shorter than a human's PR-feedback turnaround, so it's long expired by round 2).
What it saves is stage one's own redraw cost, and every cohort call for files that didn't change.
For a typical small-fix review round (1–2 touched cohorts out of 5–6), that's a real, meaningful
saving — just not the mechanism it first sounds like.

### B. Richer per-cohort context when reviewing narrow

A single-cohort review leaves most of the 250k-token ceiling unused (only that cohort's files'
`enclosing`/`caller`/`type` content competes for room, not the whole diff's). That headroom could
carry context a full-diff review has to drop for budget reasons today:

- Relevant docs/ADRs/runbooks for the touched area (e.g. this repo's `docs/architecture/designs/`
  entries, `.cursor/rules/*.mdc` files scoped to the touched paths).
- Fuller test-file context (today's `test`-role expansions are held back by default unless the
  change is test-only).
- The language-agnostic tail (SQL/YAML/Terraform/shell) via a `graphify`-backed provider, which a
  full review currently can't afford room for.

Untested — worth trying once (A) exists, since it's the same "narrow scope, spend the freed
budget elsewhere" idea applied to context instead of just findings.

### C. Open question: does a dimension-specific prompt help at all?

The "query key is a contract" idiom guidance was already in `tsrefactor`'s prompt and still
didn't get applied to the one case that mattered. We haven't tested whether an explicit,
narrower instruction — e.g. a distinct pass or rule specifically asking "for every changed
`useQuery`/mutation, name what invalidates it and confirm it's covered" — would actually catch
this class of bug, versus just being more idiom prose the model reads and doesn't apply. Worth a
controlled test before assuming either "add more prompt" or "narrow the review" is the fix.

### D. `redline post --profile` discards a completed, paid-for review on a HEAD race

Hit this directly in CI:

```
redline: profile require_head: session reviewed 8176901e but PR head is db9949a3; re-run `redline run --pr 1412`
exit status 1
make: *** [Makefile:1053: redline-post] Error 1
```

**Root cause, read from `cmd/redline/post.go`:** a `--profile` with `require_head: true` (the
default, and what `.github/review-bot-gate-profile.yml` uses) calls `enforceProfile` *before* the
review payload is even built. If the PR's live head has moved since `redline run`/`review`
executed — a real race any time a new commit lands on the PR between the review step and the post
step in the same CI run — `enforceProfile` returns a hard error and `cmdPost` aborts. The entire
review, already computed and already paid for, is never posted. Nothing.

**The fix already exists in the same file, just not wired to this path.** The *non-profiled*
`redline post` flow (a few lines below `enforceProfile` in `cmdPost`) handles the identical
staleness case by degrading instead of failing: it detects `head != tgt.Head`, prints a warning,
and prepends an honest notice to the review body — *"This review is of `<old-sha>`, which is no
longer the head of this pull request (`<new-sha>`). Findings below may already be addressed."* —
then **still posts it**. `enforceProfile`'s `RequireHead` check should do the same thing instead
of hard-failing: post the review with that same staleness notice, and only withhold whatever
*gate verdict*/freshness credit the profile would otherwise grant (so a stale review still can't
satisfy `/review-bot`'s "one qualifying review covers current HEAD" requirement — that part of
`require_head`'s intent is legitimate and should stay). The two concerns — "don't post anything
stale" and "don't let a stale review count as fresh" — are currently conflated into one hard
failure; only the second one actually needs to be enforced.

**Why it matters beyond one CI run:** every profiled post that loses this race silently burns the
review's cost with zero output, and produces a confusing CI failure that reads as a tooling bug
rather than "a commit landed mid-run" — worth fixing given real API cost is on the line, not just
convenience.
