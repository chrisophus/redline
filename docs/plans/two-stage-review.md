# A review that checks its own findings

What to do about a reviewer whose findings the reader dismisses. Nothing here
is committed. The design doc leaves open whether the review producer should
run more than one wave; this is the case for a second stage, worked out far
enough to decide with.

## The evidence

Field use on real changes produced reviews whose every finding the reader
dismissed. The reader was an agent with the repository open, and the reason
it gave most often was that the flagged construct is consistent with the
repository's own conventions. The reviewer applied general doctrine to code
that does things its own way, and the reader could see the rest of the
codebase where the reviewer could not.

The eval did not predict this, and it could not have. Its false-positive
column is `QuietViolations`, which fires on a handful of hand-picked phrases,
one rule per fixture. Every other unmatched comment lands in `Extra`, which
`internal/eval/eval.go` documents as not scored as wrong. The last measured
arms on the fourteen-defect fixture wrote more extras than catches, and the
score treated that as neutral:

| Arm (`gorefactor-nil-rules`, three samples, no context) | Caught | Extras |
|---|---|---|
| claude-sonnet-5 | 4/12 | 6 |
| gpt-5.6-terra | 3/12 | 7 |

One fixture in ten is clean. Real changes are clean about three in ten.

## Why the reviewer invents findings

Four things line up, and each one on its own is defensible.

The prompt is tuned for recall. It tells the model to report every defect,
that ten findings on a change with ten is the correct answer, and to report a
finding it is unsure of at low confidence rather than withhold it. The same
prompt says not to raise a question it cannot answer from the material. A
model with no tools cannot resolve that: it either stays silent or guesses,
and it has been told to guess with a label on.

The reviewer cannot check anything. It is one call with no tools over a fixed
payload. That is what makes it replayable and cheap, and it is also why it
cannot ask the one question that would have removed most of the dismissed
findings: does this repository already do this elsewhere?

The conventions do not reach it. Without the scout, `internal/houserules`
reads only `.github/copilot-instructions.md`. The scout reads `AGENTS.md`,
`CLAUDE.md`, `CONTRIBUTING.md` and the rest, and the scout is off by default
because it runs under `redline run`, which never calls a model. On a
repository whose rules live in `CLAUDE.md`, the reviewer never sees them.

Nothing downstream acts on confidence. The HTML report folds low-confidence
findings away. `redline post` sends every model finding to GitHub as a line
comment with no confidence check, so a guess the report hid lands on the pull
request with the weight of a measured fact. `--samples` unions k reviews with
no vote, because samples never overlap, and so it keeps every sample's
guesses too.

Sampling cannot be the fix. `internal/review/samples.go` records the
measurement: across forty-odd samples, no finding was ever produced twice, so
agreement cannot separate a real finding from an invented one. That closes
the cheapest door and leaves the one below.

## What was considered before

The graph-context plan frames the choice as one-shot expansions now and an
explore loop later, and defers the loop because a live query inside the
reviewer's turn cannot be replayed by the eval. It then ships a third
arrangement: a cheap model that reads the diff, guesses what the reviewer will
need, fetches it, and freezes it into the envelope before the expensive call.

That arrangement has the scout guessing from the diff. The reviewer never says
what it actually needed, and the thing it needs most, on the evidence above,
is not visible from the diff at all. Precedent for a construct lives in files
the change did not touch. Explore mode (`internal/review/explore.go`) lets the
reviewer ask, and it asks from a catalogue of what was already frozen, on the
expensive model, resending the conversation each turn.

## The design

Split the review into three stages, each a pure function of what the stage
before it wrote into the session. The expensive model is called twice over the
same prefix; the cheap model runs once under its existing cap; `redline run`
stays free.

### Stage one: findings with questions

The review runs as it does today, over the assembled prompt, and the schema
gains one field per comment: the question that would confirm or refute it.
"Is this pattern used elsewhere in the repository, unchanged by this
change?" "What calls this function?" "Is there a written rule about this?"

The field is required. A finding the reviewer cannot say how to check is
already suspect, and the schema saying so is cheaper than a prompt saying so.
The reviewer is told that what it writes here will be looked up, which is a
different instruction from "be sure": it is allowed to be unsure, and it has
to say what would settle it.

The result is written to the session as the stage-one review. Nothing is
posted from it.

### Stage two: the scout answers

The scout runs with its existing toolset and governor, and a different brief:
the diff, the stage-one findings, and their questions. For each question it
records what it found, tagged with the finding it answers, using the existing
record tool and roles. A precedent question is a grep and a range. A caller
question is `gorefactor_context` or a grep. A rule question is a guideline
block. The `done` notes carry what it could not establish, per finding, which
is the difference between "no precedent exists" and "did not look".

Its envelope is written into the session beside the provider envelopes, with
the finding tags kept. This is the only non-reproducible stage, and it is
frozen the same way the current scout's output is: a fixture that carries it
carries one run of it, and `provider.version` says which model wrote it.

Because the scout now runs under `review`, the design doc's invariant holds
unchanged. `run` never calls a model. `review` is the one command that spends
money, and it now spends it in three places instead of one.

### Stage three: the ruling

A second call to the reviewer's model over the same prefix as stage one, plus
the stage-one findings and the scout's answers. Its job is to refute. For
each candidate it returns one of three rulings and the evidence:

- kept, with the line in the material the finding rests on
- withdrawn, with the line that refutes it
- unverifiable, when the scout found nothing either way

A kept finding is posted. A withdrawn finding stays in `review.json` with its
ruling attached, so the report can show what was raised and taken back, and
so the eval can score the ruling stage on its own. An unverifiable finding is
folded the way low confidence is folded now, and it is not posted.

The prefix is identical to stage one, so this call is mostly a cache read.

### What the reader sees

The pull request gets kept findings only, each with the evidence line. The
report gets all three classes, labelled. `review.json` gets the whole record.
The eval gets a session that contains every stage's input and output, and can
replay stages one and three from it exactly.

## What it keeps and what it gives up

Kept: `run` costs nothing. The session is the whole input. A fixture replays.
The expensive model makes no tool calls and resends no conversation. Every
stage is priced into the ledger.

Given up: one review is now three calls, and the wall clock is roughly the
sum of them. A session that includes a scout stage is not a function of the
revision, which is already true of any session the scout touched.

The alternative of doing stage three without stage two, a second no-tools
pass over the same material, is cheaper and is not enough. A verifier shown
the same material that produced the guess can only re-read it. The reader who
dismissed the findings had the repository open, and the mechanism has to give
the ruling the same view.

## Cost

Stage one costs what a review costs now. Stage two is bounded by the scout's
governor, a quarter by default. Stage three's input is stage one's prefix,
served from cache at a tenth of the rate, plus the findings and the answers.
Under twice today's review is the expectation. The ledger will say, and the
ledger entry should carry the three stages apart so a run whose scout hit its
cap can be told from one that did not.

## Measuring it

The eval has to be able to see a false positive before this can be tuned,
which it cannot today.

Label the extras. `REDLINE_EVAL_DUMP` already writes each arm's reviews. Read
the unmatched comments and sort them: real but unlabelled goes into `expect`,
wrong goes into a new `reject` list beside `quiet`, same keyword shape. Once
that exists, `Extra` stops being neutral and the false-positive column means
what it says. This is the first step and it costs no model calls.

Turn the dismissed field findings into fixtures. Each one has a session on
disk in that repository's `.redline` directory and a reason from the reader.
The session becomes a fixture and the reason becomes its reject entry. That
grows the set toward twenty from the real distribution, including the clean
changes it is short of.

Score the ruling stage on its own. On the two fixtures with labelled defects,
the number that matters is how many of the labelled findings stage three
withdraws. A ruling that withdraws everything scores perfectly on precision
and is worthless, and the field result above shows a reader can do exactly
that. The bar is that labelled defects survive it.

Keep the clean rate in every comparison. A louder stage one paired with a
stricter stage three can look like an improvement on both columns while
costing more and finding the same things.

## Sequencing

1. **Read root convention files without a model.** Extend `internal/houserules`
   to the file list the scout already uses, bounded the same way, so the
   guideline block reaches the reviewer on every run. This alone would have
   answered some share of the dismissals; the fixtures from step 3 will say
   how many. **S**
2. **Stop posting what the report hides.** `post` skips model findings marked
   low confidence. One condition. **S**
3. **Label the extras and freeze the field sessions.** The `reject` list in
   annotations, scored as false positives; the dismissed reviews as fixtures.
   Everything after this is measured against it. **S**
4. **Add the question field and a precedent rule to the prompt.** Stage one's
   schema change, and a line in "not worth reporting" for a construction the
   repository already uses elsewhere. Measure the effect on its own before
   building the stages behind it. **S**
5. **Stage two: the scout under `review`.** A second brief for the existing
   loop, finding tags on records, the envelope written into the session.
   **M**
6. **Stage three: the ruling call.** The schema, the merge into `review.json`
   with rulings kept, the report and `post` reading the ruling. **M**
7. **Per-repository conventions from dismissals.** A file the reviewer reads
   through the guideline role, one line per rule a reader stated when
   dismissing a finding. Written by a person, committed, reviewed like code.
   Not before the read-back in [review-feedback.md](review-feedback.md)
   exists to fill it. **S**

## What this does not do

It does not find what the reviewer missed. Stage three can only rule on what
stage one raised, and the recall side stays with the fixtures and the prompt.

It does not make the ruling honest on its own. A ruling stage tuned against a
precision number alone will learn to withdraw, and the labelled-defect bar in
the section above is the only thing stopping it. That bar has to be in the
eval before stage three ships, and a change to stage three's prompt that
lowers it is a regression whatever it does to the extras.

It does not close the loop with the reader. Read-back from posted reviews is
the precision signal from the people whose attention the noise costs, and it
is worked out separately. The conventions file in step 7 is where the two
plans meet.
