# Copilot comparison — MKT-1360

> Redline review of NetApp/marketplace-cp PR #1360 held up against GitHub Copilot's
> review of the same PR. Both ran on the same head; Copilot on commit `a41ed8e5`,
> Redline on `399b4159:5362ef6a`. 70/82 files in scope for each, same 12-13
> generated files excluded. Redline artifacts under `../mct/.redline`.

## Why this PR is a good test

PR #1360 states one load-bearing invariant in its own description: "shared
evaluation timestamp so expansion counts and destination lists agree." The
whole feature is that a buyer Attention count and the drill-through list you
land on are the same set, evaluated at one `asOf`. That is a checkable claim,
and it is exactly where the real bugs turned out to be. So the PR doubles as a
test of whether each reviewer can hold an invariant and trace it across the
code.

## What each reviewer produced

Copilot: 7 inline comments, all correctness or design, plus one summary
verdict. No lint, no coverage, no structure.

Redline: 21 findings. 5 are LLM agent comments (2 warning, 3 info), each
self-adjudicated with a kept/unverifiable ruling. The other 16 are
deterministic: 6 vacuum lint, 1 oasdiff, long-function, test-only-live, 4
sibling-missing-file, assertions-removed, source-without-test, plus coverage
(85% of added lines) and the API-contract delta.

## Overlap: one finding, and Copilot's cut was sharper

Both flagged the chip-lock helper in `ui/src/lib/activeListFilterChips.ts`, from
opposite directions.

- Redline c3 (info, kept): the lock needs all three of `buyerId`, `attentionRule`,
  `evaluationAt`; drop `evaluationAt` and the chips silently unlock, loosening
  exact-membership to current-state filtering.
- Copilot #6: the lock keys off raw non-empty strings, not the rule the page
  parser actually accepts. An entitlement-only rule on `/offers` makes
  `readOfferAttentionRule` return undefined, so the request is only
  buyer-filtered, yet the chips still render immutable and "Clear all" preserves
  them. The chips misrepresent the applied filter.

Same root cause, a string-based lock instead of a semantic one. Copilot named a
concrete user-visible wrong state; Redline named the inverse edge and rated it
low.

## Bugs Copilot found that Redline missed, two verified in source

- Copilot #1 (`api/endpoints/catalog/offers.go:303`), verified. `endDateBoundary
  = UTCStartOfDay(now)` and `HidePastEndDateCommerciallyActiveAt = now` (lines
  293, 300) use the request's current day, but `AttentionAsOf =
  EvaluationAt.Or(now)` (line 303). An accepted-not-subscribed drill-through
  carrying `hidePastEndDate=true` prunes offers by today, not by `evaluationAt`,
  so a frozen list drifts from the group count after the snapshot day.
- Copilot #3 (`internal/data/entitlement_list_filters.go:80`), verified. The
  attention predicate threads `asOf` only into `buyerAttentionEntitlementRules(...)`
  to build rule opts (lines 77-80). The two usage-window rules bottom out in
  `entitlementListNoUsageWithinDaysPredicate` and
  `entitlementListUsageWithinDaysPredicate`, which hardcode `CURRENT_DATE - days`
  (lines 304, 313, 329). Reopening a saved drill-through on a later UTC day
  shifts membership. This is the PR's stated invariant, broken.
- Copilot #2 (`api/openapi.yaml:7449`): member-ID arrays are required and
  unbounded by buyer size, while the UI reads only each group's `count`.
- Copilot #4/#5 (`CustomerFocusBuyerExpansionPanel.tsx:325,341`): the attention
  and summary React Query keys sit outside the `['buyers', organizationId]`
  prefix the refresh button invalidates, so an expanded panel stays stale up to
  10 minutes after a view refresh.
- Copilot #7 (`api/openapi.yaml:4241`): the spec promises 403 for any
  inaccessible product, but the session-visible path returns 404 and the
  integration test asserts it.

Redline's own "what could not be determined" section names the blind spot for
#7 (its api pane reads inline schemas only, follows no `$ref`) and for the UI
cache behavior (ui pane checks 15-16 not built). So two of Copilot's findings
landed exactly in gaps Redline already knew it had and printed for the human,
but did not route to the agent reviewer.

## Findings Redline had that Copilot did not

- c1/c2 (warning): the entitlement and offer attention predicates hardcode
  `IncludeTestProducts=true` when rebuilding the rule WHERE. Redline raised a
  possible test/dev leak, then ruled both "kept" after concluding there is no
  leak because the outer caller-scoped test predicate is ANDed separately.
  Raised, investigated, self-resolved to a non-issue.
- c5 (info, unverifiable): exported `BuyerAttentionRuleIDs` is dead production
  code, test-only. Overlaps its own deterministic `test-only-live` lint.
- c4 (info, unverifiable): two extra repository round trips per request on an
  always-mounted panel. Load note, marked unverifiable.
- The whole deterministic class Copilot structurally does not do: 6 new vacuum
  violations (missing descriptions on the 5 new `BuyerAttention*` schemas and
  the `buyerId` param), a 256-line test tripping long-function, sibling-file
  gaps, a net -4 assertions in `featureflags_test.go`, `internal/observability`
  changed with no test, and 85% line coverage on added lines with a per-file
  uncovered breakdown.

## Read

On finding bugs in this PR, Copilot won. It surfaced 3-4 substantive
correctness or scaling defects, and the two most important are real in the
source. Those are precisely the count/list parity the PR was built to
guarantee.

Redline's LLM layer largely missed that axis. Its two warnings were about a
test-products predicate it then talked itself out of; two of its three infos
came back unverifiable because no lookup evidence was gathered. It reasons
locally and defensively rather than testing the PR's declared invariant.

Redline's deterministic half is a real moat Copilot has nothing for: lint delta,
coverage on added lines, structural parity, assertion drop. The conclusion is
not that one tool wins. It is that Redline's agent reviewer needs to reason
about invariants and trace values across call seams, because that is the class
of bug it lost on here.

## Lessons

Filed as enhancement items in [../plans/potential-enhancements.md](../plans/potential-enhancements.md)
under "Review quality (Copilot comparison, MKT-1360)".

1. Extract the PR's stated invariants from the description and rationale, then
   task the reviewer to falsify each one against the code. Copilot did this by
   default; Redline treated the description as prose.
2. Trace a snapshot/as-of value through the call graph and flag every sibling
   time source (`now`, `CURRENT_DATE`, `UTCStartOfDay(now)`) not derived from it.
   That single check catches both #1 and #3 mechanically.
3. The ruling loop is spending budget confirming non-bugs (c1/c2) and shrugging
   at empty lookups (c4/c5). An empty lookup should re-query, not conclude
   unverifiable. Value is in hypothesis breadth, not defending the first guess.
4. Route the files that deterministic panes disclaim (the `$ref` api blind spot,
   the unbuilt UI checks) into the agent reviewer as targeted tasks. The "could
   not determine" list should be an input to the agent, not only output to the
   human.
5. Two whole classes have no detector: response shape (a collection field whose
   size scales with entity count and no pagination) and React Query cache-key
   hygiene (a new `useQuery` key not nested under the invalidated parent prefix).
   Both are structural heuristics, not one-offs.
6. Agent-comment coverage was thin and lopsided: 5 comments across 4 files,
   skewed to the two SQL predicate files. Check whether the reviewer is capped
   per file or per finding-count in a way that starves later files.

## Report integrity bug

`report.md` says "review.json was written against `6e3c4169:75c46156` ... so no
agent review was merged," yet `review.json` records `399b4159:5362ef6a` (the
change under review) and carries the five comments the report then prints. The
debug review request and response are fresh. The staleness note and the merged
findings contradict each other, so a human is told the review did not apply
while reading its findings. The revision-sync check needs to be fixed so those
two cannot disagree.
