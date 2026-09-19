# What is left

One plan, holding the work that has not shipped. Eight documents ran beside
each other until now (`staged-review.md`, `two-stage-review.md`,
`potential-enhancements.md`, `report-roadmap.md`, `review-feedback.md`,
`graph-context.md`, `copilot-style-review-body.md`,
`redline-review-tooling-learnings.md`) and most of what they proposed is in
the code. What shipped is in `CHANGELOG.md`, the README status table and the
comments next to the code. The arguments that produced it are in git history,
`git log --follow -- docs/plans`.

Effort is rough: **S** is days, **M** is weeks, **L** is longer. Each item
says what is blocking it, what evidence put it here, and what would count as
finishing it. An item with no evidence is a guess and says so.

Two rules hold across all of it. `redline run` calls no model, so looking at a
change costs nothing and can run anywhere. A finding only reaches a pull
request through the ruling's gate.

A third rule used to be here and is gone: that a review had to be a pure
function of a saved session so the test fixtures could replay it. Reviews are
not deterministic and are not meant to be. Two runs over one change turn up
different real problems, which is why `--samples` unions them.

## Where the review stands

A plain `redline review` makes two calls. The first describes the change and
writes the overview and one line per file. The second gets the findings, and
reads the first call's prompt back out of the cache instead of paying for it
again. `--verify` adds the scout's lookups and a ruling behind them.
`--cohorts N` above 1 splits the files into groups and sends one judging call
per group, with `--plan` to stop after the split and `--only-cohorts` to judge
part of it. `--defer-context` leaves the resolved context out of the prompt
and lets any pass read an entry with `get_context`; see Context the reviewer
pulls below. `--look` gives the judging passes `grep` and `read_lines`, so a
claim about code outside the diff is one they can check rather than only name.
The prompts are files under `internal/review/prompts`. `post` gates on the ruling, holds back low
confidence and hedged wording, and keeps the reviewer's info findings in the
review body instead of on the diff.

Context comes from one provider per language. `gorefactor` resolves Go through
`go/types`, and `tsrefactor` does the same for TypeScript through `ts-morph`,
scoped to `ui/**`. A file that got no context at all in two reviews of one
change came back with fourteen pieces once it was wired in. Deleted lines get
their own history role, `removal`, ranked second.

There are old scores on the test fixtures: the fan-out at three samples caught
16 of 35 labelled bugs, against 11 for the two-call shape and 4 for one call
(2026-09-13, `claude-sonnet-5`). Don't lean on them. The labels have moved
three times since, and the measuring section below explains why that
instrument is being put down. They are here as history.

## The gap every measurement points at

The reviewer checks arithmetic between things it can see in one window: this
constant against that comment, this estimate against that cap. It does not
follow a value into the function that uses it. On this repository's PR #46 an
agent reading the same change found eight real bugs and `redline review` found
none of them. The crash that agent opened with, `failures[0]` on an empty
list, has since been put in front of four more runs on changed lines and none
of them mentioned it. Naming the problem in the prompt did not help.

A second case showed the same thing from the other end. A stale React Query
cache key spread across two files was missed twice, and the first explanation
was that the frontend had no resolver, so those files were being read from the
raw diff. With `tsrefactor` wired in, both halves of the bug were in the
prompt and neither was cut for space, and the review still missed it. The
thinking trace shows the model applying the right rule several times,
including the one already written into the provider's prompt about a query key
being a contract. It never asked the one question that catches this: what does
the page's refresh button clear, and does that cover this component's queries.
So it was not missing context and it was not missing advice.

| Item | What | Effort |
|---|---|---|
| **Let the review look things up itself** | Started, and the part that shipped is the cheap part. `--look` gives the judging passes `grep` and `read_lines`: they answer in the conversation, record nothing, and cost about 600 input tokens on the catalogue because it rides the cached prefix. That is two of the six tools the scout has. What is left is the rest of the shape - the reviewer asking gorefactor for callers resolved through a type checker, and the graph for a cross-language path - and the decision that follows: a reviewer that checks its own claims is a reviewer `internal/scout` and the ruling exist to serve, and there is then nothing left for a separate ruling to reconcile. Explore mode is not this: its `fetch_context` only indexes what the packet already resolved and cannot search the tree, so that loop buys a shorter prompt rather than more thought. Nothing has measured what searching buys, and that measurement is what the rest waits on; it becomes the default when it finds things on real pull requests the current shape does not, at a price somebody will pay. Hold the cost estimate loosely: Alibaba's `ocr` is this shape run all the way out, and on a 43-file change it went past $20 a review, blew through its own token cap by 150% to 235%, and posted two confident false positives while finding one real bug nothing else found. | L |
| **A targeted check for the cross-file question** | The cache-key miss above is one question asked of every changed query or mutation: name what clears it, and show that what clears it covers this. Whether that belongs in the prompt as its own pass or in a pane as a structural check is untested, and what we know so far is that more advice in the prompt will not do it. Try it on the change that exposed the gap, where the answer is already known. | S |
| **Nothing here can run the code** | Three of four questions on one run were settled in minutes by building a request and printing it, and by sending one probe. Reading and grepping cannot get at that. The worktrees under `~/.redline/worktrees` are already checkouts of the revision under review, so building, running one test, or printing one request is within reach, and that is where the real risks were. | M |
| **Findings are "if X then Y", not claims** | Six findings over two runs were all of the form "if the SDK does X then this breaks". A reviewer that only produces those has handed the whole job to a lookup pass that costs a sixth as much. Worth counting on real runs: how many findings depend on a fact the reviewer could not get at. | S |
| **A crash that can be found without a model belongs in a pane** | An index into a list a branch can leave empty is decidable by reading the code. Four runs were shown that exact line and said nothing, so this is not prompt work. | M |
| **The ruling reads everything to judge a handful of findings** | One ruling call carried 274,471 characters to weigh six findings with two answers attached. The cache hides the cost but not the effect: a judge working under 120k tokens of unrelated material. Send it the findings, the hunks they point at, and the answers. | M |
| **Tell it the code compiles** | `golangci-lint` cannot run unless the build succeeded, so a lint pane that ran is proof the code compiles. Pass that along, and say nothing when the pane failed. It is the only way to head off a false belief about the language, which no lookup can fix: `for turn := range opts.MaxTurns` was called invalid Go four times out of four under `go 1.26`. Blocked by the gorefactor crash under Lint and external tools, which is why that pane keeps failing here. Done when a review of range-over-int stops calling it invalid. | S |
| **Prefer a definition to a habit** | A ruling settled a question about what the SDK does by pointing at `internal/scout/tools.go` building the same type the same way, when the answer was a struct tag one file away in the module cache. The scout's brief ranks evidence against a finding above evidence for it, and does not rank a definition above another call site. | S |
| **The scout cannot look outside the repository** | It now says so honestly: an empty search reports what it searched and what lies outside. The module cache is still out of scope, so a question about a dependency still ends at "nobody can check that here". | M |
| **Effort quietly thins the checking** | At `--effort high` the reviewer asked one question about three findings; at `low` it asked two about six. The finding that got a lookup is the one that reached the pull request. Worth knowing before any default moves, and a real run's postmortem already records which findings were asked about. | S |

## Context the reviewer pulls

`--defer-context` is an experiment in letting the reviewer read context when
its reasoning calls for it, rather than being handed all of it up front. The
context block leaves the prompt. Each file's diff is followed by an index of
the entries that belong to it, one line each (id, role, name, location),
matched to the changed file through the provider's `scope`, and any pass reads
an entry with `get_context`. The same entries are offered as the prompt would
have carried.

What the runs showed (`claude-sonnet-5`, September 2026, one sample each, so
read them as directions):

- On `gorefactor-nil-rules` the reviewer never called `get_context` across six
  runs at low, medium and default effort and three wordings of the index, and
  its saved thinking never mentioned the index. That was the right call.
  None of the fixture's 17 labelled defects needs anything the provider
  offers: 15 are in the diff, and the other two need `.golangci.yml` and
  call sites in `analyzer/cross_file_helpers.go`, which no provider returns.
  No other fixture in the set has a labelled defect whose evidence is in the
  offered context either, so every earlier measurement of context was a
  measurement of context the review did not need.
- `caller-breaks-on-new-nil` and its clean pair were built to need it. A
  change makes `FindUser` return nil and documents it; an unchanged caller in
  another file dereferences the result. With the context deferred, both
  passes asked for the caller in their first reply, and the review caught
  the defect (error/high, $0.03, 7 seconds), as the inline run did. On the
  clean pair the findings pass fetched the caller, reasoned about it, fetched
  the type and history, and reasoned again before writing: thinking between
  tool calls, which never appeared while every tool only recorded an answer.
- Both clean runs filed a speculative finding about callers that might
  exist and were not shown. Inline rated it error/medium and would have
  posted it; deferred rated it warning/low and would not.
- The deferred prompt on `gorefactor-nil-rules` was 35k tokens against 44k
  inline, so where context is not needed it is cheaper.

What the research behind it said, and what was changed because of it:

- A tool description is the largest lever on whether a model calls a tool,
  and should say when to use it, not only what it does. Every tool
  description was rewritten to say what the call records and when to use it,
  and `get_context` says what each kind of entry is and when a reviewer
  reaches for it.
- Low and medium effort make fewer tool calls on Sonnet 5 by design, so a
  tool-use experiment has to run at default effort or above.
- Adaptive thinking reasons between tool calls, after tool results. A loop
  whose calls are only ever answered "Recorded." gives it nothing to reason
  about, which is why every pass thought everything through before its
  first call.
- Sonnet 5 follows a stated bar in a code-review prompt and reports less, so
  "Report what survives that" was replaced by coverage language. The first
  wording also listed ways a change can go wrong, and a findings pass spent
  its whole 64k output cap reasoning; that list is gone. The judging
  instruction now reaches only the passes that judge, since a describing pass
  that read it reviewed the whole change in 45k tokens of thinking it could
  not file.

| Item | What | Effort |
|---|---|---|
| **The providers reach further out** | Shipped in both, gorefactor 0.18.0 and tsrefactor 0.1.0. A caller carries its whole enclosing function instead of the use line plus two, keyed on the declaration so two uses in one function ship it once. A `callee` role says what the change calls, which is the other half of most contract defects and of the cache-key miss above. An `indirect-caller` role carries the second hop. The `type` role reaches past a signature to field types, body types and what a changed type refers to; the interface a changed type implements is sent rather than only named; a changed interface brings its implementations, matched on a shared member rather than on still satisfying it, because an interface that gains a method is exactly when its implementations stop satisfying it; tests that reach the change through a caller are emitted, which is the cross-package test the role never reported. History caps are flags. tsrefactor also resolves `.js`, makes the functions inside a function declarations, and reports the cache-key coverage question as an unknown rather than as a resolved role, since matching two key literals is a guess. | done |
| **Speculative findings about unseen callers** | On the clean fixture both runs flagged a nil return for callers that might exist. A finding should rest on code the reviewer saw or read. Measure a sentence to that effect on the clean pair. | S |
| **The describing pass reads context too** | It fetched the caller on both synthetic runs. Cheap here, but it is not the pass that needs it. | S |
| **Try it on the cache-key PR** | The real miss this is aimed at, with tsrefactor's context and a large change, where the relevant entry is one of many. Unblocked: whole calling functions have landed, so the page's caller now carries the panel render four lines below the hook call, and the callee role carries both the hook and the panel when the page is what changed. Nothing has run it on the real change yet, and that is the next thing worth a model call. | S |
| **Decide whether context defers by default** | Not before it has run on real changes with the extended providers. | S |

## Cohorts

A review can split the files into groups and judge each group in its own call.
That ships, off by default, as `--cohorts N` on an ordinary review: one, the
default, is no split. The split rides on the describing form as a field, so a
split run and an unsplit one send the same tools.

| Item | What | Effort |
|---|---|---|
| **The merge stage** | One call behind the groups that gets every group's findings numbered together, plus the scout's answers, and returns the rulings, the deduplicated comments and the final review. Dedup by `UnionKey`, which prefers the finding's question to its wording. With one group it is the ruling as it ships today. `--merge-context` decides whether it reads the whole cached prompt, which is where a connection between two groups can still be spotted, or only the findings and the change summary, which is cheaper. | M |
| **Divide the scout's allowance** | `--scout-budget shared\|per-cohort`. The allowance is $0.25 for one change, and one scout per group spends that N times over. Shared divides it; per-cohort multiplies the bill on purpose. | S |
| **Judge only the groups that changed** | On the second round of reviewing a pull request, don't redraw anything and judge only the groups whose files moved. What's missing: keeping the split in `session.json`, a way to look up the head SHA of the last review of this pull request (the ledger stores `revision` as `baseSHA:headSHA` and nothing queries it by pull request), matching `git diff lastSHA..newHEAD` onto the saved split by file path with `repairPartition`'s fallback for new or moved files, and a CI step that restores the previous `.redline` artifact, which the workflow already uploads with fourteen-day retention. This saves the describing call and the judging calls for files nobody touched. It does not save the cache, which expires in minutes while a review round takes days. | M |
| **Spend the freed room on context when reviewing narrowly** | A review of one group leaves most of the size limit unused, because only that group's files are competing for it. That room could carry what a full review drops: design notes and path-scoped rules for the area, more test context, and the languages no resolver covers. Untested, and it needs the row above first. | M |
| **Check the premise** | `redline review --stats` on a real ledger, which now groups by shape. The arithmetic: median output times the model's output-to-input price ratio, five on Sonnet, against median input. If input wins, then no rearrangement of calls saves money and the case for splitting is about quality and speed. | S |
| **Decide the default** | The split is off by default and there is no measurement that would turn it on now that arm sweeps are retired. Decide it from use: run it on real changes for a while and see whether the comments are better and whether anyone minds the cost. | S |

Things a new stage has to live with, each measured rather than read in a
document:

- Every call in a run carries the whole prompt. What a call is told to work on
  is set by the instruction at the end, not by giving it less to read.
- Every pass answers with small tool calls over a turn loop: `set_overview`,
  `describe_file`, `add_cohort`, `add_comment`, `rule` and `done`, none of them
  strict, the same six on every call, so the cache holds across passes. Each
  call is checked against its schema and a bad one is sent back to be fixed;
  what a pass recorded is kept however it ends, and a pass that stops before
  `done` says so on the report. This replaced one strict form per stage,
  because strict forms compile into one grammar with a size limit ("the
  compiled grammar is too large") and the same forms without strict came back
  unusable: 8 of 9 findings calls wrote the comments array as a string.
  Measured on fifteen fixture reviews, 2 of 202 flat calls were rejected and
  both were fixed on retry (2026-09-16, `claude-sonnet-5`).
- A findings pass left to itself makes one call per turn, and on a six-file
  fixture split three ways one cohort hit the 12-turn cap with 12 comments. A
  reply of one or two calls is now answered with a request for every
  remaining call in the next reply, and the same run took 11 turns in all
  against 19, $0.22 against $0.45 and 48 seconds against 83. Each turn's
  output is capped at what the pass's price can still pay for.
- Model and effort have to stay the same across a cached run on Sonnet. The
  ways around that are Opus 5 and Fable 5.1 only.
- The cache lives five minutes or an hour, measured from the start of the last
  call that used it. The one-hour write costs twice as much and buys nothing
  while calls are seconds apart.
- A `tool_choice` that requires a call is refused outright on Fable 5.1 and
  Mythos 5.1, so those models are left to choose and told which calls answer
  the pass in words.
- Splitting shrinks the job a call is given, not the tokens it reads.
- `--plan` is not a cheap preview. It pays the full cache write, $0.72 to
  $0.95 on a 43-file change on Sonnet, and only pays off if a judging call
  follows and reads what it wrote.
- Group names come from a live model call, so two runs over one change can
  name and split it differently. That is why `--only-cohorts` falls back to
  matching a file path.
- `--thinking` is a big multiplier with unclear return. One eight-file group
  took 10m26s and $1.41 with it, against 8.6s and $0.056 without, and produced
  three findings, two of them useful.
- Narrowing does buy precision on the files a call is given. The one-group run
  found an extension of a known bug that no full-diff run surfaced, from
  Redline, `ocr` or Copilot.

## Using the API the way it is meant to be used

Almost every awkward thing above exists to keep the prompt cache warm. The
answer travels as tool calls because the normal way to ask for structured
output sits in front of the prompt and changing it between calls throws the
cache away. Model and effort are pinned for the whole run. The passes are
siblings rather than one conversation, each one re-sending the same material
with a different instruction stuck on the end, though each pass is now a short
conversation of its own.

What that buys, measured once: $0.3161 of input against $0.2196 on the same
pair of calls, so about 30% of the input and 16% of the run. Worth having.
Probably not worth the shape of the whole program.

Worth trying, and nobody has:

| Item | What | Effort |
|---|---|---|
| **Make it a conversation** | Describing call, then the judging turn as the next message in the same conversation. History that only grows is what the cache is designed for, so the saving mostly comes for free instead of being engineered for. It also means the judging call can see what the describing call actually said rather than being handed a rendering of it. What it gives up is running the groups at the same time, since a conversation is a chain. | M |
| **Use structured outputs like everyone else** | Ask for JSON the documented way, per call, with the form that fits that call. Costs a cache write per shape change. Price it: one review both ways, same change, compare the bill and the findings. | S |
| **Let each call pick its own model and effort** | The describing call does not need the model that finds bugs. Cheaper there, stronger where it counts, is the obvious arrangement and the cache is the only reason it is off the table. Same test: run it both ways and read the bill. | S |
| **Use tools for looking things up** | Half true now. `--look` gives the judging passes `grep` and `read_lines`, so the tools are no longer only output shapes; the recording tools still are, and the rest of the scout's surface is still on the far side of a lookup pass. Same point as the first row of the gap section. | L |

The decision this section is heading for: keep engineering around the cache,
or take the simpler shape and pay more. Two reviews of one real change, run
both ways, would settle it.

## What a person can say before the review runs

A repository can already tell the reviewer what to care about.
`review.instructions` in `.redline.yml` is path-scoped, outranks anything
`redline learnings` drafted, and reaches the prompt as context. What none of
it covers is what the person asking for this review of this change knows:
which file worries them, and what they want asked. The PR #1360 miss is the
case. The reader could have named the question in one sentence and there was
nowhere to put it.

Three rules for everything in this section. What a person types is written
into the session, so the report and `redline postmortem` can show what the
review was told. It ranks below a rule the team committed to the repository.
And it arrives as context, so it can point attention somewhere and cannot push
a finding past the ruling's gate or hold one back.

| Item | What | Effort |
|---|---|---|
| **A note from whoever asked for the review** | `--note "..."` and `--note-file path` ship. The note goes at the end of every judging call, behind the cache breakpoint: the one-call review, the judging call after a walkthrough or without one, each cohort call, and the end of explore's opening. The describing call and the ruling do not see it. It is saved in `review.json` and the postmortem and shown on the report, and the prompt tells the reviewer it is not evidence and that a finding resting on it says so. What is left is the test that says whether it works: a note naming the cross-file question on the change with the React Query cache-key miss, where the answer is known. An empty note already leaves the request byte for byte unchanged. | S |
| **Judge the files a person names** | `--only-files a.go,b.tsx`. Most of it exists: `--only-cohorts` already falls back to matching a file path inside a group, which is what makes it stable between runs. Three gaps. It only works with the split turned on. A path can be swallowed by a group whose name happens to contain the same text, because the name match runs first and stops the path match. And it narrows what gets judged, not what gets read, so the saving is output and time. Done when naming one file works under either shape and the report says which files were not judged, rather than reading like a clean review of everything. | S |
| **Put extra files in front of the reviewer** | `--include path`, for files a person knows matter and no resolver found: a sibling in another shape, a design note, the interface being implemented. Read at the revision under review, written into the session, and budgeted like any other context so they compete for room rather than shove something else out. A role has to be picked, and an unknown role ranks last and gets reported, which is the honest default. Done when an included file shows up in the packet under its own name and the budget summary says what it displaced. | S |

Build them in that order and judge them on real changes. A note the model
ignores and a file selection that picks the wrong group both look the same
from outside: a quiet review. The way to tell them apart is to use them on a
change where you already know what the reviewer should have said.

## Measuring it

The test fixtures have not earned their cost and the roadmap should stop
spending on them. About $100 of sweeps produced one conclusion that had to be
withdrawn, two results inside the noise, and nothing that changed what ships.
The ladder said a forty-line prompt beat the shipped one; every paid run in it
went through a proxy that rewrote the system prompt, and rerun directly the
rungs land at 9, 10 and 7 of 31. Free-form output against the strict forms
differed by two points, inside a noise floor of about 3.6. Effort turned out
to be a price dial. The labels moved three times, so the 16 of 35 no longer
compares to anything.

Meanwhile every real bug on record was found by something else: an agent
reading PR #46 found eight, Copilot found two on the dogfood change, `ocr`
found one, and a person reading the code found the cache-key bug. The fixtures
have never predicted a failure in the field.

So the measurement that matters is what happens to a comment after it is
posted. Reviews are already running on real pull requests. Every comment gets
a reply, a fix, a thumbs-down, or silence, and all of that is sitting in
GitHub for free. It answers the question a fixture cannot: was this comment
worth interrupting somebody for. The work is in the next section.

Keep the fixtures, stop scoring arms on them. They are still a cheap smoke
test. A change that makes the reviewer return nothing, or crash, or describe
files it was never shown, turns up on one fixture at one sample for a few
cents. Run them for that.

What that gives up: nothing catches a prompt edit that quietly loses recall.
The fixtures were not catching that either, given that arms disagree at the
noise floor and two of their conclusions were withdrawn, but it is the reason
somebody might want this section back.

| Item | What | Effort |
|---|---|---|
| **Smoke test** | One fixture, one sample, whenever the prompts or the assembly change. Does a review come back, does it parse, does it describe only the files it was shown. Cents, not sweeps. | S |
| **A ledger row per finding** | `reviews.jsonl` keeps cost, duration, stop reason and a count, and `review.json` is overwritten every run, so nothing survives to be counted later. Write a row per finding with its id, confidence and ruling. This is the denominator for everything in the next section. | S |

## Reading back what happened to a finding

Nothing measures whether a posted finding was any good. The people reading
these reviews already say what they think: they resolve a thread, react to it,
reply explaining why it is wrong, or merge without touching it. A finding
nobody replied to, nobody reacted to, sitting on lines the author never went
back to, is the shape of noise. One that got a reply and then a commit on
those lines is the shape of value. No single signal is clean, and the one
worth asking for rather than inferring is a convention in the review body:
thumbs up useful, thumbs down wrong.

The numbers don't go in the report. A report about one change is the wrong
place for a statistic about all of them.

| Item | What | Effort |
|---|---|---|
| **Record what was posted** | A row per finding when `post` runs, keyed by fingerprint plus repository and pull request. Without it there is no denominator. Half of it is the ledger row in the section above. | S |
| **Read the threads back** | `redline feedback --pr N`, or a scheduled job over recent pull requests, matching threads to fingerprints and writing down what happened: disputed, acted on, ignored, quietly resolved. `internal/feedback` already reads the markers and has `Answered` and `Disputed`. Count a reply as answered without waiting for the thread to close, because people reply first and close later. | M |
| **Report it** | `redline stats`: by substrate, by severity, by source, by ruling, over a window. It answers which checks get argued with most, whether model findings land better or worse than deterministic ones, and whether the share of changes that deserve no comment is anywhere near the three in ten the prompt assumes. | S |
| **Act on it** | A check that gets disputed more often than acted on is a candidate for demotion to info. A rule disputed on the same grounds repeatedly is a candidate for an exclusion. A tool that tunes itself toward what reviewers accept will learn to say less, and the finding people most want to ignore is sometimes the one they most need, so this informs a person changing a default rather than closing the loop on its own. | M |
| **Pin, and write one rule down** | Not this repository. The consumer's `REDLINE_VERSION` on a build that has the thread-memory work, and a committed `review.instructions` for feed ingest parity. `redline learnings` drafts path-scoped rules from replies and nobody ran it between two reviews of the same commit, which is why the same lesson got paid for twice. Two findings from the PR #1360 investigation are also still open on that repository's main, a `clearAllFilters` scope leak and a no-usage rule that lost its `UsageHistory` guard, written up as a ticket and not filed. | S |

The target, stated so it can be missed: fewer than one posted finding in ten
gets argued with. That is the low end of what the commercial tools claim for
themselves.

## What the reader is handed

| Item | What | Effort |
|---|---|---|
| Order by kind of finding, severity second | The only error on one packet was a file 501 lines long against a limit of 500, while the actual bug was filed as info. Passing the tool's severity through is right and gating is what severity is for, so the fix is in the order the report and the pull request body print things. | S |
| Catch model-written junk before it posts | A ruling once emitted `…the finding claims.dependencies.python.org.(placeholder)` into a verdict. It was in neither request nor the review response, so the model made it up, and it rendered into `report.md` and would have gone to a pull request. Bare domains and placeholder-shaped fragments in model text are cheap to catch. | S |
| The report must not contradict itself | `report.md` said no agent review was merged while `review.json` recorded the change under review and the report printed its five comments. The staleness note and the merged findings cannot be allowed to disagree. | S |
| Let the reviewer say a pane is wrong | Two `sibling-missing-file` findings claimed a capability was tested on one side only, and a test file in the same diff tested both. The reviewer can build on a pane's finding but has no way to contradict one. Wants a rebuttal in the output form, checked like any other finding, and never able to clear an error-level gate. | M |
| Hand the unknowns to the reviewer | Two Copilot findings landed exactly in Redline's own list of what it could not determine. That list is printed for the person and never given to the reviewer as work. | S |
| Test the claims in the description | The pull request title, body and commit messages now render under `## The change`. What is left is asking the reviewer to check each claim against the code rather than read it as background. Copilot does this by default and caught two bugs that way. | S |
| Click a line, see the tests that cover it | The coverage profile knows covered from uncovered but not which test ran a line. That needs per-test coverage, which means running the suite many times, so it cannot be part of `run`, which stays free. An opt-in command that writes a line-to-tests map the report reads. Mutation testing wants the same data, so build it once. | L |
| Interface section | A placeholder that only ever renders a gap. Screenshots from an agent, a deterministic list of what UI moved, or drop the section. | M |
| Verdict wording | Whether a coverage gap wants "risk / acceptable" and config drift wants "reasonable / hiding something", or whether the three words already in use cover them. Decide it against real reviews. | S |
| Verdicts on surviving mutants | "needs test", "equivalent", "acceptable" per survivor, keyed on the mutant id. | S |
| Post verdicts to the pull request | Whether decisions by a person or an agent ride along on `redline post` or stay local. | M |
| More than one reviewer in `review.json` | Merge two agents, or an agent and Bugbot, into one report without overwriting, keyed by who wrote it. | M |
| `redline serve` live loop | Replace the copy-paste handoff with a socket or stdin bridge. | L |
| Should a refused record cost a turn | The scout filed the same range twice under one finding. The resolver already dedups identical ranges, so the cost is a wasted turn rather than doubled evidence. A design question, not a bug. | S |

## The deterministic half

This is the part with no competition, and in the twenty four commits before
this was written it got no new lines at all, against 4,011 for
`internal/review`. The packet earns its cost. The reviewer, on the evidence so
far, does not. That is the argument for spending here: more panes, more of the
change actually looked at.

### Coverage

| Item | What | Effort |
|---|---|---|
| Diff coverage against the repository's baseline as a finding | Today it renders as a table with no severity and no entry in `findings`. One dogfood change ran 49% against a 79% baseline, in a repository whose own rules say never lower coverage. Threshold configurable per adopter. | S |
| Measured mode (`--measure`) | Run the repository's test command with coverage when no profile exists or the one on disk is stale. Off by default so `run` never quietly starts a ten-minute suite. | M |
| Function-level findings | Name new or rewritten functions with no test running them, anchored at the declaration. | M |
| Coverage delta | Per-package percentage at base and head on the two-worktree runner. A drop becomes a finding with the number in it. | M |
| TypeScript and lcov profiles | Read lcov or istanbul output the way Go's `coverage.out` is read. | M |
| Coverage from CI | Read a profile from a CI artifact when producing one locally is too slow. | M |

### Lint and external tools

| Item | What | Effort |
|---|---|---|
| `gorefactor lint --json` crashes | `panic: ast.Walk: unexpected node type <nil>` from `analyzer/block_naming.go:180`: a `for` with no condition leaves `s.Cond` nil and `loopCollection` walks it. Reproduced on v0.16.0. `redline/lint` reads as failed on every run here, so the lint delta is missing from every dogfood review. Upstream, not this repository. | S |
| A `tsc` wrapper | A small reporter so TypeScript errors join the lint delta without every consumer writing their own JSON flattening. | S |
| SARIF as a documented pattern | Docs and a setup recipe for tools that only emit SARIF. | S |
| Companion checks beside differs | Repositories pair an OpenAPI lint with a second command on the same file. Document it; setup could propose the pair. | S |
| Markdown lint | `markdownlint-cli2` on changed `*.md`. Docs-only changes are common and CI often skips them. | S |
| Keep suppressions out of `_test.go` | Fuzz seeds and fixture `//nolint` read as real suppressions today. | S |
| `untested-function` on the diff | Whether a changed function has any test at all, which a coverage percentage does not answer. Blocked on gorefactor emitting repository-relative paths. | S |
| Smells need line numbers | Gorefactor's smell and duplicate-block rules report no position, so nothing can put them on a line. Upstream, not this repository. | M |
| Baseline files in setup | `baseline: {mode: file}` works; `/redline-setup` should find committed debt files and wire them up. | S |

### Harness and worktree preparation

| Item | What | Effort |
|---|---|---|
| Install steps for dependencies | `ui/node_modules` in a detached review worktree, confirmed live: eslint failed because `ui/node_modules/.bin/eslint` was missing, so every UI change reviewed from a detached worktree gets no lint at all. Same shape as the `ui-embed` entry that already ships. | S |
| Mutation runs, opt-in | A profile with `produce: make mutate` scoped to changed packages. Slow and needs a database, so never part of the default prepare step. | S |
| Generated-code drift pane | Run the generators and diff, reporting which generated files drifted. | M |
| Say how stale a slow profile is | When `coverage.out` or `mutants.json` is older than the diff, say by how much in the report and in `findings.json`. Half done. | S |
| More than one profile at once | Go coverage beside UI lcov: apply scope per profile and merge what was examined without double counting. | M |
| `redline.toml` runtime config | One config for database URL, migrate command, seed and routes, to be designed when the first runtime pane ships. | L |

### New panes

| Item | What | Effort |
|---|---|---|
| As-of provenance trace | When a change introduces a snapshot or as-of value, follow it through the call graph and flag every nearby time source not derived from it (`now`, `CURRENT_DATE`, `UTCStartOfDay(now)`). One check catches both bugs Copilot found and Redline missed. | M |
| Response-shape detector | A collection field whose size grows with the number of records and has no paging. A structural check on the OpenAPI schema and what consumes it, no model. | M |
| React Query cache-key hygiene | A new query key that is not underneath the prefix the page's refresh clears, so it stays stale until the refresh interval. This is the deterministic half of the targeted check above, and the one bug of this kind traced end to end. | M |
| OpenAPI rule lint built in | Find the ruleset and spec paths and run vacuum without every repository repeating the YAML. | M |
| sqlc and codegen staleness | Regenerate and diff. Same family as generated drift. | M |
| Spec against handler | The OpenAPI pane compares contracts only. Checking that handlers match needs a running API or a static crosswalk. | L |
| Migrations against a real Postgres | Apply them to a throwaway database: lock class, rewrite risk, timing, the down migration, constraint violations against seed data. The git-level checks are the cheap half and they ship. | L |
| Fixture and seed safety scan | Look for real identifiers in changed testdata. | M |
| Import and architecture lint | `go-arch-lint` or a depguard matrix as a scoped custom tool. | S |
| UI capture | Pinned browser, route screenshots, console errors, failed requests. No image diffing. | L |
| Mutation summary line | One line from `test_efficacy`, `mutants_total`, `mutants_killed` and `mutants_lived`, with a caveat when there were infrastructure errors. | S |
| Mutation results from CI | Read the same `mutants.json` from a downloaded CI artifact, with no local run. | M |
| The rerun command in the handoff | The repro command shows on the report and is not in "Copy for the agent". | S |

### The standing graph

`cmd/redline-graphify-context` reads `graph.json` and writes context, with the
mapping in `internal/graphify` and a test keeping the rest of the module away
from it. On this repository it produced five pieces of context and every one
came back under the catch-all role, because the Go extractor emits no
interface edges at all. On a single-language repository that already has a
real resolver, the graph adds type definitions and not much else.

What it is for is narrower than this started as. A graph was tried as the
answer to the TypeScript gap and could not see that one page called a hook it
imported, because working that out needs real symbol resolution and
tree-sitter only parses syntax. A language with a resolver gets a resolver.
What the graph is left holding is everything nobody will write a resolver for:
SQL, YAML, Terraform, shell, request collections.

| Item | What | Effort |
|---|---|---|
| Try it where changes span languages | Nothing is measured, and this repository cannot pose the question: its changes are Go. | M |
| The catch-all role | Context arrives under a role Redline does not know, which ranks last and gets reported. Give it a real name only if it turns out to find things. | S |
| Graph queries as scout tools | `graph_affected` and `graph_path` ask the cross-language question directly instead of reconstructing it from a one-hop walk. The CLI answers in prose, which is wrong for a program and fine for a model. | M |
| Graph queries in the review itself | The bigger version of the row above, and it lands with the first row of the gap section rather than before it. | L |
| Check the grammars are installed | Without `tree_sitter_sql` the SQL extractor returns an error that the merge step drops, so 88 migrations produced no nodes and nothing said so. The adapter now notes a file it claimed and found nothing for. The missing grammar is upstream. | S |

One rule holds here whatever else changes. The roles `caller`, `sibling` and
`type` are drawn only from what tree-sitter parsed, because the other half of
the graph is written by a model and a guess must not be handed to the reviewer
as a resolved fact. And a hop into a node with no source file is reported as
an unknown rather than as silence: an incremental update invents bare nodes
for symbols defined outside the batch, 48 of them sharing the name `Client` on
one run.

### Gorefactor upstream

Smells and duplicate-block findings carry no position, so they cannot go on a
line. The `untested-*` rules emit module-qualified paths, which Redline strips
using `go.mod`. Orphaned-config-path fires on gitignored directories, so
consumers need placeholder directories, which the setup skill should say.

## Setup, CLI, distribution

| Item | What | Effort |
|---|---|---|
| Setup skill: worktree steps | Propose `harness.worktree` entries when linters need generated or installed files. | S |
| Setup skill: profiles | Spot `make test-coverage` and `make mutate` and propose profiles with the right scope. | S |
| `redline doctor` | Check `.redline.yml`, check the tools are installed, dry-run the worktree steps, print what would run for the current diff. | M |
| Version skew warning | Compare the skill's pin against the binary version on `run`. | S |
| Publish the findings schema | Document the version field in `findings.json` and what changed between versions. | S |
| Artifact upload recipe | A standard Actions step uploading `report.html` and `findings.json`, with the URL passed to `redline post --report-url`. | S |
| More merge gate profiles | Require the mutation section, require a coverage profile, allow missing UI lint when nothing under `ui/**` changed. | S |
| Reuse a CI run's work | When CI and a developer both run at one commit, skip the re-lint if the artifacts are fresh. | M |
| Check what the exclusions bought | The budget summary counts what was held back and nothing reads those counts across runs. | S |

## Adopter notes

- ESLint and tsc need `ui/node_modules` in detached worktrees; only the UI
  build stub is wired.
- Gorefactor runs in Redline's lint delta and not in a typical repository's
  fast lint path, so Redline is stricter than the local default. Say so to
  adopters.
- A CI baseline compare is faster than a local `make mutate` and lives outside
  Redline; pointing a harness profile at a downloaded artifact would reach it.
- oasdiff and the built-in OpenAPI pane overlap, so the report should say which
  is which rather than print the same break twice.
- Shellcheck scope is usually narrower than a repository's own shell gate. The
  setup skill should copy the real one.

## If picking a small set next

1. `ui/node_modules` in the worktree, confirmed failing live.
2. Diff coverage against the baseline as a real finding.
3. Order the report and the pull request body by kind of finding before
   severity.
4. The profiled post that loses a paid review to a race, which throws away a
   whole review's spend and reads in CI as a tooling bug.
5. Catching model-written junk before it posts.
6. A ledger row per finding, the denominator for the read-back section.
7. Generated-drift pane, Go only to start.
8. The merge stage, so the split's findings get deduplicated and ruled in one
   place.
9. Run `--look` on the cache-key change and on PR #46, which is the only thing
   that can say whether a reviewer that searches is worth its turns. Every row
   above is work; this one is the measurement the first row of the gap section
   is waiting on.

Two came off this list. `--note` shipped, and a `diff` question's claim is now
checked against what the prompt carried rather than taken on trust.
