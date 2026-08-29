# Phase 1: Briefing contract - Research

**Researched:** 2026-08-29
**Domain:** Brownfield Go CLI ingest/merge contract (`github.com/ccason/redline`)
**Confidence:** HIGH

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- **D-01:** The briefing headline is the existing ingest field `summary`. Do not add a second `goal` field. — **Reversibility:** costly — HTML, markdown, and Merge already key off `summary`.
- **D-02:** Add an optional `intent` object on `Review` for links the briefing can open: `pr` and `ticket`. Both may be absent. — **Reversibility:** costly — this becomes the published ingest contract skills will emit.
- **D-03:** `intent.pr` is agent overlay: `{number, title, url}` (all omitempty). When the session target was `--pr`, `packet.target.pr` already has number, title, URL, refs. The briefing (Phase 2) may show that PR even if the agent omitted `intent.pr`. If the agent supplies `intent.pr`, that value wins on merge. Do not fail ingest when they disagree.
- **D-04:** `intent.ticket` is agent-only. Redline does not fetch Jira or any ticket host. Shape: `{id, title, url}` all omitempty.
- **D-05:** Missing PR or ticket is not an error. Omit those links. `summary` still required for a useful briefing; empty `summary` keeps today's fallback copy in the report.
- **D-06:** Add optional `surfaces` on `Review` with fixed keys `interface`, `api`, `schema`. Each value: `{ "line": "<one sentence>", "moved": bool }`. These are the tile one-liners. — **Reversibility:** costly — Phase 2 tiles bind to these keys.
- **D-07:** Do not replace `apiChanges` or `schemaChanges`. Those stay as highlight lists for drawer evidence in Phase 2. Screenshots stay the Interface evidence. Surfaces are the map; highlights/shots are the drill.
- **D-08:** If `surfaces` is omitted, do not invent lines in Phase 1. Phase 2 may treat a missing surface as idle unless existing highlights, screenshots, or a migration pane said otherwise. Phase 1 only stores what the agent sent.
- **D-09:** `Merge` treats `intent` and `surfaces` like `summary` / `files`: omitted or empty keeps the previous value; a non-empty supplied field always wins. No v1 way to "clear" intent. — **Reversibility:** costly — comment-loop ingest depends on this.
- **D-10:** Do not bump `packet.Version`. Packet shape does not change. Review JSON grows; unknown fields stay ignored (stdlib `encoding/json`). Invalid types still fail parse.
- **D-11:** `redline ingest` still never calls a model. Extra narrative is agent-authored or already on the packet target.

### Claude's Discretion
- Exact Go field names and JSON tags, as long as they match D-02 / D-04 / D-06.
- Whether `moved` is required or inferred from a non-empty `line` (prefer explicit `moved` so "did not move" can still carry a quiet line).
- Tests: extend `TestMergeKeepsOmittedNarrative` rather than a new merge story.

### Deferred Ideas (OUT OF SCOPE)
- Briefing HTML, drawer, coverage banner on the new page — Phase 2
- Fix queue JSON + clipboard — Phase 3
- Cursor / Claude / Copilot skill text that fills `intent` and `surfaces` — Phase 3 (AGT-02)
- React + Tailwind compile-to-embed — later milestone
- Fetching tickets from Jira — out of scope
- ⚠ Unpackaged sketches — run `/gsd-sketch --wrap-up` if you want a findings skill; raw manifest is already a canonical ref
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| AGT-01 | Ingest accepts first-class intent and surface fields and merges them the same way as `summary` (omitted field keeps the previous value) | Grow `packet.Review` + `Merge` empty-keep helpers; extend `TestMergeKeepsOmittedNarrative` |
| AGT-03 | The Redline binary still makes no model calls | `cmdIngest` only `ParseReview` → `LoadSession` → `Merge` → `write`; no LLM client exists in the ingest path |
| INT-01 | The briefing headline is the agent's goal sentence for the change (ingest `summary`) | Do not add `goal`. Existing `Review.Summary` remains the headline. Phase 1 persists; Phase 2 renders |
| INT-02 | When the agent supplies a pull request, the briefing shows it and opening it explains repo, number, and branch | Persist `intent.pr` `{number,title,url}`. Do not copy or fail against `packet.target.pr`. Display is Phase 2 |
| INT-03 | When the agent supplies a ticket, the briefing shows it and opening it explains the ticket | Persist `intent.ticket` `{id,title,url}`. No Jira fetch. Display is Phase 2 |
| INT-04 | Missing PR or ticket does not break the briefing — those links are omitted; the goal sentence still shows | Optional pointers; parse succeeds without them; empty `summary` keeps today's report fallback |
</phase_requirements>

## Summary

Phase 1 is a schema-and-merge increment on the existing agent review JSON. The ingest path already parses stdin, merges into the session `Review`, and persists it in `session.json`. The planner should add two optional objects on `packet.Review` — `intent` and `surfaces` — and teach `Merge` the same omit-or-empty-keeps-previous rule already used for `summary` and `files`. Packet encoding, HTML, markdown, skills, and `DefaultGuidance` stay untouched.

The comment-loop is the load-bearing constraint. A second ingest that carries only findings must not blank the briefing fields. That is already true for `summary` / `files` / highlights / screenshots; `intent` and `surfaces` must join that list as whole-field replaces, not a new merge story.

**Primary recommendation:** Add pointer-typed `Intent` and `Surfaces` on `Review`, empty-helpers that match today's `== ""` / `len == 0` checks, extend `TestMergeKeepsOmittedNarrative`, and leave `packet.Version` at `1`.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Parse agent review JSON | API / Backend (`packet.ParseReview`) | — | Stdlib `json.Unmarshal` on stdin; fail-closed on type errors |
| Persist intent + surfaces | Database / Storage (`session.json` via `run.SaveSession`) | — | `Review` is already a field on the session snapshot; new exported fields persist automatically |
| Merge omit-keep | API / Backend (`packet.Merge`) | — | Comment-loop depends on whole-field empty-keep; do not invent a second merge path |
| Goal headline | API / Backend (`Review.Summary`) | Frontend (Phase 2 HTML) | D-01: existing `summary`; Phase 1 does not render |
| PR overlay | API / Backend (`Review.Intent.PR`) | Packet target (`target.PullRequest`) | Agent overlay wins if supplied; `--pr` metadata stays on the packet for Phase 2 fallback |
| Ticket overlay | API / Backend (`Review.Intent.Ticket`) | — | Agent-only; no ticket host |
| Surface one-liners | API / Backend (`Review.Surfaces`) | — | Store only what the agent sent; do not invent idle copy |
| Drill evidence | Existing `apiChanges` / `schemaChanges` / `screenshots` | Phase 2 drawer | D-07: keep highlight lists; do not replace them |
| Report HTML / drawer | Deferred (Phase 2) | — | Out of scope |
| Skill prompts | Deferred (Phase 3) | — | Out of scope |
| Model calls | Forbidden | — | Binary never calls a model |

## Existing Types and Merge Semantics

### `packet.Review` today

[VERIFIED: internal/packet/ingest.go:14-33]

```
type Review struct {
	Summary string `json:"summary"`
	Files []FileNote `json:"files,omitempty"`
	APIChanges    []Highlight `json:"apiChanges,omitempty"`
	SchemaChanges []Highlight `json:"schemaChanges,omitempty"`
	Findings      []Judgment  `json:"findings,omitempty"`
	Unknowns []string `json:"unknowns,omitempty"`
	Screenshots []Shot `json:"screenshots,omitempty"`
}
```

There is no `goal`, `intent`, or `surfaces` field. Do not add `goal`.

Neighbor types that stay as-is:

[VERIFIED: internal/packet/ingest.go:49-55]

```
type Highlight struct {
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	File     string `json:"file,omitempty"`
	Breaking bool   `json:"breaking,omitempty"`
}
```

[VERIFIED: internal/packet/ingest.go:43-47]

```
type FileNote struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}
```

### `Merge` today

[VERIFIED: internal/packet/ingest.go:108-134]

```
func Merge(prev, next *Review) *Review {
	if next == nil {
		return prev
	}
	if prev == nil {
		return next
	}
	out := *next
	if out.Summary == "" {
		out.Summary = prev.Summary
	}
	if len(out.Files) == 0 {
		out.Files = prev.Files
	}
	if len(out.APIChanges) == 0 {
		out.APIChanges = prev.APIChanges
	}
	if len(out.SchemaChanges) == 0 {
		out.SchemaChanges = prev.SchemaChanges
	}
	if len(out.Screenshots) == 0 {
		out.Screenshots = prev.Screenshots
	}
	if len(out.Unknowns) == 0 {
		out.Unknowns = prev.Unknowns
	}
	return &out
}
```

Rules the planner must copy, not reinvent:

1. Start from `*next` (`out := *next`).
2. Empty string / empty slice copies the previous value.
3. A supplied non-empty field replaces the whole previous value (files is a whole-slice replace, not per-path merge).
4. `Findings` are **not** empty-kept. The stored `Review.Findings` is the latest ingest's judgments. `Apply` appends those judgments onto `findings.Report` separately. Do not add a findings keep.
5. Nil `next` returns `prev`; nil `prev` returns `next`.

`intent` and `surfaces` must follow rule 2–3 as **whole objects**: omitted or empty keeps the entire previous object; a non-empty supplied object replaces the entire previous object.

### Parse path

[VERIFIED: internal/packet/ingest.go:84-98]

```
		return nil, fmt.Errorf("empty review; expected findings JSON on stdin")
...
	if err := json.Unmarshal([]byte(text), &rev); err != nil {
		return nil, fmt.Errorf("review is not valid JSON: %w", err)
	}
```

`ParseReview` already strips a fenced ` ``` ` block, then `json.Unmarshal`. Unknown keys stay ignored. Type mismatches return `UnmarshalTypeError` wrapped as `review is not valid JSON`. Do not switch to `Decoder.DisallowUnknownFields`. Do not migrate to `encoding/json/v2`.

### Where the review lives

[VERIFIED: internal/run/session.go:18-24]

```
type session struct {
	Report   findings.Report          `json:"report"`
	Packet   *packet.Packet           `json:"packet"`
	Renders  []pane.Render            `json:"renders,omitempty"`
	Evidence map[string]pane.Artifact `json:"evidence,omitempty"`
	Review   *packet.Review           `json:"review,omitempty"`
}
```

[VERIFIED: cmd/redline/main.go:159-182]

```
	rev, err := packet.ParseReview(os.Stdin)
...
	res.Review = packet.Merge(res.Review, rev)
```

Adding exported fields to `Review` is enough for persist and reload. `cmdIngest` already merges. No new CLI flag, no second ingest path, no session version.

`cmdIngest` already rejects a **CLI** `--pr` that does not match the session target (`res.Target.Matches`). That check is about `review --pr 123` then `ingest --pr 456`. It must **not** grow a comparison of `intent.pr` to `packet.target.pr`.

### Packet version (do not bump)

[VERIFIED: internal/packet/packet.go:18-19]

```
// Version guards the packet encoding.
const Version = 1
```

`packet.Version` guards `Packet`, not review JSON. Review JSON has no version field. Leave `Version` at `1`.

### `--pr` metadata already on the packet

[VERIFIED: internal/target/target.go:45-55]

```
type PullRequest struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Author      string   `json:"author"`
	URL         string   `json:"url"`
	BaseRefName string   `json:"baseRefName"`
	HeadRefName string   `json:"headRefName"`
	Files       []string `json:"files,omitempty"`
	Draft       bool     `json:"draft"`
}
```

Phase 1 must **not** copy `packet.target.pr` into `Review.Intent`. Phase 2 reads `Review.Intent.PR` first and falls back to `packet.Target.PR`.

### Report fallback (do not change)

[VERIFIED: internal/report/html.go:172-174]

```
	if v.Summary == "" {
		v.Summary = "No agent summary. Redline emits evidence; the plain-language account of the change comes from the agent driving it — run `redline review` and pipe the result to `redline ingest`."
	}
```

Empty `summary` already has fallback copy. Phase 1 does not edit `html.go` or `markdown.go`.

### Agent boundary

[VERIFIED: internal/packet/packet.go:4-8]

```
// The boundary is deliberate and narrow. Redline never calls a model — the
// prose and the judgment come from the session driving it, which is why this
// works identically in Claude Code and in Cursor. Redline's half is
// reproducible; the agent's half is labelled `source: "llm"` at every point of
// use so a reviewer always knows which is which.
```

[VERIFIED: internal/findings/findings.go:50-53]

```
const (
	SourceDeterministic Source = "deterministic"
	SourceLLM           Source = "llm"
)
```

`ToFindings` already stamps `Source: findings.SourceLLM`. Do not add a model client. Do not change `Apply` / `ToFindings` unless a compile requires it (it should not).

## Recommended Go Struct Shapes (D-01..D-11)

Use pointer fields so omitted JSON stays `nil` (v1 `omitempty` does **not** treat an empty struct as empty — only `false`, `0`, `nil`, and length-zero slice/map/string). [CITED: pkg.go.dev/encoding/json]

JSON tags are locked. Go names below are discretion.

```go
// Source: recommended for D-02 / D-04 / D-06 / D-09.
// JSON keys must be exactly: intent, pr, ticket, number, title, url, id,
// surfaces, interface, api, schema, line, moved.

type Intent struct {
	PR     *IntentPR     `json:"pr,omitempty"`
	Ticket *IntentTicket `json:"ticket,omitempty"`
}

type IntentPR struct {
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
}

type IntentTicket struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

type Surfaces struct {
	Interface *Surface `json:"interface,omitempty"`
	API       *Surface `json:"api,omitempty"`
	Schema    *Surface `json:"schema,omitempty"`
}

type Surface struct {
	Line  string `json:"line,omitempty"`
	Moved bool   `json:"moved"` // no omitempty — false is a real idle
}

type Review struct {
	Summary       string      `json:"summary"`
	Intent        *Intent     `json:"intent,omitempty"`
	Surfaces      *Surfaces   `json:"surfaces,omitempty"`
	Files         []FileNote  `json:"files,omitempty"`
	APIChanges    []Highlight `json:"apiChanges,omitempty"`
	SchemaChanges []Highlight `json:"schemaChanges,omitempty"`
	Findings      []Judgment  `json:"findings,omitempty"`
	Unknowns      []string    `json:"unknowns,omitempty"`
	Screenshots   []Shot      `json:"screenshots,omitempty"`
}
```

Do **not** share one link struct with both `number` and `id`. PR number is `int` to match `target.PullRequest.Number`. Ticket id is `string` (`REL-24`).

Do **not** use `map[string]Surface`. Sketch 003's JS uses `ui` / `db` internally; the ingest contract keys are `interface` / `api` / `schema`. A map would silently accept the wrong keys.

### Empty helpers (add next to `Merge`)

A field is empty when it would not carry briefing data:

- `Intent` empty: nil, or both PR and ticket empty.
- `IntentPR` empty: nil, or `Number == 0 && Title == "" && URL == ""`.
- `IntentTicket` empty: nil, or `ID == "" && Title == "" && URL == ""`.
- `Surfaces` empty: nil, or all three surfaces empty.
- `Surface` empty: nil, or `Line == "" && !Moved`.

`{line:"Did not move. No spec or handler in the packet.", moved:false}` is **not** empty. `{moved:true}` with no line is **not** empty. `{moved:false}` with no line **is** empty (keep previous). That is how "no v1 way to clear" stays true without inventing idle copy.

`Merge` additions (same shape as summary/files):

```go
if intentEmpty(out.Intent) {
	out.Intent = prev.Intent
}
if surfacesEmpty(out.Surfaces) {
	out.Surfaces = prev.Surfaces
}
```

Whole-object replace: `{intent:{pr:{number:2}}}` replaces the entire previous intent, including ticket. Same as a one-item `files` array replacing the walkthrough. Comment-loop ingest must **omit** the `intent` / `surfaces` keys, not send a partial object.

### `moved` discretion

Use an explicit `Moved bool` with **no** `omitempty`. Do not infer `moved` from a non-empty `line`. Idle tiles need a quiet line **and** `moved: false`.

### What Phase 1 does not write into these fields

- Do not copy `packet.Target.PR` into `Intent.PR`.
- Do not invent surface lines from `apiChanges` / `schemaChanges` / screenshots.
- Do not fetch ticket hosts.
- Do not fail when `intent.pr.number` disagrees with `packet.target.pr.number`.

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go | 1.26 (module) / 1.26.3 (this machine) | Language | Existing `go.mod`: `go 1.26` [VERIFIED: go.mod:3] |
| `encoding/json` | stdlib v1 | Parse + persist Review | Already used by `ParseReview` and `SaveSession`. Do not add a schema library. [CITED: pkg.go.dev/encoding/json] |

### Supporting

None. No new modules.

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Pointer `*Intent` / `*Surfaces` | Value types + `omitzero` | Value types marshal `"intent":{}` under v1 `omitempty` because empty structs are not empty. Pointers match omit-keep without a new tag dialect. |
| Struct surfaces | `map[string]Surface` | Map accepts `ui`/`db` from sketch JS. D-06 locks three keys. |
| Shared link struct | One type with `number` + `id` | PR and ticket shapes differ (`number` int vs `id` string). |
| `encoding/json/v2` | Newer defaults | Ingest already uses v1 `Unmarshal`. Migration is out of scope. |
| `Decoder.DisallowUnknownFields` | Fail on extra keys | Contradicts D-10. Extra `"goal"` must be ignored. |

**Installation:** none.

**Version verification:** `go version` → `go1.26.3 darwin/arm64`. `go.mod` → `go 1.26`.

## Package Legitimacy Audit

No new packages. Phase 1 extends `internal/packet` with stdlib JSON only.

| Package | Registry | Age | Downloads | Source Repo | Verdict | Disposition |
|---------|----------|-----|-----------|-------------|---------|-------------|
| — | — | — | — | — | — | No installs |

**Packages removed due to [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

## Architecture Patterns

### System Architecture Diagram

```
agent stdout (review JSON, optionally fenced)
        │
        ▼
redline ingest
        │
        ├─ packet.ParseReview(stdin)     // encoding/json; unknown keys ignored
        │         │
        │         ├─ type OK → *Review (summary, optional intent, optional surfaces, …)
        │         └─ type bad → error "review is not valid JSON"
        │
        ├─ run.LoadSession(.redline/)    // previous Review + Packet + Report
        ├─ Target.Matches(CLI flags)     // CLI --pr vs session target only
        ├─ packet.Apply(report, rev)     // findings/unknowns only; not intent
        ├─ packet.Merge(prev, next)      // omit/empty keep summary, files,
        │                                // highlights, shots, intent, surfaces
        └─ write()
              ├─ session.json            // Review including new fields
              ├─ packet.json             // unchanged Version=1
              ├─ findings.json
              ├─ report.md / report.html // existing render; no briefing page
              └─ stderr: Report: URL
```

Phase 2 will read `session.Review.Intent`, `session.Review.Surfaces`, `packet.Target.PR`, highlights, and screenshots. Phase 1 only has to persist the first two.

### Recommended Project Structure

No new packages or files. Touch:

```
internal/packet/ingest.go        # types + Merge helpers
internal/packet/ingest_test.go   # extend existing merge/parse tests
```

Optional, only if a compile forces it (it should not): `internal/run/session.go` already stores `*packet.Review`.

Do not touch: `internal/report/html.go`, `internal/report/markdown.go`, `internal/packet/packet.go` (`Version`), `internal/packet/build.go` (`DefaultGuidance`), `skills/redline/SKILL.md`, `cmd/redline/main.go` (unless a test helper needs a fixture).

### Pattern 1: Empty-keep merge

**What:** Copy previous whole-field value when the incoming field is omitted or empty.
**When to use:** Every narrative field that lives only on `Review` (summary, files, highlights, screenshots, unknowns, now intent and surfaces).
**Example:** see `Merge` quote above; add the two `intentEmpty` / `surfacesEmpty` branches beside the `Summary` check.

### Pattern 2: Fail-closed parse, ignore unknown keys

**What:** `json.Unmarshal` into typed structs.
**When to use:** All ingest JSON.
**Example:** existing `ParseReview`. Invalid `intent` (string) or `moved` (string) must fail. Extra `"goal"` must succeed and be ignored.

### Anti-Patterns to Avoid

- **Second ingest path or DTO:** grow `Review`. Do not add `Briefing` / `Goal`.
- **Deep-merge of `intent.pr` vs `intent.ticket`:** that is a new merge story. Whole-object replace, like `files`.
- **Inferring `moved` from `line`:** locked discretion is explicit `moved`.
- **Copying `packet.Target.PR` into the review:** Phase 2 fallback, not Phase 1 write.
- **`DisallowUnknownFields`:** breaks D-10 and ignores-`goal`.
- **Sketch JS keys `ui` / `db`:** contract keys are `interface` / `api` / `schema`.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| JSON parse | Custom scanner / regex | `encoding/json` via `ParseReview` | Fenced-block strip + `Unmarshal` already exist |
| Merge framework | Generic deep-merge | Two empty-keep branches in `Merge` | Comment-loop is whole-field, like files |
| Schema validation library | New module | Typed structs + `UnmarshalTypeError` | D-10: unknown keys ignored; bad types fail |
| PR / ticket fetch | `gh` / Jira HTTP | Store agent strings only | Ticket is agent-only; PR already on packet target |
| HTML briefing | Template / drawer work | Nothing this phase | Phase 2 |
| Skill prompt rewrite | Edit `skills/redline` or `DefaultGuidance` | Nothing this phase | Phase 3 / AGT-02 |

**Key insight:** The ingest loop already persists whatever `Review` can marshal. The expensive mistake is a merge rule that blanks fields, not missing infrastructure.

## Common Pitfalls

### Pitfall 1: Blanking intent/surfaces on comment-loop ingest
**What goes wrong:** Second ingest with only `findings` wipes PR/ticket/tiles.
**Why it happens:** `out := *next` copies nil/zero over previous values; `Merge` only special-cases listed fields.
**How to avoid:** Add `intent` and `surfaces` to the empty-keep list. Extend `TestMergeKeepsOmittedNarrative`.
**Warning signs:** Test fails when `next` has findings only.

### Pitfall 2: Partial object treated as full replace
**What goes wrong:** `{intent:{pr:{number:2}}}` drops a previously stored ticket.
**Why it happens:** Whole-object replace matches `files`.
**How to avoid:** Document it. Comment-loop must omit the key. Do not deep-merge unless discuss-phase reopens D-09.
**Warning signs:** A "update PR only" test that expects ticket to survive — that test would be the new merge story. Do not write it.

### Pitfall 3: Value-typed `Intent` + `omitempty`
**What goes wrong:** `session.json` writes `"intent":{}` for never-set intent; empty-struct `omitempty` does not omit structs in v1.
**Why it happens:** v1 empty is false/0/nil/length-zero only. [CITED: pkg.go.dev/encoding/json]
**How to avoid:** Pointer fields.

### Pitfall 4: `moved,omitempty`
**What goes wrong:** `moved: false` disappears on marshal; idle vs omitted becomes ambiguous in fixtures.
**Why it happens:** `false` is empty for `omitempty`.
**How to avoid:** `json:"moved"` with no `omitempty`.

### Pitfall 5: Bumping `packet.Version`
**What goes wrong:** Agents and packet readers treat version as a hard guard.
**Why it happens:** Review growth is mistaken for packet growth.
**How to avoid:** Leave `const Version = 1`. Review has no version.

### Pitfall 6: Adding `goal`
**What goes wrong:** Two headlines; HTML/markdown/Merge keep using `summary`.
**How to avoid:** Extra `"goal"` is ignored unknown. Headline stays `summary`.

### Pitfall 7: Failing ingest on PR mismatch
**What goes wrong:** Agent `intent.pr` ≠ `--pr` session target rejected.
**Why it happens:** Confusing CLI `Target.Matches` with agent overlay.
**How to avoid:** No comparison. D-03.

### Pitfall 8: Inventing surface lines
**What goes wrong:** Phase 1 fills idle copy from highlights/screenshots.
**Why it happens:** Sketch 003 idle strings look like product copy.
**How to avoid:** Store only what was sent. Phase 2 invents idle display.

### Pitfall 9: HTML / skill / model work in this phase
**What goes wrong:** Scope bleed into Phases 2–3 and AGT-02.
**How to avoid:** Plans touch `ingest.go` + `ingest_test.go` only.

## Code Examples

### Empty-keep for the new fields

```go
// Source: extend internal/packet/ingest.go Merge (same pattern as Summary/Files)
if intentEmpty(out.Intent) {
	out.Intent = prev.Intent
}
if surfacesEmpty(out.Surfaces) {
	out.Surfaces = prev.Surfaces
}
```

### Ingest JSON the contract must accept

```json
{
  "summary": "A reviewer can open the report in Cursor and see what each file does.",
  "intent": {
    "pr": {"number": 1, "title": "Serve the report over loopback", "url": "https://github.com/chrisophus/redline/pull/1"},
    "ticket": {"id": "REL-24", "title": "Serve the report over HTTP", "url": "https://example.invalid/REL-24"}
  },
  "surfaces": {
    "interface": {"line": "The HTML report is the product surface.", "moved": true},
    "api": {"line": "Did not move. No spec or handler in the packet.", "moved": false},
    "schema": {"line": "Did not move. Migrations pane skipped.", "moved": false}
  },
  "files": [{"path": "internal/report/html.go", "summary": "Renders the walkthrough."}],
  "apiChanges": [{"title": "unchanged list — still valid"}],
  "findings": [{"file": "a.go", "rule": "x", "message": "boom"}]
}
```

`apiChanges` / `schemaChanges` / `screenshots` remain legal. Surfaces do not replace them.

### Invalid input that must fail closed

```json
{"summary":"x","intent":"PR #1"}
{"summary":"x","surfaces":{"interface":{"moved":"yes"}}}
```

`ParseReview` must return an error wrapping `review is not valid JSON`.

### Unknown extra field that must succeed

```json
{"summary":"x","goal":"must be ignored"}
```

## Test Plan

Framework: Go `testing`. Command: `go test ./internal/packet/` (quick) and `go test ./...` (full). Makefile target: `make test` → `go test ./...`. [VERIFIED: Makefile:22-23]

### Extend, do not replace, the merge story

`TestMergeKeepsOmittedNarrative` today [VERIFIED: internal/packet/ingest_test.go:107-133]:

```
func TestMergeKeepsOmittedNarrative(t *testing.T) {
	prev := &Review{
		Summary:       "What this change does.",
		Files:         []FileNote{{Path: "a.go", Summary: "does a"}},
		APIChanges:    []Highlight{{Title: "moved"}},
		SchemaChanges: []Highlight{{Title: "added column"}},
		Screenshots:   []Shot{{Route: "/x", Path: "/tmp/x.png"}},
	}
	next := &Review{Findings: []Judgment{{File: "a.go", Message: "boom"}}}
```

**Add to `prev`:** a non-empty `Intent` (PR + ticket) and `Surfaces` (at least one tile with `line` + `moved`). Leave `next` as findings-only. Assert intent and surfaces survive, and existing summary/files/highlights/shots/findings assertions still hold.

**Extend `TestMergePrefersTheNewReviewWhereItSpeaks`:** when `next` supplies non-empty `intent` and `surfaces`, those values win.

Keep `TestMergeHandlesNils` as-is.

### New parse tests (same file, same story)

| Test | Input | Expect |
|------|-------|--------|
| `TestParseReviewIntentAndSurfaces` | JSON with `intent.pr`, `intent.ticket`, three surfaces | Fields populated; `moved:false` preserved |
| `TestParseReviewRejectsInvalidIntentType` | `"intent":"PR #1"` | error contains `review is not valid JSON` |
| `TestParseReviewRejectsInvalidMovedType` | `"moved":"yes"` | same error |
| `TestParseReviewIgnoresUnknownGoal` | `"goal":"…"` plus `summary` | success; `Summary` set; no `Goal` field exists |
| `TestMergeKeepsEmptyIntentAndSurfaces` | prev filled; next has `Intent:&Intent{}` and `Surfaces:&Surfaces{}` | previous kept |

Do not add an HTML assertion that the briefing page shows PR/ticket. `TestIngestAcceptsTheSessionsOwnTarget` already checks `summary` appears in `report.html`; leave it. Optional later: `SaveSession`/`LoadSession` round-trip — not required if `Review` fields are exported and existing session tests stay green.

Do not add a "partial intent keeps ticket" test. That would lock a deep-merge D-09 forbids.

Existing parse tests (`TestParseReviewToleratesFence`, `TestParseReviewFiles`, `TestParseReviewScreenshots`, `TestToFindingsStampsLLM`) must stay green.

## Risks

| Risk | Why it matters | Mitigation |
|------|----------------|------------|
| Breaking ingest | `ParseReview` is the only agent write path | Keep unknown-key ignore; only typed structs change; run `go test ./internal/packet/ ./cmd/redline/` |
| Blanking fields | Comment-loop omits narrative | Empty-keep + extend `TestMergeKeepsOmittedNarrative` |
| Packet version | Agents treat `version` as a guard | Do not edit `packet.Version` or `Build` |
| Whole-object wipe | Partial `intent`/`surfaces` drops siblings | Same as `files`; document; omit keys on follow-up ingest |
| Accidental HTML/skill scope | Phase 2–3 work lands in Phase 1 plans | Plans may not list `html.go`, `report.html.tmpl`, `skills/`, `DefaultGuidance` |
| `goal` field creep | Second headline | No Go field; unknown JSON ignored |
| PR mismatch fail | False ingest errors | No `intent.pr` vs `target.pr` check |
| `json/v2` or `DisallowUnknownFields` | Changes ignore/fail rules | Stay on v1 `Unmarshal` |

## What NOT To Do

- Do not edit `internal/report/html.go`, `markdown.go`, `assets/report.html.tmpl`, or sketches.
- Do not open a drawer, Fix queue, or coverage-banner redesign.
- Do not edit `skills/redline/SKILL.md` or `DefaultGuidance` (AGT-02 is Phase 3).
- Do not add a model client, HTTP call to Jira, or URL fetch of `intent.*.url`.
- Do not add `goal` on `Review`.
- Do not replace or remove `apiChanges` / `schemaChanges` / `screenshots`.
- Do not invent surface lines when `surfaces` is omitted.
- Do not bump `packet.Version`.
- Do not enable `DisallowUnknownFields`.
- Do not fail ingest when agent PR ≠ `--pr` packet target.
- Do not copy `packet.target.pr` into `Review.Intent` in Phase 1.
- Do not create a second ingest DTO or merge function.

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Infer briefing from leftovers (`summary` + highlights) | First-class `intent` + `surfaces` on ingest | This phase | Opening page (Phase 2) is not inferred |
| Comment-loop empty-keep for strings/slices only | Same rule for two optional objects | This phase | Pointers + empty helpers |
| encoding/json v1 `Unmarshal` | Keep v1 | Existing | Do not migrate to v2 |

**Deprecated/outdated:**

- Adding a second `goal` field: rejected in D-01.
- Reusing `apiChanges` titles as tiles: rejected in D-07.
- Bumping packet version because review JSON grew: rejected in D-10.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Whole-object replace for `intent`/`surfaces` (not per-key deep-merge) is what D-09 means by "like summary / files" | Merge | If the user wanted per-key keep, a follow-up ingest that sends only `intent.pr` would drop ticket — discuss-phase would need to reopen D-09 |
| A2 | Phase 1 success for INT-02/INT-03 is persist, not HTML display | Phase Requirements | Planner might add report.html work; CONTEXT forbids it |

No other `[ASSUMED]` claims. JSON behavior is cited from official docs; in-repo values are quoted from files read this session.

## Open Questions

1. **Should a later phase deep-merge `intent.pr` vs `intent.ticket`?**
   - What we know: D-09 says treat them like `files` (whole field).
   - What's unclear: comment-loop agents almost always omit the whole key, so the wipe case may never ship.
   - Recommendation: implement whole-object replace; do not plan a deep-merge.

2. **Should `DefaultGuidance.Output` mention `intent`/`surfaces` now?**
   - What we know: AGT-02 / skill text is Phase 3.
   - Recommendation: leave `packet.go` guidance alone.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | compile + `go test` | ✓ | 1.26.3 (`go.mod` 1.26) | — |
| `encoding/json` | parse/persist | ✓ | stdlib | — |
| `gh` CLI | `--pr` target resolve | not required this phase | — | Phase 1 does not fetch PRs |
| Jira / ticket host | — | n/a | — | Must not be called |

**Missing dependencies with no fallback:** none

**Missing dependencies with fallback:** none

Step 2.6 note: no new external tools. Graphify is disabled in `.planning/config.json`; no `graph.json`.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go `testing` (stdlib, module Go 1.26) |
| Config file | none — `go test` |
| Quick run command | `go test ./internal/packet/` |
| Full suite command | `go test ./...` (`make test`) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| AGT-01 | Omit `intent`/`surfaces` keeps previous | unit | `go test ./internal/packet/ -run TestMergeKeepsOmittedNarrative -count=1` | ✅ extend |
| AGT-01 | Non-empty `intent`/`surfaces` replace | unit | `go test ./internal/packet/ -run TestMergePrefersTheNewReviewWhereItSpeaks -count=1` | ✅ extend |
| AGT-01 | Empty `{}` keeps previous | unit | `go test ./internal/packet/ -run TestMergeKeepsEmptyIntentAndSurfaces -count=1` | ❌ Wave 0 |
| AGT-01 | Parse valid intent + surfaces | unit | `go test ./internal/packet/ -run TestParseReviewIntentAndSurfaces -count=1` | ❌ Wave 0 |
| AGT-03 | Ingest path has no model client | static / existing | `go test ./cmd/redline/ -count=1` (no new LLM wiring) | ✅ existing ingest tests |
| INT-01 | Headline remains `summary`; `goal` ignored | unit | `go test ./internal/packet/ -run TestParseReviewIgnoresUnknownGoal -count=1` | ❌ Wave 0 |
| INT-02 | `intent.pr` `{number,title,url}` accepted | unit | same as `TestParseReviewIntentAndSurfaces` | ❌ Wave 0 |
| INT-03 | `intent.ticket` `{id,title,url}` accepted | unit | same | ❌ Wave 0 |
| INT-04 | Missing PR/ticket is not an error | unit | existing fence/files parse tests + new parse of summary-only JSON | ✅ / extend |
| — | Invalid types fail closed | unit | `go test ./internal/packet/ -run 'TestParseReviewRejectsInvalid' -count=1` | ❌ Wave 0 |
| — | Existing files/shots/findings still work | unit | `go test ./internal/packet/ -count=1` | ✅ |

### Sampling Rate

- **Per task commit:** `go test ./internal/packet/`
- **Per wave merge:** `go test ./...`
- **Phase gate:** `go test ./...` green before `/gsd-verify-work`

### Wave 0 Gaps

- [ ] Extend `TestMergeKeepsOmittedNarrative` with `Intent` + `Surfaces` on `prev`
- [ ] Extend `TestMergePrefersTheNewReviewWhereItSpeaks` for replacement
- [ ] `TestParseReviewIntentAndSurfaces`
- [ ] `TestParseReviewRejectsInvalidIntentType` / `TestParseReviewRejectsInvalidMovedType`
- [ ] `TestParseReviewIgnoresUnknownGoal`
- [ ] `TestMergeKeepsEmptyIntentAndSurfaces`
- [x] Framework install: none — `go test` already works

## Security Domain

`workflow.security_enforcement` is enabled. Phase 1 accepts untrusted JSON on stdin.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | Local CLI; no auth |
| V3 Session Management | no | Session is a local file, not a user session |
| V4 Access Control | no | Single-user local tool |
| V5 Input Validation | yes | Typed `encoding/json` unmarshal; fail on type mismatch; do not fetch URLs from `intent` |
| V6 Cryptography | no | No new crypto |

### Known Threat Patterns for Go JSON ingest

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Type confusion (`intent` as string) | Tampering | `UnmarshalTypeError` → `ParseReview` error |
| Extra fields / prototype-style keys | Tampering | Unknown keys ignored; no `map[string]any` for surfaces |
| SSRF via `intent.*.url` | Information disclosure | Store only; do not GET the URL |
| XSS via summary/line in HTML | Tampering | Out of scope — do not change HTML this phase; Phase 2 must keep `html/template` |
| Prompt/model injection via ingest | Elevation | No model calls (AGT-03) |

## Project Constraints (from .cursor/rules/ and CLAUDE.md)

No `.cursor/rules/` directory. From `.claude/CLAUDE.md`:

- Tech stack: Go CLI, `embed` for `report.html`.
- Determinism: agent text is always `source: "llm"`.
- Agent boundary: packet in, review JSON out. Binary never calls a model.
- Local only: `.redline/` is gitignored.
- Mutual exclusion: only one of `--pr`, `--branch`, `--commit`, `--range`.
- `.planning/` stays out of product commits unless explicitly asked.
- Do not make direct repo product edits outside a GSD workflow unless asked to bypass.

## Sources

### Primary (HIGH confidence)

- `internal/packet/ingest.go` — `Review`, `Merge`, `ParseReview`, `Highlight`
- `internal/packet/ingest_test.go` — `TestMergeKeepsOmittedNarrative`
- `internal/packet/packet.go` — `Version = 1`, agent-boundary comment
- `internal/run/session.go` — session `Review` persist
- `internal/target/target.go` — `PullRequest`
- `internal/report/html.go` — `Summary` fallback; API/schema highlights
- `cmd/redline/main.go` — `cmdIngest` merge loop
- `internal/findings/findings.go` — `SourceLLM = "llm"`
- `.planning/phases/01-briefing-contract/01-CONTEXT.md` — D-01..D-11
- `redline-design.md` — agent boundary; binary never calls a model

### Secondary (MEDIUM confidence)

- [pkg.go.dev/encoding/json](https://pkg.go.dev/encoding/json) — `omitempty` empty set, unknown keys ignored, `UnmarshalTypeError`, `DisallowUnknownFields`

### Tertiary (LOW confidence)

- None material. Sketch 003 JS keys (`ui`/`db`) are design leftovers, not the ingest contract.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — brownfield stdlib; `go.mod` and `go version` read this session
- Architecture: HIGH — ingest/merge/session path read end-to-end
- Pitfalls: HIGH — empty-keep and whole-object replace are demonstrated by existing tests and D-09

**Research date:** 2026-08-29
**Valid until:** 2026-09-28 (stable in-repo contract; stdlib JSON rules are not fast-moving)

---

## RESEARCH COMPLETE

**Phase:** 1 - Briefing contract
**Confidence:** HIGH

### Key Findings

- Grow `packet.Review` with pointer `intent` (`pr` `{number,title,url}`, `ticket` `{id,title,url}`) and `surfaces` (`interface`/`api`/`schema` × `{line,moved}`). No `goal` field. No packet version bump.
- `Merge` must empty-keep those two fields the same way as `summary`/`files` (whole-object replace). Extend `TestMergeKeepsOmittedNarrative`; do not invent a deep-merge.
- Persist is free: `session.Review` already JSON-round-trips. `cmdIngest` already calls `Merge`. Do not copy `packet.target.pr`, fetch Jira, fail on PR mismatch, or invent idle lines.
- Invalid types fail via existing `json.Unmarshal`; unknown keys (including `"goal"`) stay ignored. Do not use `DisallowUnknownFields` or `encoding/json/v2`.
- Out of scope: HTML/drawer, Fix queue, skill/`DefaultGuidance` text, model calls.

### File Created

`.planning/phases/01-briefing-contract/01-RESEARCH.md`

### Confidence Assessment

| Area | Level | Reason |
|------|-------|--------|
| Standard Stack | HIGH | No new deps; Go 1.26 / stdlib JSON |
| Architecture | HIGH | Ingest → session → Merge path read |
| Pitfalls | HIGH | Comment-loop blanking is the existing merge test's reason to exist |

### Open Questions

- Whole-object vs per-key merge: locked as whole-object (like `files`). Reopen only if comment-loop starts sending partial `intent`.

### Ready for Planning

Research complete. Planner can now create PLAN.md files.
