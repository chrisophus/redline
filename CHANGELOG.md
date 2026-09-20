# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This file starts at 0.10.0. Earlier entries are drawn from the annotated git
tags, which remain the record for them: `git tag -l --format='%(contents)' vX.Y.Z`.
Releases whose tag carries only a subject line are listed as that subject.

## [Unreleased]

### Added
- **The postmortem records what the lookups asked and what the refused calls
  were.** `Looked` and `Rejected` were counts: a saved artifact said a pass
  checked sixteen things and had thirteen calls refused, and nothing about
  which. On PR #1462 that left the mistake readable only off a terminal that
  was no longer there. `Result` now carries `Lookups` (tool, arguments, bytes
  back, and whether the answer was "nothing found" - which is an answer) and
  `Refusals` (tool, reason, arguments, and how many times the same reason
  recurred, so one mistake repeated every turn is one entry). Both render in
  `redline postmortem`. Arguments are cut at 200 characters and distinct
  refusal reasons at 20, because the trace wants the shape of a mistake rather
  than a second copy of the conversation.

### Changed
- **Every lookup cap is raised: `read_lines` 200 to 600 lines, `grep` 60 to 200
  matches, `list_docs` 80 to 500 documents, `symbol_context` and
  `line_history` 16 KiB to 64 KiB.** The caps were set against a worry about
  size rather than against the window. The model reads a million tokens, a
  review's ceiling is a quarter of that, and the question a cap answers is not
  whether the text is a lot but whether it is cheaper than the turn it costs
  to ask again. It is not close: on a large review one extra turn re-reads the
  whole cached prefix for about five cents, while the extra text is written to
  cache once and read back at a tenth of the input rate, so six hundred lines
  of a file cost about a cent and a half and five hundred document lines about
  two. A cap tight enough to force a second call was the expensive choice.

  The two byte caps are 32 KiB, not 64. Three of these lookups count lines,
  matches or documents and two count bytes, so the judgement has to be made in
  tokens, and the ratio is the trap: prose runs near four characters per token
  and `envelope` measures Redline's payloads at 2.33, because they are code,
  diffs and JSON. At four, 64 KiB reads as 16k tokens and looks level with the
  rest; at the real ratio it is 28k, twice `read_lines` and four times `grep`.
  A test now prices all five against each other and fails when any is more
  than three times another.
- **`list_docs` takes a `path` filter, and a cut listing says where the rest
  are.** Every lookup that cuts gives advice the caller can act on, and this
  one said "grep for a word a document would use" - which cannot be taken,
  because the reason to list documents is not yet knowing which one to ask
  for. On a repository with more documents than the bound, the answer now
  names the directories holding the remainder with a count each, biggest
  first, and `path` lists one of them. A repository with 167 documents gets a
  map and one follow-up call instead of the first 80 paths alphabetically and
  a dead end. Both the `--look` tool and the scout's own take the filter.

### Added
- **`--look` gets three more lookups: `list_docs`, `symbol_context` and
  `line_history`.** The judging pass could search and read; now it can also
  ask what the team wrote down, ask gorefactor for a Go symbol's callers
  resolved through the type checker, and ask git why a span of lines is
  there. Each answers the question behind a whole class of dismissed finding:
  the construction is a decision somebody recorded, the caller the change
  breaks is a fact rather than a name match, and the guard that was removed
  was there for a reason the commit message states. A run offers the lookups
  its checkout can answer, so `symbol_context` is left off the catalogue
  where `gorefactor` is not on PATH: a tool the reviewer can see and cannot
  use costs it a turn to find that out. The catalogue is 2454 input tokens
  with no lookups, 3073 with `grep` and `read_lines` alone and 3888 with all
  five, and it sits in the cached prefix, so only a run's first call pays
  full rate for the difference.
- **`ruling` on a finding in `findings.json`.** The verifying pass's verdict
  rides on the finding itself, for `source: llm` only and empty when no pass
  ran. It reached `post` as a confidence demotion before, which made "the
  repository said no" and "the reviewer was unsure" the same field, so the two
  gates below could not be told apart.

### Changed
- **`symbol_context` and `line_history` bound what they put into the
  conversation.** Every other lookup counts its own unit: 60 matches, 200
  lines, 80 documents, 3 commits. These two hand back whatever a subprocess
  printed, and gorefactor's context for a widely-used symbol is every caller in
  the repository. Nothing recorded or truncated it on the way past, so it went
  into the next turn's input as it stood, priced against a budget that still
  had to pay for the turns after it. Cut at a line boundary at 16 KiB, saying
  it was cut and what to narrow. Found by `redline review` on its own PR #83.
- **The ledger and the postmortem record the effort the calls were made at.**
  Both read the `--effort` flag, which is empty on every run that takes the
  default, so the rows a default exists to describe were the rows that did not
  say what they ran at. `Result` now carries the resolved effort beside the
  resolved model, and both readers take it from there.
- **The judging call is handed the walkthrough the describing call wrote.** The
  two calls share one cached prompt and differed only in their tail, so the
  overview and the file lines the first call produced never reached the second
  one: it saw the same diff, a catalogue that still offers `set_overview` and
  `describe_file`, and one line saying it takes `add_comment`. On PR #1462 a
  findings pass spent a turn writing an overview and twelve file summaries that
  already existed, all thirteen calls refused, on a run that had four turns.
  Telling the pass a walkthrough exists has been tried twice and did not hold,
  both times asking it to believe in something it could not see. The
  walkthrough now rides in the tail, behind the cache breakpoint, sorted by
  path so two runs of one change build the same request. The file lines earn
  their tokens twice over: they are a reading of every shown file, including
  the ones a judging pass would skim, from a call told to describe and not to
  judge.
- **This repository's own coverage profile is `when: missing`, not
  `when: stale`.** Stale failed the whole run whenever `coverage.out` was older
  than the change, which on a branch under active work is most of the time, and
  the answer was always the same two flags. A review refused outright is worse
  than one whose coverage section is behind: that section feeds the line-level
  "added lines no test executes", which sharpens a finding rather than
  producing one, and the staleness still reaches the HTML report, the markdown
  report and the pull request body. A profile that is not there at all is still
  a failure, because then nothing measured and silence would read as a pass.
  This is one repository's configuration; the `when` field is unchanged and
  still takes `missing`, `stale` or `always`.
- **`--call-turns` defaults to 30, from 12.** 12 was the number for a pass that
  could only record. A pass that can look things up spends turns before it
  writes anything and spends them at the front: on this repository's PR #83, a
  judging pass with the lookups on used all twelve on `grep` and `read_lines`,
  was cut off mid-search, and filed no comments at all. The output budget still
  governs the money, so a turn with no allowance left ends the pass whatever
  this says.
- **`--effort` defaults to `medium` rather than to whatever the endpoint
  picks.** An unset effort meant the API's own default, which is `high` and is
  free to move under us: the same review on the same model could then cost and
  find different things across two SDK versions, and the ledger would record
  the change as noise. `medium` on measurement rather than on the general
  guidance: reviews of real changes on `claude-sonnet-5` come back good at it,
  and the levels above cost more per review without having shown they find
  more. `--effort` raises it for a change that warrants it, and the checking
  pass stays at `low`.
- **The `--max-cost` tripwire defaults to $3.00, from $2.00.** It is a
  tripwire and not a governor, so it belongs above what a review of an
  ordinary change costs rather than near it. At $2.00 an ordinary pull request
  on this repository tripped it, and a tripwire that fires on work the tool is
  meant to do teaches whoever hits it to pass `--max-cost` without reading the
  number, which is the one habit that makes it useless on the day it matters.
- **`--look` is on by default, and `--no-look` turns it off.** A reviewer that
  pulls the context it needs beats one working from a guess made in advance
  about what it would want, and a claim it can check while it writes is one
  that does not have to survive a pass whose main output is withholding. It
  costs turns: three runs on one pull request put the inline shape at 4.7x a
  plain review, which is price rather than doubt about the shape, and
  `--max-cost` is the control for price. `looked=N` on each run says what the
  lookups bought. `--no-look` writes the review from the material alone, for a
  run that has to cost what a plain one costs and for measuring against the
  shape without them. `--look` still parses and wins over `--no-look`, the
  precedence `--cache` uses.
- **A reviewer finding that says it is unsure posts, at warning and error.** A
  defect held back is lost and a wrong one costs the author a minute reading
  it, and at those two severities the second price is the smaller one. So a
  reviewer that suspects the change corrupts data or breaks a caller says so
  and the comment carries the doubt. An unsure `info` remark still folds: a
  hedge about nothing much is the comment that teaches a team to stop reading
  the review.
- **The ruling gate and the `none`-question gate apply at every severity.**
  Both used to run through the confidence field, so widening what doubt lets
  through would have let them through too. They are their own predicates now.
  A finding the verifying pass did not keep, and one whose author said nothing
  would settle it, stay off the pull request whatever their severity, and stay
  on the report with the reason.
- **The report folds exactly what `post` holds back.** An unsure warning is in
  the findings list with its confidence on it rather than behind the chevron,
  so a reader with the report and the pull request in front of them sees one
  review.
- **The judging prompt asks for what the reviewer is unsure of, and names
  warning and error as where that matters most.** The reason it gives no
  longer rests on a filter running behind it, since `--verify` is off by
  default.
- **The system prompt no longer says linters have run over the change.** That
  is `priorsSection`'s sentence, which names the tools that actually ran and
  says nothing when none did.
- **One skip list serves the scout's search and its document listing, and it
  covers the build directories of more than two languages.** `vendor` and
  `node_modules` were skipped while `.venv`, `__pycache__`, `target`, `.next`
  and `.terraform` were walked and searched, so the same class of directory
  was treated differently by language and a hit could point at a vendored copy
  of code that lives somewhere else. It is named directories rather than every
  dot directory, because `.github` holds the CI config and `.claude` holds the
  house rules and a claim about the repository is exactly the kind that has to
  check against those. `build`, `out` and `coverage` are deliberately not on
  the list: each names a real source directory often enough that skipping it
  would hide code from a search that then reported no match.

## [0.13.0] - 2026-09-20

### Added
- **`--cohort-context` scopes resolved context to each cohort's own files.**
  A split run used to send the whole change's context on every call,
  including the describing call, which never judges anything and read none
  of it. With this the describing call gets no context at all, and each
  cohort call gets only what belongs to files outside every *other*
  cohort's list, written fresh into its own tail rather than the shared
  prefix. Excluding another cohort's files rather than keeping only this
  cohort's own is deliberate: an expansion's file is where the resolved
  code lives, not the file the change is about — a caller's file is the
  calling function's own path — so keeping only this cohort's own files
  would drop most caller and type context outright. The room each call
  fits into is the fan-out's own bound divided into what is left of the
  ceiling, since `fanOut` runs every cohort at once against one budget.
  The tradeoff is real: this context no longer rides the cached prefix, so
  it is paid for on every cohort's call rather than read back from what an
  earlier one wrote (#81).

### Changed
- **`--look`'s `grep` and `read_lines` skip anything `.cursorindexingignore`
  excludes at the repository root.** `.gitignore` is not read for this: it
  says what should not be committed, not what an automated reader should
  skip, and generated code or vendored deps are routinely both gitignored
  and something a claim needs to check against. A `grep` that turns up no
  matches now says when a path in scope was excluded rather than reading
  as proof the code is not there, the same distinction `read_lines`
  already made by naming the excluded path outright (#81).

### Fixed
- **`Result.Looked` counts a `--look` pass's `grep`/`read_lines` calls, the
  way `Fetched` already counts `get_context`'s.** Until now the only record
  of what a `--look` pass actually searched or read was the model's own
  narration of it; the count reaches the summary line (`looked=N`) and the
  postmortem trace, and the per-call debug line, which special-cased
  `get_context`, now logs `grep` and `read_lines` input too (#81).

## [0.12.0] - 2026-09-19

### Added
- **`--look` gives the judging pass `grep` and `read_lines`.** A claim about code
  outside the diff was something the reviewer could only *name*, as a question
  for a later pass to answer and a third to rule on. With this it can check the
  claim while it is writing the finding. Off by default, and an experiment:
  nothing has measured what searching buys, and it spends turns — the one
  published run of this shape went past $20 a review on a 43-file change, for
  two confident false positives against one real bug nothing else found.
  The lookups are the scout's own, so a path climbing out of the tree under
  review is refused, an escaping symlink is skipped and a search is capped — one
  implementation rather than a second copy of the hardened one. Measured on this
  repository: the two tools cost about 600 input tokens on the catalogue, 91 on
  the whole request, since the catalogue rides the cached prefix.

### Changed
- **A `diff` question's claim is checked instead of taken on trust.** A question
  of kind `diff` says the material already in front of the reviewer settles the
  finding, so it needs no lookup, and nothing checked that. On PR #46 six of the
  nine findings the ruling could not settle were of this kind, against a review
  that delivered five of fifteen, so the claim taken on trust cost about two
  thirds of a checked review. The claim is now checked against what the prompt
  carried — through `Input.ShownFiles` and `Input.shownLines`, not a second copy
  of what "shown" means — and a finding resting on material the reviewer was
  never shown becomes a lookup. It keeps its kind, and `diff` now maps to the
  `enclosing` record role so the scout is not refused when it files what it
  found.

## [0.11.0] - 2026-09-17

### Removed
- **`--brief` and `--no-brief` are gone, and with them the short prompt.**
  Measured on 2026-09-14 it returned a placeholder in place of a review on 22 of
  168 calls where the long prompt did on none of 42, left about twice the
  unlabelled comments, and cost no more; recall never separated the two. It was
  also the last caller that turned thinking off, and it did so by sending
  `thinking: {type: "disabled"}`, which Fable rejects with a 400 at any effort —
  so `--brief --model claude-fable-5-1` was a run that could not start. Every
  call now asks for adaptive thinking with a summarized display, on every model
  and every stage, which also removes the last place two calls sharing a cached
  prefix could send different thinking modes. `cappedEffort` goes with it: it
  existed to bring `xhigh`/`max` down to `high` for Opus 5, which accepts
  disabled thinking only at `high` or below, and nothing disables thinking now.
  `REDLINE_EVAL_BRIEF` and `REDLINE_EVAL_NO_BRIEF` stop a sweep rather than being
  ignored, so a run that still sets one cannot be scored under the shipped shape
  and labelled as the arm under test.

### Added
- **`--reuse-synopsis` reuses `review.json`'s walkthrough instead of paying
  for a new describing call.** The judging call still gets the
  findings-alone contract a fresh walkthrough would earn it, at no
  describing-call cost. Above `--cohorts 1`, it also reuses the partition
  that call drew - `review.json` now carries a split review's cohorts
  alongside its overview and files - so the whole describing-and-partitioning
  stage is skipped and the run goes straight to the cohort calls. Combine
  with `--only-cohorts` to re-run one cohort alone at its full share of
  `--max-tokens`, not the fraction `--cohorts` divides it into. Refused when
  `review.json` is missing, has no walkthrough, was written against a
  different change, or (above `--cohorts 1`) has no partition to reuse.
  `Result.SynopsisReused` and the matching ledger field say which zero
  `SynopsisOutputTokens` means: nothing written, or nothing paid for.
- **`--verbose` is split from `--debug`.** One flag used to mean two things:
  what a person watching the run sees on stderr, and what the run leaves
  behind on disk for someone to read afterward. `--verbose`
  (`REDLINE_VERBOSE`) is now the stderr narration alone; `--debug`
  (`REDLINE_DEBUG`) is the `--out/debug/<run timestamp>/` capture alone,
  independent of it. A CI job wants `--debug` on (the artifact is the only
  record once the runner is gone) and `--verbose` off (its own log already
  has stdout/stderr).
- **`--defer-context` lets the reviewer pull the context it wants.** The
  context the providers resolved leaves the prompt. Each file's diff is
  followed by an index of its entries, matched to the file through the
  provider's scope, and any pass reads an entry with `get_context`. An
  experiment, off by default. On a fixture whose defect is only visible from
  an unchanged caller, the reviewer fetched the caller in its first reply and
  caught the defect, and reasoned between fetches; on fixtures that do not
  need the context it fetched nothing and the prompt was a fifth smaller.
- **A synthetic fixture pair that needs context beyond the diff.**
  `caller-breaks-on-new-nil` changes `FindUser` to return nil, and an
  unchanged caller dereferences the result; `clean-caller-already-checks-nil`
  is the same change with a caller that checks. No earlier fixture had a
  labelled defect whose evidence is in the offered context.
- **`--note` tells the reviewer what to look at.** `redline review --note
  "..."`, or `--note-file path`, adds a note from whoever asked for the review
  to the end of every judging call: which file worries them, what to look at
  first, a question to answer. The describing call and the ruling do not see
  it. The prompt says the note is not evidence, that a rule the repository
  committed outranks it, and that a finding resting on it says so. The note is
  saved in `review.json` and the postmortem trace and shown on the report. With
  no note the request is unchanged. Whether a note turns a known miss into a
  catch has not been measured yet.

### Changed
- **Each pass reads only its own instruction, and the tools say what they are
  for.** The shared prompt is now material only: the judging instruction goes
  to the passes that judge and the describing instruction to the passes that
  describe, after a describing pass that read the judging instruction
  reviewed the whole change in thinking it could not file. The judging
  instruction asks for every defect, including uncertain and low-severity
  ones, rather than setting a bar. Tool descriptions say what each call
  records and when to use it, and the calls block states facts rather than
  how to batch calls. The system block no longer says the material below is
  everything, or points at steps that do not exist.
- **Every review call is a turn loop of small tool calls.** A pass used to
  answer with one strict tool carrying the whole answer as one object. Now
  every call declares the same six non-strict tools (`set_overview`,
  `describe_file`, `add_cohort`, `add_comment`, `rule`, `done`), and a pass
  answers by calling them, as many per reply as it likes. Redline checks each
  call against its schema: a good one is recorded, a bad one is answered with
  exactly what is wrong and the model sends it again. The pass ends on `done`,
  and a `done` is refused while a call in the same reply was rejected or a
  describing pass has no overview. What a pass recorded is kept however it
  ends: a turn cap, the output budget, a cost cap or a broken turn stops it
  early and the report says which passes stopped before they were done.
  Progress is printed each turn. This removes the strict grammar limit, keeps
  the same tools on every call so every pass reads the cached prompt, and lets
  a failed describing call fall back to writing the whole review again rather
  than findings alone. Measured before the build on fifteen fixture reviews:
  2 of 202 calls rejected, both fixed on retry, where the same single-object
  forms without strict were unusable on 8 of 9 findings calls.
- **A pass ends as soon as its work is visibly complete.** A describing pass
  with the overview, a line for every file and, when asked, cohorts, and a
  ruling with a ruling for every finding, end on that reply without waiting
  for `done`. The model is also told a call is answered "Recorded." unless
  something is wrong, so it can end its last reply with `done` rather than
  wait. Each extra turn resends the whole conversation, and with thinking on
  that includes all the reasoning so far: on a 70k-token fixture two turns
  that only collected `done` cost about $0.29 of a $0.98 review.
- **A pass never costs more than it was priced at.** The tripwire still prices
  each pass as one request up front. As the loop runs, each turn's output cap
  is cut to what that price can still pay for after what the pass has spent,
  and a pass with no room left stops on the cost cap with what it has.
- **The OpenAI wire runs the same loop**, answering each tool call with a tool
  message.
- **Every review keeps the model's thinking.** Each pass's reasoning summary is
  saved in `postmortem.json` and printed by `redline postmortem`, labelled by
  pass, cohort or sample, on every run rather than only under `--debug`. There
  is a summary only when the call was allowed to think: `--thinking`, or a
  model that refuses a required tool call, which is now asked for the summary
  too.
- **One describing form, and a failed describing call keeps the cache.** The
  split is now a field on the describing form, not a separate form, so the
  unsplit review, a split and `--stepwise` send the same three tools: ruling,
  findings and the describing form. An unsplit call returns the field empty.
  The review form, which writes the walkthrough and the findings together,
  only travels with `--no-synopsis` and `--brief`. When the describing call
  fails, the next call no longer asks for the whole review under a different
  set of tools, which threw away the prefix the failed call had just paid to
  cache. It asks for findings alone under the same tools, and the review's
  overview says the walkthrough is missing and why. One live call confirmed the
  endpoint accepts the set with the split field on it.
- **`--cohorts N` is the split, and `--pipeline` is gone.** The flag decided
  two things at once, whether a separate call describes the change and
  whether the judging is one call or several, and a caller could not set them
  apart. `--cohorts 1`, the default, is the describing call and one judging
  call; above 1, the describing call also draws the partition and each cohort
  is judged in its own call. The default bound was 6 under `--pipeline staged`
  and is 1 now, so a split has to name its size. The stepwise conversation is
  `--stepwise`, refused beside `--cohorts` above 1. `--plan` and
  `--only-cohorts` are refused without a split. `--synopsis` beside `--brief`
  or `--stepwise` is accepted rather than refused: it names the default, and
  the shape that replaces it wins. No synonym is kept for `--pipeline`. The
  ledger still groups rows as `oneshot`, `staged` and `stepwise`, so earlier
  rows compare. The eval harness reads `REDLINE_EVAL_COHORTS` and
  `REDLINE_EVAL_STEPWISE` and stops on `REDLINE_EVAL_PIPELINE`.
- **The plan documents are one document.** Eight documents under `docs/plans`
  had grown past three thousand lines, most of it describing work that has
  since shipped, and several of their measurements were overturned by later
  runs (the prompt ladder, which turned out to be measuring a proxy that
  rewrote the system prompt). `docs/plans/roadmap.md` carries what is still
  open, with the evidence each item rests on and the numbers that still hold.
  What shipped is in this file, the README status table and the doc comments
  beside the code; the arguments are in `git log --follow -- docs/plans`.
- **`tool_choice` is never pinned, on either wire.** It used to be pinned
  whenever thinking was off, which was the only reason `--thinking` existed:
  a pinned call spends zero thinking tokens on Sonnet 5. Measured directly,
  the model thinks by default once the choice is not pinned at all, so the
  trade was backwards. Every call now asks for adaptive thinking with a
  summarized display (unless a call has thinking off for its own reason),
  so the reasoning it already pays for is visible instead of invisible.
- **`--plan` always defers context, whether or not `--defer-context` was
  also given.** `--plan` sends one call, ever, in its invocation, and no
  later call reads a fully-written context back from cache; writing it in
  full paid the cache-write premium on tokens nothing amortizes - a quarter
  of the call's own estimated cost on the PR this was measured against.
  Deferred, the same context costs a rounding error, and it is the same
  prompt shape a `--defer-context` follow-up run reads, which is usually
  what a `--plan` run is drawn to plan for.
- **`--debug`'s capture keeps the exact instructions sent with each call.**
  `callsBlock`'s prose - including the deferred-context directive ("Read the
  context you need with `get_context` before writing comments") when a pass
  has one - goes out as its own content block on the wire, but was never
  recorded. The captured request JSON now has an `"instructions"` field with
  the exact text, so a reader asking whether a pass was actually told to
  defer context can check the artifact instead of trusting source code.

### Removed
- **The Message Batches path.** A batched request is one turn and a review
  call is now a loop, and only the retired eval sweeps used it.
  `REDLINE_EVAL_BATCH` stops the eval harness with a message.
- **`--stepwise` is gone.** It ran the review as one conversation in two
  turns, describing the change from its diff before the rest of the packet
  arrived. Measured as a hillclimb arm on 2026-09-14 it was noisier and cost
  twice as much, and it was never made the default. The review's calls are
  moving to a turn loop of small tool calls, and stepwise's own conversation
  would have had to be rebuilt on top of that for a shape nobody uses.
  `REDLINE_EVAL_STEPWISE` stops the eval harness with a message. Ledger rows it
  wrote are still grouped apart by `--stats`.

### Fixed
- **A profiled post no longer throws away a review when the PR head moves.**
  With `require_head: true`, the default, a commit landing between the review
  step and the post step in one CI run ended as `session reviewed <old> but PR
  head is <new>` and exit 1, and the finished, paid review never posted. It
  now posts with a notice that it is of an older commit, keeps its finding
  markers, and withholds only the gate verdict marker, so a stale review still
  cannot satisfy a gate that wants one covering the current commit.
- **The stale-commit notice reaches the pull request.** It was prepended to
  the body after the payload was built, and the step that drops findings
  already posted renders the body again, so a real post lost the notice. Only
  `--dry-run` showed it.
- **A stalled stream now fails within two minutes instead of hanging
  forever.** A run that stalled hung for upwards of twenty minutes with no
  error and no heartbeat line, because the heartbeat only reports what a
  stream event told it and a stalled connection delivers none. A first
  attempt reset a timer on every `stream.Next()`, which a test written to
  prove a slow-but-alive stream survives it caught as wrong: the SDK's SSE
  decoder discards `ping` keep-alive frames before a caller ever sees them,
  so that approach would kill a call that is genuinely alive but saying
  nothing but ping for minutes. Moved below the decoder instead: streamed
  response bodies are wrapped in a reader that races each `Read` against the
  timeout, so ping frames count as activity because they are bytes, whatever
  the SSE layer does with them. A stalled read now fails with "no data
  received in 2m0s, not even a ping: the connection stalled, this is not the
  model reasoning" - reaching `--debug`, the captured response, and the
  pass's stopped reason like any other call error.
- **`--out/debug` gets its own subdirectory per run, named by when the run
  started.** The old flat layout numbered files from 1 on every invocation:
  two runs sharing a stage name overwrote each other's capture, and two runs
  of different shapes (a split review's `synopsis`/`findings` stages beside
  a one-shot's `review` stage) left half the directory stale and half fresh
  with no filename collision to reveal it.

## [0.10.1] - 2026-09-16

### Added
- **`--plan` stops a staged run after the describing call.** It draws the
  partition and pays for stage one, then sends no cohort call: Comments comes
  back empty because nothing judged the change, not because it was clean, so
  Summary and the report say plan-only rather than a finding count. The cost
  tripwire prices stage one alone under it rather than the full fan-out, which
  it was refusing at before this — a `--plan` run was priced as if it were
  about to send the cohort calls it never sends.
- **`--only-cohorts` judges a subset of a staged partition.** Each
  comma-separated selector is a 1-based index into the partition as printed,
  a substring of a cohort's name, or — only where no name matches — a
  substring of a file path inside one. The file-path fallback exists because
  the name is stage one's to word fresh on every run: two calls over the same
  change can describe it differently, so a selector kept from an earlier
  `--plan` may be chasing wording that has already moved, and a file path is
  the one identifier that does not. A selector matching nothing is refused
  rather than silently reviewing none of the change.

## [0.10.0] - 2026-09-15

### Changed
- **A review describes the change in its own call.** Across about 400 stored
  samples, a reply that skipped the walkthrough — a junk overview, no file
  lines, findings only — ran at 23% to 40% on every packet above 34k tokens,
  and at 0% to 7% on the six small ones. The sweep never showed it: it scores
  the union of three samples, and one sample writing the walkthrough hides two
  that did not. A default review takes one sample, so a reader of a large
  change had roughly a one in three chance of a report with no walkthrough on
  it. The describing call already existed behind `--synopsis`; it writes the
  walkthrough, the judging call is then asked for findings alone, and the
  second call reads the first one's prefix from cache at a tenth of base
  input. `--no-synopsis` asks for the old one-call shape.
- **The review is no longer shown the findings the deterministic checks
  produced.** A reviewer reading a list of what the panes already caught
  spends its attention on their ground, and the report carries those findings
  whether or not the model saw them. What stays is the two things that are
  about the checks rather than their output: which linters ran, so the
  reviewer leaves their ground to them, and what no check determined, so
  silence is not read as a pass. The ruling pass is untouched — it builds its
  own contract and still rules on findings, because it is a separate call that
  exists to check the review rather than to write it.
- **The review prompts are files rather than Go string constants.** Eleven
  markdown files under `internal/review/prompts`, embedded at build time the
  way the report template already is, byte-for-byte what the constants held.
  The doc comments stay in Go beside the vars, where they carry the
  measurements and incidents behind each instruction. `go:embed` fails the
  build on a missing file; a test fails on a prompt that loads empty, and
  another on a file in the directory that nothing embeds.
- **Each stage carries only its own instruction.** The system block had been
  the whole review prompt, 3,849 tokens, on every call, so a call whose job was
  to describe the change read two thousand tokens on how to judge one before
  it reached the diff.
- **The prompt text is cut to what the model cannot infer.** A model trained on
  code reviews already knows what a defect looks like, so the paragraphs
  explaining why each rule mattered spent attention on nothing. The prompt
  files go from about 2,360 words to 1,450, and the scout's exploring and
  answering briefs and the sentences `prompt.go` builds around the packet get
  the same cut.
- **A run that will not rule stops declaring the ruling contract.** The
  checking pass is off unless `--verify` asks for it, and a run without it made
  one call that still declared 443 input tokens of strict schema naming a stage
  nothing would be pinned to. Tools drop from 2,184 to 1,742 tokens on every
  default review. A verified run is untouched.

### Added
- **`.redline.yml` takes an `exclude` list.** Generated-file detection covers
  generators that say what they are — the `linguist-generated` attribute, a
  `DO NOT EDIT` or `@generated` header, filenames only a generator writes. A
  generator that does none of that had no way to be kept out of a review. The
  patterns read like `.gitignore`: a bare name matches at any depth, a trailing
  slash takes a directory, `**` crosses directories and `*` does not; matching
  is case-sensitive, because git is. Excluded paths leave the change where
  generated ones do and are named on the report with the pattern that dropped
  them. A config that cannot be parsed fails the run rather than reading as an
  empty list.

### Removed
- **`verdicts` leaves the review and findings contracts, and `category` and
  `relatedFindings` leave the comment contract.** These existed so the model
  could work on the findings list it is no longer shown. `verdictSchema` had no
  other caller and is deleted. `MergeVerdicts` stays in `run`: a `review.json`
  a skill wrote can still carry verdicts, and merging them is the agent
  boundary.

### Fixed
- **The test-delta pane stops reading a moved test as removed assertions.** A
  renamed or split test file arrives as a deleted path and one or more new
  ones, since the changed-path list has no rename detection, and netting each
  path alone reported the move as a drop. A deleted file is now judged against
  the assertions the change's new test files add; a file that only changed
  still nets against itself, so a new test cannot hide a real drop.
- **The bare `fit`, `fdescribe`, `xit` and `xdescribe` patterns no longer match
  a method of the same name**, such as `chart.fit()`. They now refuse a
  preceding dot.

### Documentation
- **Where a context provider runs.** A provider runs with its working directory
  at the root of the tree under review: the caller's checkout when it is clean
  at the reviewed revision, a detached worktree otherwise. A worktree carries
  only what git tracks, so no `node_modules` or build output unless
  `--prepare` builds them, and the root is not where a language keeps its
  project files. Also states that `{{base}}` is the merge-base SHA and that a
  provider is stopped after five minutes.

## [0.9.0] - 2026-09-15

Per-target sessions and `review --run`, `--thinking`, primed samples,
per-command help, `--no-context`.

## [0.8.1] - 2026-09-15

### Added
- `--no-lint`, to skip the lint panes.

## [0.8.0] - 2026-09-14

### Changed
- **The long prompt is the default.** 0.7.0 made the short prompt the default
  on measurements that had gone through a local proxy, which appended its own
  instructions to the system prompt. Measured again directly on
  `claude-sonnet-5`, fourteen fixtures at three samples, the short prompt
  returned a stub reply on 22 of 168 calls with thinking on or off, and the
  long prompt on none of 42. The long prompt also left under half the
  unlabelled comments and cost no more per review. `--brief` asks for the short
  one.
- The review is the same request on the Anthropic and OpenAI wires, carried by
  the tool grammar. Flag combinations the endpoint refused now degrade instead
  of failing.
- The eval scores the configuration that ships, names fixtures that produced
  nothing, checks every label against a padding reviewer and an oracle, and
  pairs each synthetic defect fixture with a clean one.

### Added
- `gpt-5.6-terra`, `gpt-5.4` and `gpt-5.3-codex` are priced, so the cost
  tripwire applies to them.

### Fixed
- **A reply made of stubs is refused.** Comments and file lines of fewer than
  two words are dropped before the check for a reply that reviewed nothing, and
  counted as `stubs=N` on the log line.

## [0.7.0] - 2026-09-13

### Changed
- **The simplest shape is the default.** The review call was rebuilt after
  measuring what each of its layers was worth: one call, a forty-line prompt,
  no tool grammar, and the review arriving as JSON in a text reply. On the two
  fixtures carrying 31 of the 38 annotated expectations, that caught 5 at $0.13
  a review against the old default's 4 at $0.10.
- Thinking is off on that call — it bought two catches out of 31 for 3.6x the
  money and twelve times the wall time. `--verify` is off too: it had run by
  default for most of the tool's life, no eval arm had ever scored it, and the
  one trace showed it suppressing nine of the ten findings that never reached
  the reader.

### Added
- The staged cohort fan-out (`--pipeline staged`), the describing stage
  (`--synopsis`), `TestLadder`, and a two-fixture probe carrying 82% of the
  signal for a sixth of the calls.
- The first score for the explore mode that had shipped unmeasured.

## [0.6.0] - 2026-09-11

### Changed
- **The scout is told what the change is for.** The author's account reaches
  the exploring scout's brief as a claim to steer searches by, while the
  answering scout is kept from it so the independent check is not biased by the
  author's own justification.
- Both wires and explore mode put cache breakpoints on the system prompt and
  the opening turn, and each governor prices that prefix at the cached rate
  once the wire has reported cached tokens for it. An eight-turn budget stays
  eight turns instead of collapsing to three.

### Added
- **A review says what it is doing.** A streamed review reports elapsed time,
  reasoning tokens and whether the answer has started, every fifteen seconds,
  and warns once when reasoning has taken three quarters of the output cap with
  nothing written.
- `--scout-model` and `--scout-effort`, to run the checking pass on a different
  model or effort than the review itself.
- The debug capture records stop reason, usage, cost and duration beside the
  body on every path, including the failing ones.

### Fixed
- The truncation error tells the two failures apart: a review cut off
  mid-finding wants a bigger cap; one that never began spent the cap reasoning
  and wants a lower effort.
- The answering scout is given the paths that changed, so guideline files
  beside the code resolve instead of only the ones at the repository root.
- `record()` checks its cap after the retag replaces its twin, so the tool no
  longer refuses the second call its own reminder asks for.
- The parity pane no longer reads a changed test file as a capability its
  sibling package is missing.

## [0.5.0] - 2026-09-10

### Added
- **The author's account of the change reaches the prompt.** The pull request's
  title and body and the commit bodies were parsed and stored and never sent,
  while the first thing the reviewer was asked for was a change that does not
  do what its commits say. They render under "The change", framed as a claim
  rather than as evidence, and the review reports where the description and the
  diff disagree.

### Changed
- **The review checks itself.** The ruling is addressed as a judge of someone
  else's findings rather than the reviewer checking its own work, withdrawal
  has a ground for a finding resting on a false premise about the language, and
  a finding whose lookup never ran is unverifiable rather than kept.
- A ruling is not sent at all when the lookups answered nothing: the findings
  go out as the review wrote them, with the reason on the report, instead of
  every one being withheld in a step that reads like a clean change.

## [0.4.1] - 2026-09-10

### Fixed
- OpenAI structured output via a forced tool call.

## [0.4.0] - 2026-09-10

See the `v0.4.0` tag.

## [0.3.3] - 2026-09-10

### Fixed
- PR head resolution in CI checkout.

## [0.3.2] - 2026-09-10

### Fixed
- `post` login for GitHub App installation tokens (GraphQL viewer).

## [0.3.1] - 2026-09-10

### Fixed
- `post` for GitHub App installation tokens.

## [0.3.0] - 2026-09-10

See the `v0.3.0` tag.

## [0.2.0] - 2026-09-09

### Added
- **Context providers.** A provider is a separate program Redline runs as a
  subprocess and reads one JSON envelope from; it links none of their code.
  Three ship: `gorefactor` for Go through a type checker, an adapter over a
  Graphify graph for everything else, and `redline-scout`, which calls a model
  of its own to decide what the change needs and then copies the bytes out of
  the tree, so the content is source and only the selection is a judgement. The
  scout is off unless a repository names it, because a model-backed provider
  makes `run` cost money.
- `--api openai`, speaking to any endpoint using the chat completions protocol.
- `--mode explore`, handing the reviewer a catalogue of the resolved context and
  a tool to fetch from it, capped in dollars.
- `--samples`, taking several independent reviews and unioning them —
  measurement found the samples disjoint: across forty-odd samples in eleven
  configurations of one fixture, no finding was ever produced by two samples.

### Changed
- The prompt asks for every defect rather than the most important one, which
  measured larger than any architectural change tried beside it: 3 of 14
  labelled defects at three samples became 7.
- The ceiling bounds the whole request: the token estimate was calibrated
  against measured runs, the provider's prompt fragment and the context header
  are priced, and a request that does not fit is refused rather than sent.

### Fixed
- A review that fails after the request went out records what it spent, so the
  cost ledger stops excluding its own expensive tail.
- The p90 it reports is the tail rather than the maximum printed twice.
- A stale coverage profile no longer reads as a change with nothing to test.
- A review written against another change is refused and said out loud rather
  than merged onto whatever ran last.
- Files no provider speaks for are named, and a provider's failure keeps its
  diagnostic.

## [0.1.5] - 2026-09-07

### Fixed
- Require harness profiles from config; fix PR coverage lookup.

## [0.1.4] - 2026-09-07

### Fixed
- Module path, fail fast without coverage, and dedupe worktree harness.

## [0.1.3] - 2026-09-07

### Added
- Worktree harness, PR coverage prepare, and a required coverage profile.

## [0.1.2] - 2026-09-07

### Added
- Document gomutants wiring and read `mutation-report.json`.

## [0.1.1] - 2026-09-07

### Added
- Harness prepare and flexible `review.json`.

## [0.1.0] - 2026-09-07

Initial release.
