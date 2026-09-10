# A review that checks its own findings

What to do about a reviewer whose findings the reader dismisses. The design
doc leaves open whether the review producer should run more than one wave;
this is the case for a review that verifies before it posts, checked against
what the other review tools do about the same problem.

It shipped. What is below is the argument as it was written, with a section
near the end recording where building it changed the design.

## The evidence

Field use on real changes produced reviews whose every finding the reader
dismissed. The reader was the author's own agent, with the repository open,
and the reason it gave most often was that the flagged construct is
consistent with the repository's own conventions. On one staging change it
dismissed four findings: a hard-coded column list that a migration could
drift from, an idempotency check outside the transaction that two ingesters
could race, a zero-based row index, and the race again in other words. Each
was dismissed because the sibling file next to it, `aws_account_feed.go`, had
made the same choice and the team had accepted the risk. Three of the four
were accurate observations. None was a defect the team wanted to hear about.

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

The reader is worth a caution of its own. An agent judging a review of its
author's change, with every reason to defend it, dismissed everything, and
"same as account feed" is a reason that works for anything account feed also
does. Consistency with a sibling is evidence of intent. It is not evidence of
correctness, and if account feed carries a real bug, offer feed now carries
it too. So the dismissals are data about the reviewer and they are not
ground truth, and the plan has to hold both.

## Why the reviewer invents findings

Six things line up, and each one on its own is defensible.

The prompt is tuned for recall. It tells the model to report every defect,
that ten findings on a change with ten is the correct answer, and to report a
finding it is unsure of at low confidence rather than withhold it. The same
prompt says not to raise a question it cannot answer from the material. A
model with no tools cannot resolve that: it either stays silent or guesses,
and it has been told to guess with a label on.

The reviewer cannot check anything. It is one call with no tools over a fixed
payload. That is what makes it replayable and cheap, and it is also why it
cannot ask the one question that would have removed the four findings above:
does this repository already do this elsewhere?

The conventions do not reach it. Without the scout, `internal/houserules`
reads only `.github/copilot-instructions.md`. The scout reads `AGENTS.md`,
`CLAUDE.md`, `CONTRIBUTING.md` and the rest, and the scout is off by default
because it runs under `redline run`, which never calls a model. And the
convention that dismissed the four findings was written nowhere: it lived in
the file next door.

Nothing downstream acts on confidence. The HTML report folds low-confidence
findings away. `redline post` sends every model finding to GitHub as a line
comment with no confidence check, so a guess the report hid lands on the pull
request with the weight of a measured fact. `--samples` unions k reviews with
no vote, because samples never overlapped when measured, and so it keeps
every sample's guesses too.

Nothing remembers. `post` skips a finding it has already posted, keyed on
head SHA plus fingerprint, and a model finding's fingerprint is its file plus
its normalised wording. The second run words the defect differently and a
push changes the SHA, so the key never matches. `review` reads nothing from
the pull request, `Merge` replaces the comments on every run, and the
author's replies go unread. The duplicated race finding above came from two
runs.

The eval cannot see any of this. What is not scored cannot be tuned, and the
design doc's own argument is that a tool tuned by feel is the thing this
producer replaced.

## What the other tools do about it

Every shipped review tool has met this failure and each has a different
answer. Read from their public material rather than their code, so what
follows is the shape of each mechanism and not its detail.

| Tool | Mechanism | What Redline takes from it |
|---|---|---|
| Cursor Bugbot | Several parallel passes, majority voting, then a validator model and trained classifiers that decide whether a candidate is real. Teams dismissing a class of finding suppress it. Reports most flagged issues resolved before merge. | The validator stage. The resolution rate as the headline number. |
| CodeRabbit | Path-scoped instructions in config, and learnings: a reply explaining why a comment does not apply is stored and applied to later reviews. Learnings are listed, edited, and ranked below explicit instructions. | Learnings as a first-class, editable, path-scoped store, with written rules taking precedence. |
| Greptile | A graph of the repository queried during review, thumbs reactions and replies feeding a per-repository memory, and observed patterns in the team's own review comments. | Reactions and replies as the feedback channel. The precedent question answered from the whole repository. |
| Graphite | Precision first: fewer comments, a very low reported false-positive rate, custom rules in plain English, and an explicit trade of coverage for trust. | The trade is a setting a team chooses, and the plan says which way Redline defaults. |
| GitHub Copilot | Instruction files, severity on every comment, and the documented advice that silence beats a low-confidence comment. | Severity and confidence gating at post time. |
| Refute-or-Promote (research) | A stage-gated pipeline where a candidate must survive an adversarial refuter with cited evidence before it is promoted. Motivated by many agents agreeing on a defect that did not exist. | Refutation with evidence as the ruling's job, and the warning that agreement is not truth. |
| Chain-of-verification (research) | Generate, plan verification questions, answer them independently, then produce the verified answer. | The question field on every finding, answered by a separate stage. |

Three lessons come out of the table together.

Every tool that reports a low false-positive rate has a stage after
generation whose only job is to throw candidates away. None of them trusts
the generator's own confidence. Redline has no such stage, and its one
confidence signal is the generator's word.

Every tool that stays usable learns from the reader, and the ones that stay
trusted keep that learning visible and editable rather than letting it close
the loop on its own. CodeRabbit ranks explicit instructions above learnings,
which is the right order: what the team wrote down beats what a reader once
said in a thread.

The trade between coverage and trust is real and the tools land in different
places on it. Graphite says few comments and almost no wrong ones. Copilot
reports five comments on the seven reviews in ten that say anything. Redline
has been measuring itself against Copilot's volume while its readers judge
it by Graphite's standard. The plan picks: a finding is posted with evidence
or it is not posted, and the recall bar is held in the eval rather than in
the pull request.

One measured result of Redline's own bears on the table. Bugbot votes across
passes; Redline measured zero overlap across forty samples and concluded that
agreement cannot be a confidence signal. The two are not in contradiction.
Redline's identity for a finding is its normalised wording, and two samples
describing one defect in different words count as two findings under it.
Once identity is the question a finding asks, which stage one below
introduces, agreement across samples becomes measurable again, and it is a
cheap experiment to rerun.

## The design

A review is posted only with the evidence it rests on. Everything else
follows from that sentence. The generator says what would settle each
finding; a cheap model goes and gets it; a ruling with the evidence in front
of it keeps, withdraws, or marks the finding as something the repository
already does on purpose. The reader's earlier replies are input. `redline
run` stays free. The session stays the whole input, so a fixture still
replays.

Four stages, each a pure function of what the stage before it wrote into the
session. The expensive model is called twice over the same prefix; the cheap
model runs once under its existing cap.

### Stage zero: what this pull request already heard

On a `--pr` target, before anything else, fetch what Redline already said on
this pull request and what came back. The fetch already exists in `post`:
the comment and review bodies, with Redline's own comments identified by
their markers. What it does not read yet is the thread under each one, the
replies, the reactions, and the resolved state, which is the same read the
read-back plan needs and one more field on the same call.

That goes into the prompt as its own section, framed as what it is: already
raised, and answered. Not as fact. The instruction is that a finding already
raised is not raised again; that an author's stated reason is this
repository's convention for the purpose of this review; and that a finding
on lines the change has since touched may be reported as resolved or as
still standing, with the line that shows which. The reply "same
pre-transaction idempotency check as account feed staging" is exactly the
text the next review of that pull request needs to see.

The section is written into the session, so the review stays replayable and
a fixture carries the pull request history it was reviewed against.

A reply can be wrong. An author dismissing a real bug teaches the next
review to stay quiet about it. So the prior response is shown to the
reviewer as the author's position, and the ruling stage says which side it
took and why, rather than the response suppressing the finding on its own.

### Stage one: findings with typed questions

The review runs as it does today, over the assembled prompt, and the schema
gains one field per comment: the question that would confirm or refute it,
with a type from a closed set that maps onto what the scout can actually do.

- precedent: does this repository do the same thing elsewhere, unchanged by
  this change? Answered by grep and a read.
- caller: what calls or reads this? Answered by `gorefactor_context` or the
  graph, or grep where neither speaks the language.
- rule: is there a written rule about this? Answered by the guideline files.
- history: why was the removed code there? Answered by git.
- type: what can this type represent? Answered by a read.

The field is required, and a finding with no answerable question is dropped
before the scout runs. That is a schema doing what a prompt line could not:
the reviewer is allowed to be unsure, and it has to say what would settle it
in terms a tool can act on. "Is this a good idea" is not a type.

Two prompt changes go with it. The list of what is not worth reporting gains
a construction this repository already uses elsewhere, unchanged by this
change, since convention beats doctrine. And the instruction to report an
unsure finding at low confidence is reworded: report it with the question
that would settle it, because it will be looked up. The enumeration
instruction stays. It is the largest measured recall gain the prompt has, and
the stages behind it are what make it affordable.

Two duplicates with the same question are one finding. That identity is
what the union of samples has lacked.

The result is written to the session as the stage-one review. Nothing is
posted from it.

### Stage two: the scout answers

The scout runs with its existing toolset and governor, and a different
brief: the diff, the stage-one findings, and their typed questions. For each
question it records what it found, tagged with the finding it answers, using
the existing record tool and roles. A precedent question is a grep and a
range. A caller question is `gorefactor_context` or the graph. A rule
question is a guideline block. The `done` notes carry what it could not
establish, per finding, which is the difference between "no precedent
exists" and "did not look".

Its envelope is written into the session beside the provider envelopes, with
the finding tags kept. This is the only non-reproducible stage, and it is
frozen the same way the current scout's output is: a fixture that carries it
carries one run of it, and `provider.version` says which model wrote it.

Because the scout now runs under `review`, the design doc's invariant holds
unchanged. `run` never calls a model. `review` is the one command that
spends money, and it now spends it in three places instead of one.

Some questions need no model. A precedent question about a file whose
sibling shares its directory and name stem is answered by putting the
sibling in the envelope, and that is a path heuristic `run` can apply for
free. Neither existing mechanism finds it today: gorefactor's sibling is
another implementation of an interface, and the parity pane compares
filenames across provider directories rather than within one. The scout is
spent on what a file listing cannot answer.

### Stage three: the ruling

A second call to the reviewer's model over the same prefix as stage one,
plus the stage-one findings, the scout's answers, and what stage zero read
from the pull request. Its job is to refute. It dedupes first, by question,
and a candidate that matches a finding already posted is ruled already
raised. For each remaining candidate it returns one of four rulings and the
evidence:

- kept, with the line in the material the finding rests on
- withdrawn, with the line that refutes it
- justified, with the place this repository already makes the same choice on
  purpose, or the author's earlier reply accepting the tradeoff
- unverifiable, when the scout found nothing either way

A kept finding is posted. A withdrawn or justified finding stays in
`review.json` with its ruling attached, so the report can show what was
raised and taken back, and so the eval can score the ruling stage on its
own. An unverifiable finding is folded the way low confidence is folded now,
and it is not posted.

Justified is the ruling the field evidence asked for. The four staging
findings were accurate and already accepted, so withdrawn would be wrong and
posting would be noise. The word the verdict schema already uses for a
deliberate suppression is the right one here, and it means the same thing:
true, and done on purpose.

The prefix is identical to stage one, so this call is mostly a cache read.

### What the reader sees

The pull request gets kept findings only, each with the evidence line and a
severity. The report gets all four classes, labelled. `review.json` gets the
whole record. The eval gets a session that contains every stage's input and
output, and replays stages one and three from it exactly.

A repository can move the line with one setting: post kept findings only,
which is the default, or also post unverifiable ones folded under a
collapsed heading for a team that would rather see them. Nothing withdrawn
or justified is ever posted.

### Learnings, in the open

The replies stage zero reads are the repository's conventions as its authors
hold them, and most of them are written nowhere else. So they are written
down: a file the reviewer reads through the guideline role, one line per
rule, with the path scope it applies to, in the shape the houserules package
already parses. "Feed staging mirrors `aws_account_feed.go`: CopyFrom with
an explicit column list, idempotency checked before the transaction" is a
line in that file, and the author has already written it.

Three rules keep it from becoming the failure the read-back plan warns of. A
person writes each line and commits it, so it is reviewed like code. A rule
the team wrote in `CLAUDE.md` or a path instruction outranks a learning,
which is CodeRabbit's ordering and the right one. And a learning suppresses
nothing by itself: it reaches the ruling stage as evidence for a justified
ruling, and the ruling says so, so a wrong learning is visible in the record
rather than silent.

Path-scoped instructions belong in `.redline.yml` beside the learnings, for
the same reason every other tool has them: a rule that applies to
`migrations/**` should not reach a review of a change that touches no
migration. The houserules package already honours `applyTo` frontmatter;
this is the same field in the config.

## What it keeps and what it gives up

Kept: `run` costs nothing. The session is the whole input. A fixture
replays. The expensive model makes no tool calls and resends no
conversation. Every stage is priced into the ledger, apart.

Given up: one review is now three calls and a fetch, and the wall clock is
roughly the sum of them. A session that includes a scout stage is not a
function of the revision, which is already true of any session the scout
touched.

The alternative of doing stage three without stage two, a second no-tools
pass over the same material, is cheaper and is not enough. A verifier shown
the same material that produced the guess can only re-read it. The reader
who dismissed the findings had the repository open, and the mechanism has to
give the ruling the same view.

Explore mode is superseded for the purpose it served. It let the reviewer
ask for context on the expensive model, from a catalogue of what was already
frozen. Stages one and two let it ask on the cheap model, from the
repository. It stays until the measurement says the stages beat it.

## Cost

Stage zero is one API read. Stage one costs what a review costs now. Stage
two is bounded by the scout's governor, a quarter by default. Stage three's
input is stage one's prefix, served from cache at a tenth of the rate, plus
the findings and the answers. Under twice today's review is the expectation.
The ledger will say, and the ledger entry carries the stages apart so a run
whose scout hit its cap can be told from one that did not.

## Measuring it

The eval has to be able to see a false positive before this can be tuned,
which it cannot today. Then the field has to say whether the readers agree.

### In the eval

Label the extras. `REDLINE_EVAL_DUMP` already writes each arm's reviews.
Read the unmatched comments and sort them: real but unlabelled goes into
`expect`, wrong goes into a new `reject` list beside `quiet`, same keyword
shape. Once that exists, `Extra` stops being neutral and the false-positive
column means what it says. This is the first step and it costs no model
calls.

Turn the dismissed field findings into fixtures. Each has a session on disk
in that repository's `.redline` directory and a reason from the reader. The
session becomes a fixture and the reason becomes its reject entry, marked as
a reader's opinion rather than a verified fact, because of the caution above.
That grows the set toward twenty from the real distribution, including the
clean changes it is short of.

Score the ruling stage on its own. On the fixtures with labelled defects, the
number that matters is how many labelled findings stage three withdraws. A
ruling that withdraws everything scores perfectly on precision and is
worthless, and the field result shows a reader can do exactly that. The bar
is that labelled defects survive it, and a prompt change to stage three that
lowers that number is a regression whatever it does to the extras.

Keep the clean rate in every comparison. A louder stage one paired with a
stricter stage three can look like an improvement on both columns while
costing more and finding the same things.

Rerun the overlap measurement with identity by question. If samples agree
under the new identity, agreement becomes a second signal the ruling can
weigh, and `--samples` stops multiplying noise.

### In the field

The read-back plan's outcomes become the headline numbers, and one is
borrowed from Bugbot: the share of posted findings acted on before merge.
Beside it, the disputed rate per finding class, per severity, and per
ruling. The eval says whether the verifier drops real defects. The field says
whether it removed the noise people were seeing. Neither alone is enough.

The target is stated so it can be missed. Disputed under one in ten posted
findings, which is the low end of what the tools above report for
themselves, while the labelled-defect bar in the eval holds. A configuration
that gets the first by losing the second is not an improvement.

## What shipped

Steps 0–7 below are implemented. Step 8 (read-back statistics) is not.
Three places where building it changed the design are worth recording,
because each was a decision the plan got wrong. The section after this one
is what the next field round showed the shipped steps still miss.

**A finding with no answerable question is folded, not dropped.** The plan
said drop. That would have broken a promise the report already makes, that an
uncertain finding costs the reader nothing and a withheld one costs them the
finding. So `none` became a fifth question kind meaning the author's own
account of a finding as speculation, and it forces low confidence, which the
report already folds and `post` now withholds. Nothing is deleted, and the
precision is the same.

**`diff` is a question kind, and it is the strongest one.** The plan's set was
all lookups, which left no honest answer for a finding the material already
settles: a swallowed error visible in the hunk needs nothing fetched and is
not speculation. Without it, every self-evident finding would have had to
claim a lookup it did not need.

**Nothing new was built to carry a withdrawn finding.** The plan had it living
in `review.json` with the report learning to render a new class. Low confidence
already means folded on the report and withheld from the pull request, and
`Context` already renders as a quote under a finding in both renderers. So a
ruling sets confidence and writes its reason into `Context`, and one rule
governs what reaches an author instead of two that can disagree.

The ruling has five verdicts rather than four: `already-raised` earned its own
word instead of being folded into `justified`, because they are different
facts about a finding and a reader of the report wants to know which.

Running the branch's own review over itself also turned up a defect nobody had
noticed. `IsTest` reads anything under `testdata` as test material, which is
right for the composition table and wrong for deciding which bodies to hold
back from a review. This repository's fixture README reached one labelled a
test file that had "moved", with the review told to judge the code it tests.
`IsTestCode` is the narrower predicate the review uses now.

Two things in the list below are not finished, and both need something this
session did not have. Freezing the field sessions as fixtures needs the
`.redline` directories from the runs that produced the dismissals. Rerunning
the sampling overlap under the new identity costs model calls.

## What the next field round showed

The same pull request was reviewed again on the **same head SHA**, after the
author had replied `**Not fixing (intentional).**` on every first-round
thread and **without resolving those threads**. The second `/review-bot
review` posted seven more inline comments.

That second pass was still the **unpatched producer**: Marketplace had not
pinned this branch. It is therefore the baseline this work is supposed to
beat, not a failure of the stages below. It is also a list of gaps the
stages as shipped would still leave open once the pin lands.

### Where the ruling gets the context

Three inputs, in order of reliability:

1. **Prior threads on this PR** (`PriorReview`). `redline run --pr` fetches
   Redline's own comments plus every reply, reaction, and resolved flag.
   The review prompt already includes those replies whether or not the
   thread is closed (`outcome: answered, thread still open`). It tells the
   model not to raise them again, and to treat an explanation of intent as
   this repository's convention for the rest of the review.
   `feedback.Dismissed` is unused on this path. It is a helper for
   read-back statistics, and it currently requires a reply *and* a closed
   thread. Marketplace replies first and resolves later, so that helper
   would mis-label those threads if stats keyed on it. The review itself
   already has the reply text.

2. **Deterministic sibling files** (`internal/precedent`). Same-directory
   name-stem, no model. Puts `aws_account_feed.go` in front of a review of
   `aws_offer_feed.go`. Stage one and stage three both see it. This is the
   cheap, reliable answer for the first-round CopyFrom / row-index / race
   comments.

3. **Scout lookups** (stage two). Only runs for findings whose question is
   answerable (`precedent`, `caller`, `rule`, `history`, `type`). The scout
   greps and records file ranges; it does not rule. Stage three sees those
   bytes and should mark matching claims `justified`. Cross-package
   mirrors (`offerfeedingest` vs `accountfeedingest`) are in reach **if**
   stage one asked a `precedent` question naming the pattern. If it asked
   `diff` or `none`, the scout never looks, and the ruling has only the
   envelope it was already given.

So: unresolved replies are already context. The remaining miss is
**finding the sibling that is not in the same directory**, unless the
generator happens to ask the scout for it.

### Same defect, new wording, new line

Two first-round warnings came back as different comments on different
anchors:

- StageObject race: first at the pre-transaction check, then at CopyFrom
  (UNIQUE vs ON CONFLICT). Same claim, new fingerprint.
- Checkpoint ordinal: first at `processObjects`, then at
  `encodeObjectOrdinal`. Same LastModified-second limitation.

Stage zero would have shown the earlier threads, including the author's
replies. `post`'s fingerprint key would not have matched, because identity
is still file plus normalised wording and the wording moved. Question
identity in the ruling is supposed to collapse those, **if** the ruling
treats them as the same question. A ruling asked only "has this pull request
heard this exact comment" can honestly say no.

### Precedent next door is not precedent in the package next door

Same-directory name-stem (`aws_offer_feed.go` beside `aws_account_feed.go`)
is the case `internal/precedent` solves. The second round also flagged:

- `offerfeedingest/workflow.go` vs `accountfeedingest/workflow.go`
- `offer_feed_ingest.go` vs `account_feed_ingest.go`
- schedule `Request{}` vs the account-feed schedule

Those are **parallel packages**, same basename, not the file beside it. The
parity pane compares missing filenames across those directories; it does
not put the sibling's body in the envelope. gorefactor's sibling role still
needs a shared interface. So checkpoint encoding, parse-fail skip, and
empty schedule Request would still look novel after the pin.

### Info and hedges still reach the inbox

Eight of the fifteen comments were **Info**. `post` withholds only
`source: llm` + `confidence: low`. Info is still a line comment. The merge
gate does not block on LLM findings at all (`findingAttestMarker` skips
them), but Marketplace still triages every inline thread.

One Warning admitted it was "acceptable but worth noting" and then posted.
The prompt already forbids "may / might / consider"; nothing in `post`
drops a comment that says so in other words.

### What would have been enough on that change

After the pin, prior-thread text plus same-directory precedent should remove
the CopyFrom / row-index / race comments. Remaining noise on this shape of
PR, unless the items below land:

1. **Already-raised across rewording**, not only exact fingerprints. Same
   file, or same question kind + subject, on a thread that already has a
   reply. Head SHA must not reset that for a no-diff re-review.
2. **Parallel-package precedent**, deterministic. Same basename under
   directories the parity pane already treats as siblings goes in the
   envelope, so stage one does not have to ask the scout to notice
   `accountfeedingest`. The scout remains the backstop when the generator
   does ask `precedent`.
3. **Do not key review behaviour on `Dismissed`.** If read-back stats use
   it, count an attributed reply as answered even while the thread is
   still open. Marketplace replies first and resolves in merge-gate.
4. **LLM Info is not a line comment.** Body of the review, or the HTML
   report. Warning/Error only on the diff, unless a team opts in.
5. **Drop hedges at post time** if stage three did not. "acceptable",
   "worth noting", "however if" on a Warning is the generator violating
   its own silence rule; the ruling should have marked it unverifiable or
   withdrawn.

`redline learnings` would have drafted path-scoped rules from those
fifteen replies. Nobody ran it between the two reviews. That is working
as designed (a person commits the rule), and it is also why a second
review of the same head still had nothing written down. Until Marketplace
pins this branch **and** commits a small `review.instructions` for feed
ingest parity, the field will keep paying for the same lesson.

## Sequencing

0. **Read the pull request back before reviewing it.** Reuse the fetch in
   `post`, add the thread replies, reactions and resolved state, write the
   section into the session, and tell the reviewer what was already raised
   and answered. This removes the cross-run duplicate on its own, before any
   stage behind it exists, and it is the first half of
   [review-feedback.md](review-feedback.md) built for a different reader.
   **S**
1. **Stop posting what the report hides.** `post` skips model findings
   marked low confidence, and every posted model finding carries its
   severity. One condition. **S**
2. **Label the extras and freeze the field sessions.** The `reject` list in
   annotations, scored as false positives; the dismissed reviews as fixtures.
   Everything after this is measured against it. **S**
3. **Read root convention files and the sibling next door without a model.**
   Extend `internal/houserules` to the file list the scout already uses,
   bounded the same way, and add the same-directory name-stem sibling to the
   envelope from `run`. Cheap and right on their own. On the staging example
   the first would have removed nothing and the second would have put
   `aws_account_feed.go` in front of the reviewer. **S**
4. **Typed questions and the precedent rule.** Stage one's schema change,
   the drop of findings with no answerable question, the prompt lines, and
   identity by question in the union. Measure the effect alone before
   building the stages behind it, including the overlap rerun. **S**
5. **Stage two: the scout under `review`.** A second brief for the existing
   loop, finding tags on records, the envelope written into the session, the
   per-stage ledger line. **M**
6. **Stage three: the ruling.** The schema with four rulings, dedup by
   question and against stage zero, the merge into `review.json` with
   rulings kept, the report and `post` reading the ruling, the eval scoring
   the stage on labelled-defect survival. **M**
7. **Learnings and path instructions.** The file the reviewer reads through
   the guideline role, path-scoped, ranked below written rules, filled by a
   person from the replies stage zero collects. The `.redline.yml`
   instructions field beside it. **S**
8. **Read-back statistics.** The rest of
   [review-feedback.md](review-feedback.md): outcomes per posted finding,
   the acted-on and disputed rates, per class and per ruling. **M**
   (not done)
9. **Already-raised across rewording.** Same-head re-review after
   `**Not fixing (intentional).**` must not post a rewording of that thread.
   The reply is already in the prompt; identity (question kind + subject,
   or same file + similar claim) has to match it. If stats use
   `Dismissed`, count a reply without waiting for `isResolved`. **S**
10. **Parallel-package precedent.** Same basename under directories the
    parity pane already treats as siblings (shared filenames, different
    parent leaf) goes in the envelope, bounded like same-directory
    name-stem, so the scout is not the only way to see
    `accountfeedingest/workflow.go`. **S**
11. **LLM Info is body-only; hedges do not post as Warning.** Line comments
    are error/warning that survived the ruling. **S**
12. **Pin + one committed instruction.** Marketplace `REDLINE_VERSION` onto
    this branch, and a committed `review.instructions` for feed-ingest
    parity (offer mirrors account). Learnings drafts do not apply until
    someone commits them. **S** (Marketplace, not this repo)

Steps 0 through 3 ship value on their own and none of them calls a model
that `review` does not call today. Step 4 is the experiment that decides how
much the stages behind it are worth. Steps 9–11 are what the second field
round showed the shipped stages still miss. Step 12 is how the field
actually stops paying for the first round twice.

## What this does not do

It does not find what the reviewer missed. Stage three can only rule on what
stage one raised, and the recall side stays with the fixtures and the
prompt.

It does not make the ruling honest on its own. A ruling stage tuned against
a precision number alone will learn to withdraw, and the labelled-defect bar
is the only thing stopping it. That bar has to be in the eval before stage
three ships.

It does not fix findings. Bugbot's autofix and the `fix` field in Redline's
own verdicts are adjacent, and a reviewer that cannot yet be trusted to post
should not be trusted to push.

It does not trust the reader either. A reply is the author's position and it
is recorded as one. The person reading the read-back numbers is the only
party in this loop with no stake in the change, and the loop is built so that
person changes a default and nothing else does.

## Sources for the comparison

Read as search summaries, since the pages themselves were not fetched.

- Cursor, "Building a better Bugbot" and the Bugbot docs: parallel passes,
  validator model, classifiers, team feedback.
- CodeRabbit docs, "Learnings", and its configuration reference:
  learnings from replies, path instructions, precedence.
- Greptile docs, "Memory and learning": reactions, replies, observed
  patterns, the repository graph.
- Graphite, "Expected false-positive rate from AI code review tools" and the
  Diamond product pages: precision-first design, custom rules.
- GitHub docs, "Using custom instructions to unlock the power of Copilot
  code review", and the May 2026 changelog on severity levels.
- "Refute-or-Promote: An Adversarial Stage-Gated Multi-Agent Review
  Methodology for High-Precision LLM-Assisted Defect Discovery",
  arXiv 2604.19049.
- Meta AI, chain-of-verification: plan verification questions, answer them
  independently, then answer.
