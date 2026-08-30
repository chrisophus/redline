# 01-02 Summary — Fail closed on bad types; empty objects keep previous values

**Status:** Complete
**Files modified:** `internal/packet/ingest_test.go`

## What shipped

Test-only plan. Expanded the 01-01 contract with fail-closed parse edges,
omitted-link success, and empty-object keep. No production change was needed —
01-01's typed structs and empty helpers already satisfy every case.

## Tests added to `internal/packet/ingest_test.go`

- `TestParseReviewRejectsInvalidIntentType` — `"intent":"PR #1"` fails with an error containing `review is not valid JSON`.
- `TestParseReviewRejectsInvalidMovedType` — `surfaces.interface.moved:"yes"` fails with the same wrapped error.
- `TestParseReviewIgnoresUnknownGoal` — `"goal":"…"` beside `summary` parses; `Summary` is set; no second headline field exists.
- `TestParseReviewAllowsOmittedIntentLinks` — summary-only JSON leaves `Intent` nil; an `intent:{}` object parses with nil PR and nil Ticket, not an error.
- `TestMergeKeepsEmptyIntentAndSurfaces` — next carrying `Intent:&Intent{}` and `Surfaces:&Surfaces{}` (empty structs, not nil) keeps prev's values.

## Held boundaries

Decoder still ignores unknown keys (no `DisallowUnknownFields`). `cmdIngest`
still goes `ParseReview → Merge → write` with no generation or HTTP client and
no `intent.pr` vs packet-target comparison. `packet.Version` stays `1`. No
edits to `html.go`, `markdown.go`, skills, `DefaultGuidance`, or `cmd/redline`.

## Verification

```
go test ./internal/packet/ -count=1   # ok
go test ./cmd/redline/ -count=1        # ok (ingest path stays model-free)
go test ./... -count=1                 # all packages ok
```
