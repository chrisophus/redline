# 01-01 Summary — Persist intent and surfaces through parse and omit-keep merge

**Status:** Complete
**Files modified:** `internal/packet/ingest.go`, `internal/packet/ingest_test.go`

## What shipped

Grew `packet.Review` with two optional pointer fields and taught `Merge` to
empty-keep them the same way it already keeps `summary` and `files`.

New production symbols in `internal/packet/ingest.go`:

- `Review.Intent *Intent` (JSON key `intent`, omitempty) — inserted after `Summary`.
- `Review.Surfaces *Surfaces` (JSON key `surfaces`, omitempty) — inserted after `Intent`.
- `type Intent { PR *IntentPR; Ticket *IntentTicket }`
- `type IntentPR { Number int; Title, URL string }` — `Number` is `int` to match `target.PullRequest.Number`.
- `type IntentTicket { ID, Title, URL string }` — `ID` is a string (e.g. `REL-24`).
- `type Surfaces { Interface, API, Schema *Surface }` — fixed keys, not a string-keyed map.
- `type Surface { Line string; Moved bool }` — `moved` carries **no** omitempty, so `false` survives marshal and a quiet idle line is real content.
- Empty helpers `intentEmpty`, `intentPREmpty`, `intentTicketEmpty`, `surfacesEmpty`, `surfaceEmpty`.
- `Merge` gained `if intentEmpty(out.Intent) { out.Intent = prev.Intent }` and the matching `surfaces` branch, after the existing `Unknowns` keep.

## Merge semantics

Whole-object replace, exactly like `files`:

- Omitted or empty `intent`/`surfaces` on the next review keeps the previous object.
- A non-empty supplied `intent`/`surfaces` replaces the entire previous object (a next intent with only a PR drops a previously stored ticket).
- A surface with a quiet `line` and `moved:false` is **not** empty; `{moved:false}` with no line **is** empty and cannot wipe the previous value.

## Tests

- `TestParseReviewIntentAndSurfaces` (new) — full briefing JSON parses; `moved:false` preserved; `apiChanges` still parse beside surfaces.
- `TestMergeKeepsOmittedNarrative` (extended) — prev intent + surfaces survive a findings-only follow-up.
- `TestMergePrefersTheNewReviewWhereItSpeaks` (extended) — non-empty next intent/surfaces win entirely, dropping prev's sibling objects.

## Held boundaries

`packet.Version` stays `1`. `ParseReview` still uses `json.Unmarshal` with unknown keys ignored. No `goal` field, no second headline, no model client, no URL fetch. `apiChanges`/`schemaChanges`/`screenshots` untouched. Only `ingest.go` and `ingest_test.go` changed.

## Verification

```
go test ./internal/packet/ ./cmd/redline/ -count=1   # ok
go test ./... -count=1                                # all packages ok
go vet ./internal/packet/                             # clean
```
