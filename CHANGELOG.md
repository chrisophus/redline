# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This file starts at 0.10.0. Earlier entries are drawn from the annotated git
tags, which remain the record for them: `git tag -l --format='%(contents)' vX.Y.Z`.
Releases whose tag carries only a subject line are listed as that subject.

## [Unreleased]

### Added
- **`--note` tells the reviewer what to look at.** `redline review --note
  "..."`, or `--note-file path`, adds a note from whoever asked for the review
  to the end of every judging call: which file worries them, what to look at
  first, a question to answer. The describing call and the ruling do not see
  it. The prompt says the note is not evidence, that a rule the repository
  committed outranks it, and that a finding resting on it says so. The note is
  saved in `review.json` and the postmortem trace and shown on the report. With
  no note the request is unchanged. Whether a note turns a known miss into a
  catch has not been measured yet.

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

### Changed
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
