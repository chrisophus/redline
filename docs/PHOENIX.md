# Phoenix architecture — rebuilding Redline from ashes

This document is the extraction: the essential contracts, decisions, and
invariants from which this repository could be reproduced. It assumes the
rebuilder is a competent Go engineer (or an agent) with git on PATH. Read it
together with the two files that are themselves part of the phoenix set and
should be preserved verbatim:

- `redline-design.md` — the why and the roadmap: the three jobs (postable
  verified reviews, evidence panes, the review page), what the tool keeps
  and what it dropped, the check backlog. Everything below implements a
  slice of it.
- `skills/redline/SKILL.md` — the agent-facing half of the product. The
  binary and the skill are one system; the skill's JSON shapes are contracts
  the binary must honour.

Everything else can be regenerated from this file plus those two.

## 1. Identity

Redline reviews a change by **observing it at two revisions and diffing the
observations** — evidence from execution, not inference from source. It is a
single static Go binary (`module github.com/ccason/redline`, go 1.26, zero
third-party dependencies — stdlib only, shelling out to `git` and `gh`) plus
a skill directory. It makes no model calls, has zero network egress of its
own (`--pr` fetches via `gh`, read-only), never posts to GitHub, and never
gates anything.

Implemented state ("rung 1"): migration hygiene checks 1–2 (no database, no
container) plus the full review loop — packet → agent → ingest → HTML
report. Checks 3–16 are specified in the design doc but not built, and the
tool must say so loudly (see §6, unknowns for unbuilt panes).

## 2. Repository shape

```
cmd/redline/           main.go (CLI), reviewers.go (--with adapters)
internal/
  target/    resolve what is being reviewed (worktree|commit|range|branch|pr)
  gitx/      thin git layer: resolve, blobs, diffs, fetch, worktree cache
  run/       wire panes to a repo, produce one Report + Packet; session.json
  pane/      the Pane interface and shared result types
  pane/migrations/  checks 1 & 2 (the only pane built)
  findings/  the wire schema (doctor-compatible) + fingerprints
  packet/    the agent contract: Packet out, Review in, merge/apply
  instructions/  discover repo review-rule files (Copilot/Claude/Cursor/…)
  graph/     read a graphify graph.json, derive threads through the change
  reviewer/  external reviewer adapters (claude, cursor) for --with
  report/    markdown + self-contained HTML report, loopback server, shots
skills/redline/SKILL.md
Makefile               build/test/vet; install = SYMLINK binary + skill
redline-design.md
```

Makefile decisions worth keeping: `make install` symlinks both the binary
(into `~/.local/bin`) and the skill dir (into `~/.claude/skills`) so they
can never drift — a binary older than the skill driving it produces packets
missing fields the skill expects and reads as a tool bug. `make
install-repo REPO=…` *copies* SKILL.md into a repo's `.claude/skills/`
(a copy by necessity: it must survive without the checkout).

## 3. CLI surface

```
redline run     observe the change and report (deterministic entry point)
redline review  same run, then emit packet.json on stdout for the agent
redline ingest  read agent review JSON on stdin, merge, write report, open
redline open    serve and open the last report
redline serve   serve .redline over loopback http (blocks; --stop ends it)

target (all subcommands; pass at most one):
  (default)        working tree, uncommitted work included
  --commit REF     that commit against its first parent
  --range A..B     tree diff of the span (B defaults to HEAD; ... accepted)
  --branch REF     a branch tip against --base
  --pr N|URL       a GitHub PR (metadata via `gh pr view --json …`)

flags: --base REF, --upstream REF (collision check target), --migrations DIR,
  --format report|json, --out DIR (default .redline), --open / --no-open,
  --port N (default 8765), --with claude|cursor|none, --stop
```

Exit-code philosophy: serving and browser-opening are best effort. Once the
report is on disk, a taken port must not exit non-zero — that tells the
driving agent a completed review failed. Only `ErrNoReport` (nothing to
serve) is fatal, matched with `errors.Is`, never inferred from an empty URL.
When nothing serves, print `Report: <out>/report.html` (the disk path).

## 4. Target resolution (internal/target)

- All non-worktree targets are materialized as **detached git worktrees** so
  the user's checkout is never moved. Worktrees are a content-addressed
  cache under `~/.redline/worktrees/<sha256(git-common-dir)[:8]>/<sha>`
  (override root via `REDLINE_WORKTREE_ROOT`); reusable iff the dir has a
  `.git` and its `--git-common-dir` resolves (abs+symlink-clean) to ours;
  a foreign leftover is dropped and recreated; losing a creation race to
  another process reuses the winner's result. Runs never clean up: the
  revision is immutable.
- Commit target: base defaults to first parent; a root commit errors asking
  for `--base`. Range target: base defaults to A. PR target: fetch
  `refs/pull/N/head` into `refs/redline/pr/N`; base defaults to
  `origin/<baseRefName>` (fetched if absent). PR metadata via
  `gh pr view REF --json number,title,body,author,url,baseRefName,headRefName,files,isDraft`
  — Redline never handles a token itself; requires `gh` on PATH.
- `Target{Kind, Dir, Head, Base, Label, PR*}` is serialized into the packet.
  `Target.Matches(Options)` compares **what the user typed** (kind + label;
  for PRs, number or any URL ending in it), not resolved SHAs — the caller
  is `ingest`, which has no repo open, and the mistake worth catching is
  `review --pr 123` then `ingest --pr 456`. Kind mismatch, label mismatch,
  and PR-number mismatch all error with "this run reviewed X, not Y".

## 5. Git layer (internal/gitx) — the load-bearing details

- The working tree is a revision: `WorktreeBlobs()` = `git ls-files -z
  --cached --others --exclude-standard`, then batch `git hash-object
  --stdin-paths` (dropping paths deleted from disk first). Redline is
  pre-push, so **untracked non-ignored files are in scope**.
- `ChangedPaths(rev)` = set-diff of `Blobs(rev)` (ls-tree) vs WorktreeBlobs,
  sorted.
- `MergeBase(rev)` falls back to rev itself when there is no common ancestor
  — the diff is then the whole history, which is the honest answer.
- `DiffPath(rev, path)`: `git diff -U3 rev -- path`; when empty and the file
  exists on disk (untracked), fall back to `git diff --no-index /dev/null
  path` (exit 1 is the success case). Best effort — evidence capture must
  never fail a run.
- `Stat` uses `diff --numstat -z`; for worktree targets, untracked files are
  absent from numstat, so count +/− lines from the same DiffPath diff the
  packet shows, keeping the report's numbers consistent.
- Log format `%H%x1f%an%x1f%s%x1f%b%x1e` → `Commit{SHA,Author,Subject,Body}`.

## 6. The run (internal/run + internal/pane)

Pane interface (per-check-family plugin, closed set, compiled in):

```go
type Pane interface {
    Name() string
    Scope(changed []string) []string   // subset examined, NOT a bare bool —
                                       // the union of scopes is the coverage number
    Observe(rev Revision) (Observation, error)  // expensive, side-effectful
    Diff(before, after Observation) (Result, error) // pure
}
// Revision{Name:"base",Rev:sha} vs Worktree = Revision{Name:"worktree"}
// Result{Render{Title,Summary,Lines}, Findings, Confirmations, Unknowns,
//        Evidence map[obsID]Artifact{Kind:"diff|sql|text", Content}}
```

Run algorithm: resolve target → open repo at target.Dir (for PR/branch this
is the detached worktree, so panes never know what kind of target they see)
→ resolve base ref (explicit, else fallbacks, else `origin/main`,
`origin/master`, `main`, `master`; an explicit ref that doesn't resolve is
still returned so the real failure is reported) → merge-base → ChangedPaths
→ **exclude Redline's own output dir** (`.redline` plus `--out`, repo-
relative; without this a second run reviews the first run's report) → for
each pane: empty scope ⇒ SubstrateSkipped; Observe(base)+Observe(worktree)+
Diff; an error ⇒ SubstrateFailed **plus** an Unknown ("pane applied to this
change but did not run") — a dark sensor never reads as a pass.

After panes: compute `Coverage{ChangedFiles, ExaminedFiles, Unexamined[]}`;
append Unknowns for **unbuilt check families** by classifying unexamined
files into areas (api → "checks 10-13 … are not built", ui → "checks 15-16
…", tests → "check 14 …"); if ExaminedFiles == 0 and anything changed,
append the "none of the N changed file(s) fall under any pane" unknown —
"the empty findings list below is not evidence of correctness". Then
`Report.Finalize()`, sort findings, and **always** build the Packet (the
HTML report renders its drill-in sections from it) and attach graph threads.

Session persistence: the whole Result (report, packet, renders, evidence,
last review) is written to `<out>/session.json` so `ingest` merges into the
packet the agent judged instead of re-observing a tree that may have moved.
Missing/corrupt session on ingest is an error ("run `redline review`
first"), never a silent re-observe.

## 7. Findings schema (internal/findings)

An independent reimplementation of gorefactor/doctor's JSON shape
(`SchemaVersion = 1`) — shared contract, not shared internals — extended
additively:

```go
Finding{ File, Line, Rule, Substrate, Category, Severity, Message,
         New, Fingerprint, FixCmd, Context,            // doctor's fields
         Anchor{Kind,ID}, Evidence []obsID, Expected, Observed,
         Source "deterministic"|"llm", Reviewer, Confidence }  // additive
Report{ SchemaVersion, BaseRef, BaseSHA, Scope, Findings, Substrates,
        NewCount map[Severity]int, Coverage, Confirmations, Unknowns }
SubstrateStatus{ Name, State ran|skipped|failed, Detail, Gating(false) }
Confirmation{ Substrate, Rule, Message, Anchor }  // deliverable, not noise
Unknown{ Substrate, Message, Reason }             // report section 3
```

- Categories: `schema`, `contract`, `cover`, `ui`, `review` (doctor's "api"
  means Go exported-API changes — do not overload it). Default severity:
  schema/contract ⇒ error, rest ⇒ warning.
- Every pane is diff-based by construction ⇒ `New` is always true, no
  baseline machinery.
- **Fingerprint** = `loc + "\x00" + rule + "\x00" + NormalizeMessage(msg)`
  where loc is File, or `Anchor.Kind + ":" + Anchor.ID` when there is no
  file. NormalizeMessage collapses each maximal ASCII-digit run to `#`
  (doctor's scheme). The fingerprint is the join key for human comment
  state, so anchor identity must be stable across runs.
- `Finalize()` stamps defaults (severity from category, source
  deterministic), New=true, fingerprints, NewCount; nil slices become empty
  so JSON emits `[]`. `Dedupe()` drops later duplicates by fingerprint
  (ingest can run twice; judgments must not stack) and recomputes NewCount.
  `Sort`: severity rank, then substrate, rule, file — stable output.
- `Source` is load-bearing: a reviewer must see at a glance which findings
  reproduce. Only packet ingest and reviewer adapters may set "llm".

## 8. The migrations pane (checks 1 & 2)

Substrate `redline/sql`. Convention: golang-migrate files matching
`(?:^|/)(\d+)_[^/]*\.(up|down)\.sql$`; `--migrations DIR` restricts to one
directory. Observation = `Set{Rev, Files map[path]blobSHA}`, obs ID
`migrations@<rev|worktree>`; the base-side observation also records
`UpstreamVersions map[version]path` from `--upstream` (default: the base
ref) — a missing upstream ref is recorded as `UpstreamErr`, never swallowed.

Diff (pure):

- **Check 1 `migration-modified-after-merge` / `migration-deleted-after-merge`**
  (error, schema, anchor kind "migration" id version): any path in the base
  set that is missing or has a different blob in the head set.
  golang-migrate records applied *versions*, not contents, so an edit
  silently diverges every already-migrated database from every fresh one.
  For an edit, capture the actual SQL diff as evidence artifact
  `diff:<path>` — the reviewer's question is "what changed", and blob hashes
  don't answer it. FixCmd: "revert the edit and add a new migration".
- **Check 2 `migration-version-collision`** (error): a version added by this
  change (in head's versions, not base's) that already exists upstream under
  a *different* path (same path = same migration; check 1 owns drift).
  FixCmd: renumber above the highest upstream version. If upstream was
  unreadable and versions were added ⇒ Unknown ("N new versions could not be
  checked"), never a pass. Each non-colliding version ⇒ Confirmation
  `migration-version-unique`.
- **State the denominator**: all base migrations byte-identical ⇒ one
  Confirmation `migration-immutable` ("all N …"); otherwise an Unknown
  ("M of N … were changed; see findings") — "1 file unchanged" beside a
  modified sibling reads as reassurance; "1 of 2" reads as what it is.
- Render (report section 1): `+ version V — paths` for additions,
  `~ path (existed at base)` for touched pre-existing files.

## 9. The packet (Redline → agent)

`Packet` (Version 1): Target, BaseSHA, Commits, Stats, `Files []FileChange
{Path, Status added|modified|deleted, Added, Removed, Diff, Language,
Areas}`, `Instructions []instructions.File`, `Deterministic []Finding` (the
agent must not re-derive or contradict these), `Threads`, `Guidance`,
`UITouched bool`.

- Per-file diff capped at 60 000 bytes ("... diff truncated; read the file
  directly") so a generated file cannot crowd out the hand-written change.
- Status comes from presence in base tree vs worktree blobs (an empty
  untracked diff used to read as "modified"); diff-header sniffing is the
  fallback.
- `Areas(path)` classifier (a file may have several): sql = `.sql` or a
  migrations dir; api = basename contains openapi/swagger, or yaml/json
  under `/api/`, or `.proto`; ui = tsx/jsx/vue/svelte/css/scss/html, ts/js
  under web|ui|frontend|components, `.tmpl` containing "html"; tests =
  `_test.go`, `.test.`, `.spec.`, `/tests/`; else `code`. UITouched = any
  file has area "ui".
- `Guidance` (focus/avoid/output/sources) ships **in the binary**, versioned
  with Redline, not in a prompt. Its content is tuned against the failure
  mode that kills AI review — volume — and mandates: a one-sentence summary
  for every file, no style nits, no restating hunks, nothing already in
  `deterministic`, confidence over suppression or bluster, UI walk when
  uiTouched (agent-browser, diff-scoped, never presented as checks 15–16).

Instruction discovery (internal/instructions), in precedence order:
`.github/copilot-instructions.md`, `.github/instructions/*.instructions.md`
(YAML frontmatter `applyTo` glob honoured, comma-separated, `**` supported
beyond filepath.Match; a bare directory prefix governs its subtree),
`AGENTS.md`, `CLAUDE.md`, `.cursorrules`, `CONTRIBUTING.md`, plus
`.cursor/rules/**/*.mdc`. Each file capped at 16 000 bytes. Files are
filtered to those governing at least one changed path.

Graph threads (internal/graph): read-only over an existing graphify
`graph.json` (searched at `graphify-out/`, repo root, `.planning/graphs/`,
`.planning/graphs/*/`) — Redline never builds a graph. Nodes match changed
files on path-segment boundaries; each touched node's outgoing edges become
`Thread{From,To,Nodes,Explanation}`, capped at 12.

## 10. The review (agent → Redline)

`ingest` reads JSON from stdin (tolerating one fenced ``` block, since
that is how a model most often emits it):

```json
{ "summary": "...", "files": [{"path","summary"}],
  "apiChanges"/"schemaChanges": [{"title","detail","file","breaking"}],
  "findings": [{"file","line","rule","category","severity","message",
                "context","fix","confidence","instruction"}],
  "unknowns": ["..."],
  "screenshots": [{"route","path","before","caption"}] }
```

- `ToFindings`: every judgment becomes a Finding with Substrate
  `redline/review`, Source `llm` (nothing else may set that), category
  default `review`, severity default `warning`; `instruction` is appended to
  context as "House rule: …"; non-high confidence appended as "The agent
  reported X confidence in this finding."
- `Apply`: append LLM findings + unknowns (unknowns deduped by
  substrate+message, reason "reported by the reviewing agent"), then
  Finalize, Dedupe, Sort.
- `Merge(prev, next)`: a follow-up ingest carrying only changed findings
  must not blank the summary/walkthrough/highlights/screenshots/unknowns the
  report leads with — omitted fields keep their previous value; a supplied
  field wins. Carried-forward screenshots are pruned when their temp files
  are gone (`PruneMissingShots`, stderr note) — but a *newly supplied*
  missing file is an error worth hearing about.
- Ingest never re-observes, and errors if target flags name a different
  target than the session recorded (§4).

## 11. External reviewers (--with, internal/reviewer)

An adapter is configuration, not code: `{name, command[], prompt, timeout}`
with placeholders `{prompt}`, `{out}`, `{target}`, `{schema}`. Built-ins:

- `claude`: `claude -p {prompt} --output-format json --allowedTools "Bash
  Read Grep Glob Write"`, prompt `/code-review {target}` + contract —
  invoke the vendor's own review logic, never a Redline-authored generic
  prompt.
- `cursor`: `cursor-agent -p {prompt} --output-format text -f`, prose
  review prompt + contract (no headless Bugbot entry point exists yet).

`<out>/reviewers.json` (`{"reviewers": {name: adapter}}`) overrides
built-ins by name; Timeout marshals as a duration string ("10m"), accepting
bare nanoseconds too. The contract tells the tool to **write findings to a
file** (`<out>/reviewer-<name>.json`, schema: findings[] of file/line/
severity/title/body/confidence) because stdout carries the tool's own chat.
Run semantics: stale output file removed first (a silent failure cannot
pass off last run's findings); run in the *reviewed* tree (the detached
worktree for non-worktree targets, named by SHA not label); timeout via
context; non-zero exit with a valid file is fine (reviewers exit non-zero
for findings-found), no file is an error leading with the exec error (the
common case is "not installed"); findings with empty titles dropped,
severity/confidence lowercased with invalid ⇒ warning/medium. A failed
reviewer is recorded as substrate `reviewer:<name>` failed + an Unknown —
never raised as a run error, never readable as "found nothing". Findings
are **appended, not merged** across reviewers (guessing two wordings are one
defect deletes a finding silently; exact dupes collapse via fingerprint) and
carry Reviewer + Confidence + Source llm.

## 12. Reports (internal/report)

Written on every run to `<out>/`: `findings.json`, `packet.json`,
`session.json`, `report.md`, `report.html`, `evidence/<sanitized-obs-id>`
files, `evidence/ui/NN-slug-{before,after}.ext` for screenshots.
`comments.json` is reserved for reviewer state and never written by runs.

**Four sections, in order** (both renderers): 1 what changed (pane renders,
in the domain of the change), 2 what was checked and held (confirmations,
collapsed, never discarded), 3 what could not be determined (dark
substrates loudly, unknowns, unexamined files, skipped panes), 4 findings
(each with location/expected/observed/source/fix, inline evidence ≤ 4 000
bytes else a path reference). A **banner** leads when coverage is hollow:
nothing changed / "Redline examined none of this change … the empty findings
list says nothing" (with a variant naming external reviewers that did run:
their reading is not reproducible and silence is not a pass) / "N pane(s)
applied and did not run". The **file walkthrough** (path, status, +/−,
agent's sentence, findings count + worst severity per file) renders even
when no pane ran — that is when a reviewer most needs it.

HTML specifics: one self-contained file, no network, no JS/CSS deps,
hand-rolled diff markup; light/dark via prefers-color-scheme. Diff lines
carry `data-file`/`data-line`/`data-side` parsed from `@@ -a,b +c,d @@`
hunk headers (added/context lines number in the new file, deletions in the
old; header detection only before the first `@@` of a file so a deleted
line starting `---` isn't swallowed) — clicking a line attaches a comment;
**Copy comments for the agent** exports them as JSON naming `path:line`.
Per-file "viewed" checkboxes and comments persist in localStorage keyed by
`Identity` = `short(baseSHA):short(head)` (+ a packet-content hash for
worktree reviews, so two dirty trees against one base don't share
comments) — not by the report path, which every run overwrites. Drill-in
area sections (Schema & migrations / API contract / Interface / Code /
Tests) render each file's diff from the packet. Screenshots inline as
base64 data URIs (`template.URL`, else html/template emits `#ZgotmplZ`)
within budgets (3 MB per shot, 12 MB total; past that link to the on-disk
copies, which the server serves), and the section renders "did not run"
when empty.

Serving: `report.html` must be viewed over loopback HTTP (editor webviews
reject file://). Default port 8765, scanning up to +20. A server identifies
itself at `/.redline-id` with `{pid,port,dir}` — reuse only a server that
answers **for this evidence directory** (a bare 200 proves nothing; it may
be another repo's report). `serve.json` in the out dir records the running
server; `--stop` signals it only after the identity check (a crash-leftover
PID may have been reused by an innocent process). Detached spawn re-execs
`os.Executable() serve --out … --port …` (setsid + /dev/null on unix), with
a guard refusing to re-exec a `*.test` binary — that is a fork bomb. Idle
timeout 30 min so a review doesn't serve diffs until reboot. Browser open:
`open` / `xdg-open` / `rundll32 url.dll,FileProtocolHandler`.

## 13. Behaviors the test suite pins (≈2 700 lines of _test.go)

Reproduce tests asserting at least: check 1 fires on edit and delete but
not on untouched files; check 2 fires only on true collisions (same
path upstream is not one) and degrades to an unknown without upstream;
fingerprint stability and digit normalization; Finalize/Dedupe/Sort
semantics; target matching errors (`ingest --pr` mismatch), range parsing
(`A..B`, `A...B`, missing ends); worktree cache reuse and foreign-dir
recreation; untracked files present in ChangedPaths/DiffPath/Stat;
own-output exclusion; unbuilt-pane and zero-coverage unknowns; packet diff
truncation, status-by-blob-presence, area classification, instruction
applyTo matching incl. `**`; Review merge keeping prior summary/files/
screenshots; fenced-JSON parsing; reviewer adapter placeholder expansion,
stale-file removal, no-file error, normalization, timeout; hunk-header line
numbering (incl. a deleted line beginning `---`); shot materialization
budgets and pruning; serve identity check, port scanning, stop safety, and
the test-binary re-exec guard (cmd/redline's TestMain is the second lock).

## 14. The invariants that must survive any rewrite

1. Deterministic evidence, non-deterministic commentary — never the
   reverse; every LLM-derived finding is labelled at the point of use.
2. A pane (or reviewer) that did not run must never read as a pass; absence
   of a pane must never look like an empty findings list. Coverage is
   stated; the flattering misreading is pre-empted with a banner.
3. Confirmations are the deliverable, with denominators.
4. The agent never chooses what runs; applicability comes from the diff.
5. Redline calls no model, posts nothing to GitHub, gates nothing.
6. Reviewing must never disturb the user's checkout or handle credentials.
7. Findings and human comment state stay in separate files, joined by
   stable fingerprints.
8. One static binary, stdlib only, evidence on disk, report readable with
   no server and no network.
