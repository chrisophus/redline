# Redline, reviewed from outside

Date: 2026-09-10. Revision reviewed: `f37508c` (the head of PR #25, on top
of `main` at `0fa26b9`). The review is of the whole tool as it stands, not of
that pull request, and it is not bound by the current design or its stated
constraints.

Method: read every design document, plan and skill; read the source of every
package; ran build, vet, gofmt, the test suite with coverage, and the offline
eval; built the binary and ran it on this repository's own latest commit and
on a twelve-commit range; priced a review with `--dry-run`; screenshotted the
HTML report; checked CI history and open pull requests on GitHub. Five
parallel deep reads covered the observe pipeline, the model producer, the
output layer, the eval, and the product surface. Findings marked "confirmed"
were reproduced against the built binary or a scratch repository; the rest
were established by reading the code at the lines cited.

## Verdict

Redline is a well-built instrument with a good idea at its centre, and a
model-review layer on top of it that has grown faster than anything
measures it.

The good idea is the framing: a gate compresses what it saw into one bit, and
a reviewer needs the detail behind the bit, plus an explicit statement of
what was not checked. The code takes that seriously. A pane that cannot run
is loud, absence is named, findings say which producer wrote them, and the
report is a single offline file with a loop back to the author's agent that
no other tool has. That half works and is the part worth showing people.

The other half is `redline review`. It is roughly forty percent of the code,
most of the last two days of commits, and the subject of most of the README.
Its central hypothesis, that deterministic findings handed to a model as
priors produce a better review than the model alone, has not yet been
measured with a real key. Field use twice produced reviews whose every
finding the reader dismissed. The response was three more suppression
mechanisms and a second model call, and the meter that would say whether
any of it worked (`docs/plans/review-feedback.md`) is still marked not done.
The verify pass that is meant to fix the dismissals fails open in three
places, so it does not yet deliver the property it was built for.

Most of what follows is fixable in small pieces. The observe layer's holes
are one-fixture tests away. The verify pass needs a handful of fail-closed
rules. The eval needs labels that can say no. None of it is architectural.

## Scorecard

| Area | State | One line |
|---|---|---|
| Build, vet, gofmt | clean | Go 1.26, two direct dependencies |
| Tests | 30 of 31 packages pass | one architecture test fails on this branch; 74% statement coverage |
| Observe pipeline | good with holes | invariant mostly holds; symlinks, renames, `$ref` do not |
| Model review | unproven | verify pass fails open in three places; second call costs more than the docs say |
| Eval | not credible yet | keyword matcher a null review passes at 22% recall; one clean fixture |
| Posting to GitHub | careful, with gaps | body unbounded; markers trusted from any author |
| Report | good | offline, escaped correctly, readable |
| Report server | needs a Host check | readable by other sites through DNS rebinding |
| Docs | a design journal | 668-line README, no quickstart, no language matrix |
| Release | none yet | no license, no tag; matters only if others are meant to use it |

## The findings that matter most

Ranked by how much they change what a user of the tool gets.

### 1. The verify pass fails open

The design sentence is "a review is posted only with the evidence it rests
on." In code:

- A candidate the ruling did not mention is posted unchecked.
  `internal/findings/question.go:161` returns true for an empty verdict and
  `rule.go:290` skips candidates absent from the rulings. The schema cannot
  require one ruling per candidate and the `finding` field is a free string,
  so a truncated or lazy ruling response silently posts what it skipped.
- A model-issued `already-raised` is accepted on its word. `rule.go:394`
  adds deterministic matches and never rejects a model verdict that has no
  prior thread behind it.
- The quoted evidence for `withdrawn` and `justified` is stored, never
  checked against the material it claims to quote (`rule.go:439`). The
  burden of proof the prompt describes is prose only.

The eval's own plan says a ruling stage tuned on precision will learn to
withdraw. The code lets it withdraw on nothing, and lets it keep what it
never looked at. This is the feature the last day of work was for, and it
is the one that decides what reaches an author.

### 2. Several confirmations assert checks that did not happen

This is the failure the design exists to prevent, and it occurs inside the
observe layer:

- Lint: a change touching only `.eslintignore` in a repository with no
  linter at all produces substrate `redline/lint: ran` and the confirmation
  "no new linter finding against the base revision ()" (confirmed).
  `delta.go:351` confirms when `introduced == 0` and `allBaseRan` of zero
  runs is true.
- OpenAPI: `openapi.go:159` reads only inline schemas and parameters. A
  spec written with `$ref`, which is most of them, is invisible, and
  `api-no-breaking-change` is then confirmed at `openapi.go:284`.
- NOT NULL: `notnull.go:457` captures the clause with `[^,]*`, so
  `numeric(10,2) NOT NULL` is missed and `CHECK (x IS NOT NULL)` is
  reported as a column named `CONSTRAINT`.

### 3. The git layer does not model symlinks, renames, binaries or failure

`gitx/git.go:126` stats through symlinks and hashes the target, while
`ls-tree` stores the link text, so every tracked symlink reads as changed on
every run and a symlink to a directory reads as deleted (confirmed).
Submodules read as deleted by the same path. There is no rename detection,
so a moved file is deleted plus added, every pre-existing lint issue in it
becomes "introduced", every existing suppression is re-reported, and every
line counts as added; the README's "moved code does not read as new
violations" holds only inside one file. A file's `Head` is carried whole
into the session on a newline count alone (`change.go:210`), so a multi-MB
binary with few newlines lands in `session.json` and the prompt. `Repo.File`
turns every git failure into "absent" (`git.go:509`), which the OpenAPI,
lint-config and migration panes read as "added". `MergeBase` silently falls
back to the ref itself on a shallow clone (`git.go:69`). `ls-tree --format`
needs git 2.36, undocumented.

### 4. Posting has four holes the README says are closed

- The review body is not bounded as a whole. `post.go:362` caps the
  overview and file table at 20k each and leaves the "findings not shown
  inline" list unbounded, with no check against GitHub's 65,536-character
  limit. A lint-heavy PR rejects the whole review, comments and gate marker
  included.
- A reviewer comment's `side` is parsed (`findings/review.go:249`) and
  dropped; every comment posts `RIGHT` (`cmd/redline/post.go:223`). A
  comment on a removed line lands on the wrong code.
- `hedged()` scans `Context` (`post.go:282`), and for a verified finding
  `Context` is the ruling's quoted repository evidence. A kept finding is
  withheld as hedged because the code it quotes contains "nit:" or "probably
  fine".
- Hidden markers are read from any author (`post.go:503`). Anyone who can
  comment on a public PR can paste a `redline:fp:` marker and suppress a
  finding for that head.

The fingerprint (`fingerprint.go:37`) is file, rule and digit-normalised
message, so two findings in one file with the same rule whose messages
differ only by a number collide: one verdict lands on both, one posted
marker suppresses all, and `relatedFindings` resolves to the first.

### 5. The report server and the scout can be read from outside the tree

`internal/report/serve.go:56` binds `127.0.0.1` and validates nothing about
the `Host` header, serves the whole `.redline` directory with directory
listing on, and stays up for thirty minutes of idle. `session.json` carries
every diff, every envelope's file contents, and the PR body. A page on
another origin that rebinds to `127.0.0.1` becomes same-origin and reads it.
The fix is a `Host` check and serving only `report.html` and `evidence/`.

In the scout, `record.go:326` checks paths lexically for `..` and absolute
prefixes but never resolves symlinks, so a symlink added by the change makes
`read_lines` and `grepTree` read outside the tree (`.git/config` included),
and the bytes land in the envelope, the session and the prompt.
Model-chosen strings are passed positionally to `gorefactor` and `graphify`
with no `--` separator (`tools.go:227,251`).

### 6. Cost is under-recorded and the ceiling bounds only the first call

Stage two's spend is printed to stderr and never reaches the ledger
(`cmd/redline/review.go:279`). The ruling's duration is not added. The
ruling's output tokens are folded into the stage-one entry, so
`MedianOutput` is inflated and feeds back into the next estimate. The
`--max-cost` tripwire is evaluated once against stage one
(`review.go:383`); the ruling computes its own ceiling and never compares
it, and it appends the entire scout envelope to the request with no budget
(`rule.go:233`). `--samples` checks one sample's ceiling and sends N. The
plan's "a re-review where everything has been answered pays nothing" is
false: `Verify` calls the answerer at `rule.go:342` before computing what is
already settled at `rule.go:364`, so the scout runs its full loop to look up
questions whose findings are then discarded.

Two wiring bugs in the same area: `--api openai --base-url X` hands the
OpenAI gateway URL and key to the scout's Anthropic client
(`cmd/redline/review.go:284`), so verification always fails on that path;
and stage two is gated on the `ANTHROPIC_API_KEY` environment variable
alone (`review.go:267`) while the README says an `ant auth login` profile
is enough. Nothing in the code loads such a profile.

### 7. The eval cannot yet say no

Matching is case-insensitive substring on `any_of` and `all_of` word lists,
with an optional file and never a line (`internal/eval/eval.go:351`). A
single contentless sentence posted on each labelled file scores 7 of 32
labels (confirmed against the real scorer). One comment can satisfy many
labels. An expectation with no words matches everything. The `reject` list
that was added so that false positives count has zero entries in any
annotation, so precision is unmeasured. There is one clean fixture, so the
clean rate can only read 0 or 100. Twenty-eight of thirty-two labels come
from two commits in one of the author's other repositories.

The paid sweep has not yet run with a working key, records no prompt hash
or commit, and has no checked-in result anyone could rerun. A real bug sits
in the newest scorer: `delivered.go:64` pairs `CommentFindings()[i]` with
`rev.Comments[i]`, but `CommentFindings` skips empty bodies, so the indices
drift and a real defect can be reported as suppressed. The Copilot
comparison in the README is a curated ten fixtures against a self-reported
figure of a different quantity; it is fine as an aspiration and should not
be read as a measurement.

### 8. The second model call costs more than the documents say

`README.md:289`, `docs/plans/two-stage-review.md:293` and comments in
`rule.go` say the ruling call is served from prompt cache at a fraction of
the input rate, and the plan's "under twice today's review" estimate rests
on it. The code sets no cache breakpoint anywhere (`anthropic.go:49`;
`cost.go:200` says so), so the second call pays the full input rate for
the whole first prompt again. Even with a breakpoint added, `rule.go:406`
appends the ruling instruction to the system block, so the prefix changes
and the user turn could never be a hit. The fix is a breakpoint plus moving
the ruling instruction to the tail of the user turn. Until then the cost
story for verified reviews is roughly double what the ledger and the docs
report, on top of the under-recording in finding 6.

### 9. The tool is Go-only in practice, and the documents do not say so

Coverage reads Go's profile format only (`cover.go:107`; lcov and istanbul
names appear in `generated.go` and nothing reads them). Migrations accept
golang-migrate naming only. Built-in linters are golangci-lint, eslint and
gorefactor. The only context provider that resolves symbols is gorefactor.
Mutation is gomutants only. `run.go:544,588,717` hard-code `.go`.
JavaScript enters through eslint; every other language is "unexamined".
None of this is wrong for a tool built for one Go team. A README that says
so up front would save the next reader an hour.

### 10. CI has been red on this branch for a day

`internal/boundary` fails because `cmd/redline/review.go:15` imports
`internal/scout` for the verify pass, and the test's allowlist only names
`cmd/redline-scout`. The product invariant the test protects, that `run`
never calls a model, still holds; `review` is the caller and it is allowed
to. The fix is a line in the allowlist and a sentence in the design doc, or
running the answering scout as a subprocess. It is listed here because the
boundary step runs first in CI, so the race tests and golangci-lint have
not run on any of the 19 commits on PR #25.

## By layer

### Observe pipeline

The invariant that a pane cannot pass silently is enforced at the right
seam: `runPane` failure becomes a failed substrate and an unknown
(`run.go:231`), a missing or failing linter at head fails the pane, a base
failure degrades with a stated unknown, a broken `.redline.yml` fails
visibly. The tests are named for behaviours and mostly test them. The fake
`golangci-lint` on PATH is a good device.

Below that seam it leaks, in the places listed under findings 2 and 3, and
in a few more: display truncation of a diff at 60,000 bytes
(`change.go:91`) feeds `cover.AddedLines` at `run.go:549`, so added lines
past the cut drop out of the coverage and mutation denominators while the
lint pane, which fetches a fresh diff, sees them; `cover.blocksFor`
(`cover.go:379`) says "longest suffix" and returns the first match from map
iteration, so a repository with two `main.go` files can report a different
diff-coverage number on two runs of the same input; `filterIntroduced`
(`delta.go:387`) reduces the delta to added-line linting for warning and
info, so a change that breaks an unchanged line is dropped; cached
worktrees are reused without a cleanliness check and without a lock
(`git.go:216`), so a linter cache or a harness step's output becomes an
added file the next time that SHA is a head.

Performance: `WorktreeBlobs`, which stats and hashes the entire tree, runs
five to seven times per run, and `Blobs(base)` three to four; `delta.go:410`
spawns one `git diff` per introduced issue; `change.Build` spawns two git
processes per changed file; linters run `./...` twice, unscoped; panes run
sequentially. On a large monorepo this is tens of seconds before any
linter starts. Memoizing the tree listings per run is a small change.

Design: the Observe/Diff pane shape fits migrations, OpenAPI and parity. For
suppressions, config and testdelta, `Observe` is a no-op and `Diff` does
all the reading. `Scope` mutates pane state and is called twice. The
`Evidence` map is nearly unused. `.redline.yml` has two loaders. There are
three diff-line walkers in the tree (`git.go:412`, `testdelta.go:498`,
`cover.walkAdded`) and one of them handles a removed line beginning with
`--` correctly.

### Model review producer

What is right: one call over a frozen session, so a fixture replays; the
scout records locations and the program reads bytes, so a model's memory of
a function can never pose as source; absences are named at every level;
cost is measured into a ledger; the typed question per finding is a real
mechanism that turns "I am unsure" into a dispatchable lookup and gives two
samples an identity. The envelope contract is clean and the budgeting is
deterministic. The OpenAI wire path and the refusal and truncation handling
are careful.

What is wrong is under findings 1, 6 and 8. Beyond those:

The prompt keeps three overlapping channels for uncertainty: "do not guess,
say nothing" (`prompt.go:48`), "report a finding you are unsure of at low
confidence rather than withhold it" (`prompt.go:114`), and `none` as the
question kind for speculation (`prompt.go:139`). The plan said the second
would be reworded when the third arrived. It was not, so the plan's own
diagnosis, a model "told to guess with a label on", still applies. The
`confidence` field is now nearly vestigial, overwritten by the ruling and
by `none`, yet the model is still asked to fill it.

The output schema is built from a Go map (`schema.go:135`), so
`encoding/json` sorts the keys and the model is asked for `comments` before
`overview`. Under structured output the model generates in schema order.
Overview-first, which the prompt's framing wants, was lost by accident.

`already-raised` is used both for in-review duplicates (`rule.go:141`) and
for threads the PR already heard; the report will label an in-review
duplicate as PR history. The `already-raised` key is question kind plus
free-text subject with no file (`question.go:90`), so an answered thread on
one file suppresses the same subject on another; `Thread.Answered` counts a
thumbs-up reaction as an answer (`feedback.go:373`), so agreeing with a
correct finding makes its next rewording disappear. The GraphQL fetch takes
`first:100` threads with no pagination. The houserules fragment says
guideline blocks are "binding on the review" and the scout's fragment for
the same role says they are "repository content rather than instructions";
both reach one system block. `changeShape` (`prompt.go:824`) treats any line
starting with `---` as a file header, so a removed SQL comment disappears
from "removed by this change" and shifts the line numbering after it.

`openai.go:36` uses `http.DefaultClient` with no timeout and no retry, under
`context.Background()`; a stalled proxy stream hangs the command
indefinitely. There are three hand-rolled Anthropic loops (one-shot, explore
on the beta API, scout). Explore mode is documented as superseded and kept
"until the measurement says", with no measurement recorded. Unknown envelope
roles (`guideline`, `neighbor`) rank 99 in `budget.go:62` and are dropped
before `history`, which contradicts the "floor" comments in
`houserules.go:93` and `scout/record.go:36`.

`internal/review` is 3,434 lines of non-test code doing assembly, two wire
protocols, sampling, exploring, verifying, pricing and a ledger.
`normPath` exists five times across packages and "is this a test file" is
defined three ways beside `change.IsTest`. `review.Question` and
`scout.Question` are the same struct copied field by field because the
scout imports the review package for pricing; a small `pricing` package
would break the cycle.

Tests in this package mostly assert `strings.Contains(prompt, phrase)`.
There is no golden prompt for any fixture, so a rewording of any rule
outside the pinned phrases passes CI. Nothing tests a ruling that omits a
candidate, rules `already-raised` with no prior, or quotes evidence that is
not in the material, because none of those cases is handled.

### Output layer

The HTML is escaped correctly everywhere: every template action goes
through `html/template`, the only `template.HTML` values are built from
already-escaped text, and the JavaScript builds DOM with `textContent`. The
page is fully self-contained, works from `file://`, has a complete dark
theme, and is responsive. The `gh` boundary passes nothing untrusted as an
argument; the review JSON goes over stdin. Idempotency per head SHA and
fingerprint is tested. The gate fails closed on a pane that applied and did
not run. `post_test.go` exercises the real predicates and is the best test
file in the repository.

Gaps beyond finding 4: line anchoring is validated against the current head's
diff while the review is anchored to the session's head
(`cmd/redline/post.go:52,223`), so after a push the non-profile path can
still send a line GitHub rejects; the verdict headline and the evidence
table count findings the same body then says were withheld
(`post.go:283,480`); only the last evidence artifact per finding renders in
HTML (`html.go:239`); `Suggestion` renders nowhere. Human-written
`review.json` verdicts are relabelled `source: llm`. The severity filter
hides cards inside the closed low-confidence fold. Diff lines are click-only
spans with no keyboard path. The 934-line template carries about 380 lines
of untested inline JavaScript.

CLI, all small: one flat `FlagSet` serves every subcommand, so `redline
post --samples 3` is accepted silently; `--format` is not validated;
`redline run -h` exits 1; `serve.go:317` tells the user to run `redline
review` first when it means `run`; `schemaVersion` is 1 and has not moved
as fields were added to `findings.json`; `gc --older-than` compares
creation time, not last use.

A first run on a fresh checkout of this repository exits 1 with no report,
because `.redline.yml` requires a coverage profile and none exists
(confirmed). The user has to know about `--prepare` or
`--allow-missing-coverage`. On a twelve-commit range the report shows four
identical "code changed but no test in that package did" cards, and a tile
reading "42/46 files examined" while the lint pane, the one that would have
examined them, did not run. Only the test-delta pane counted them.

### Eval

Covered under finding 7. What is good: the design commentary inside
`internal/eval` is honest and the ideas are sound (frozen sessions, union
versus per-sample rate, delivered versus written, known-gap labels, NaN
cost propagation). `TestScoreCountsACorrelationOnlyWhenItReferences` is the
right kind of test. The nil-rules labels are grounded in reproductions.

What would make it able to say no: precision labels with real entries; a
matcher a null review cannot pass; a held-out set; fixtures from
repositories the author did not write, with ground truth from the merged
fix; a clean-fixture share near the field's three in ten; provenance on
every result (prompt hash, commit, fixture-set hash, model, k); a golden
prompt snapshot in CI.

### Product and documents

There are three products in one repository: the instrument (panes, report,
`findings.json`), the model reviewer, and the PR bot with a merge-gate
profile. The positioning line, "gates say pass or fail, Redline shows what
they saw", fits the first exactly and the README spends about 250 of its
668 lines on the second.

Against the field: lint delta and new-code-only findings are what reviewdog,
golangci-lint's `--new-from-rev` and SonarQube's new-code model already do;
diff coverage with line annotations is diff-cover and Codecov; every
mechanism in the model reviewer is borrowed from Copilot, CodeRabbit,
Bugbot, Greptile or Graphite, and `two-stage-review.md:94` says so in a
table. What is differentiated: the local pre-push report with a
copy-for-the-agent loop; `findings.json` as priors a model is told not to
restate; mutation survivors on changed lines; suppression triage with a
verdict vocabulary; the `ran`, `skipped`, `failed`, `not-applicable` states
and the "what could not be determined" section; the frozen-session eval and
the cost ledger; the typed question with a commit-free marker; the migration
hygiene checks. The bet is that the combination beats the incumbents, and
the bet is unmeasured.

The README is a design journal with a README's filename. It has no
quickstart, no screenshot, no requirements list (git 2.36, `gh`, Go 1.26,
golangci-lint v2, optionally gorefactor, gomutants, graphify), and no
supported-languages table. The setup skill is the best document in the
repository and the only onboarding path, and it requires Claude Code or
Cursor to execute; most of its section 2 should be the README's
supported-tools table.

Minor and worth a tidy-up pass rather than a finding: the Status table says
`review` is "one model call" while the same file documents a scout loop and
a second ruling call; whole-file limit is documented as four hundred lines
and coded as 500; fixture counts are eight in two documents and ten on
disk; the Layout section omits eight packages; the dogfood report describes
a `packet` package and an `ingest` command that were later cut; the report
opens with a gap banner for a feature marked "not started". Internal
references (MCT, `make db-dsns`, a Marketplace review bot's markers, MKT
tickets, `.compound-engineering/`) leak into artifacts that `make install`
puts on every repository on a machine; worth scrubbing before anyone else
installs it.

Project health: 50 commits in about 30 hours, half agent-authored, one
author, Go 1.26 required, no tag yet. CI itself is good: gofmt, vet, the
boundary as its own step, `-race`, golangci-lint v2, goreleaser check and
snapshot, advisory bench and diff-scoped mutation.

## What I would do, in order

1. Make the ruling fail closed. `finding` becomes an enum of candidate ids;
   a candidate with no ruling after a completed pass is `unverifiable`;
   `withdrawn` and `justified` require evidence found verbatim in the
   material; a model `already-raised` requires a prior thread or a
   same-question sibling. Compute what is already settled before calling
   the answerer and filter its questions. Drop reactions from `Answered`.
   Add stage-two and ruling spend to the ledger as their own fields and
   apply the tripwire to the second call.

2. Close the observe-layer holes that are one-fixture tests away: symlinks
   and submodules in `hashObjects`, the lint confirmation with zero runs,
   truncated diffs feeding the coverage denominator, `blocksFor` map
   iteration, `numeric(p,s) NOT NULL`, `$ref` in OpenAPI (or state that the
   pane reads inline schemas only), binary detection, `Repo.File` error
   versus absent, and `MergeBase` on a shallow clone as an unknown. Memoize
   the tree listings.

3. Close the posting holes: bound the whole body against 65,536, honour
   `side`, scan `Message` and not `Context` for hedges, filter markers by
   the posting login, validate line anchors against the session head, and
   derive the headline from what is actually posted. Add a `Host` check to
   the report server and serve two paths, not the directory. Resolve
   symlinks in every `inTree`.

4. Build the meter before more machinery. Implement `review-feedback.md`
   steps 1 to 3 (record what was posted, read the outcomes, `redline
   stats`), run the paid sweep once with a real key, and put the acted-on
   and disputed rates somewhere visible. Freeze `--mode explore`,
   `--samples` and the Graphify provider until a number says one earns its
   keep. If after a month of real pull requests the review's acted-on rate
   is not measurably better than Copilot's on the same pull requests, cut
   `redline review` back to `review.json` ingest and let Claude Code or
   Copilot be the reviewer, which is the conclusion `redline-design.md:61`
   reached once already.

5. Fix the eval so it can say no. Add `all_of` anchors or question-identity
   matching, a test that a null review scores zero, five real reject
   entries, two more clean fixtures, and provenance on every sweep result.
   Fix `delivered.go:64`.

6. Set a cache breakpoint and move the ruling instruction to the tail of
   the user turn, or delete the sentences that say caching is on. Fix the
   `--api openai` scout wiring. Give the OpenAI client a timeout.

7. Fix CI: add `cmd/redline` to the boundary allowlist with a sentence in
   the design doc saying why, or run the answering scout as a subprocess.
   Then let the race tests and golangci-lint run on PR #25.

8. When others are meant to use it: a LICENSE, a `v0.1.0` tag (goreleaser
   is already wired), a README of about eighty lines with a screenshot and
   a supported-tools table, and the internal references scrubbed. Make a
   first run on a repository with no profile produce a report that says the
   profile is missing, rather than exit 1 with nothing.

9. Then the roadmap items that widen the user base: lcov ingest, a
   coverage-delta finding, a `redline doctor` that onboards without an
   agent, and a `findings.json` schema version that moves.

## What to stop doing

Phase 4's runtime panes (a Postgres container, browser capture, a Jira
header, a check run) are large infrastructure for a project with one user
so far; delete the Interface placeholder from the report now and defer the
rest until someone asks. The per-fact-type decision vocabulary has no usage
evidence behind the three rulings that exist. Explore mode and `--samples`
are kept for a measurement nobody has scheduled.

## What is worth keeping unchanged

The framing. The absence-is-loud rule and the four substrate states. The
frozen session as the whole input to the review, so it replays. The scout's
rule that it records locations and the program reads bytes. The cost
ledger and the worst-case tripwire. The report's copy-for-the-agent loop.
The commit messages, which record why a rule exists better than most
projects' documentation does. And the instinct, visible throughout, to
write down where the plan was wrong rather than quietly absorb it. The
instrument half of this tool is better than what most teams have.
