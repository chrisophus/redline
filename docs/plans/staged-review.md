# A review that reads the change before it judges it

What to do about a review whose wall time and output budget are spent
rebuilding an understanding of the change that nothing then keeps. The
producer is one turn today, and the turn does two jobs at once: it works out
what the change is, and it says what is wrong with it. This is the case for
splitting those, running the judgment per cohort, and paying for the shared
material once instead of once per call.

Status: **steps 1 and 2 shipped, the rest planned**. The contract goes as a
constant tool array selected by `tool_choice`, and the prompt the review and
the ruling share now carries a breakpoint, so the second call reads the first
call's prefix back instead of paying for it again. Measured A/B on one session:
158,065 input tokens uncached against 6,022 plus a 76,895-token write read back
whole, which is 30% off the input and 16% off the run. Everything after this is
a design that has to be scored before it becomes a default.

## The premise, and how to check it before building on it

The complaint this starts from is that time and cost are dominated by output
thinking tokens. Half of that is right and the half that is wrong changes what
to build.

Two numbers are recorded in the code. `internal/review/anthropic.go` reports a
real run at 171,690 input tokens. `internal/review/review.go` reports that at a
32,000-token cap, reviews that completed were emitting 23,000 to 25,000 output
tokens. On Sonnet's rates that is about 34 cents of input against 25 cents of
output, so in dollars the input is still the larger half. In wall time it is
the reverse and not close: 170k of prefill is seconds and 25k of streamed
thinking is minutes.

So output dominates the clock and input dominates the bill, and any staging
proposal trades input up to trade output down. Before any of it is built,
`redline review --stats` has to say what this installation's distribution
actually looks like: `Stats.MedianOutput` exists for exactly this question. If
median output, scaled by the model's output-to-input rate ratio (five on
Sonnet, from `priceTable`), does not beat median input, then no rearrangement
of stages saves money and everything below is a quality and latency argument
rather than a cost one. It is still worth doing on those grounds. It is not
worth selling as cheaper.

## The finding that makes staging affordable

Every stage past the first re-sends the material the first one sent. Today's
shipped two-call design already does this: `verifyCeilingCost` records that the
ruling re-sends stage one's whole prompt. A five-call pipeline would send it
five times. At 170k a call on Sonnet that is $2.04 of input per review against
today's $0.34, which is not a design, it is a way of losing.

Prompt caching is the answer and it is currently off, for a reason that was
correct about a different question. The design doc says:

> Prompt caching stays off. Cache writes cost 1.25x base input at the
> five-minute TTL and 2x at the hour, and a pre-push tool firing a few times a
> day pays every write and reads none of them.

That is a claim about caching *across runs*, and it still holds. It says
nothing about several calls sharing one prefix *inside* a single run, seconds
apart, which is the opposite arithmetic: one write at 1.25x, then N reads at
0.1x each.

### Why the shared prefix did not cache

It was tried and it missed. `ruleRequest` deliberately keeps the system block
byte-identical (`out.System = r.System`) and appends the ruling instruction to
the tail of the user turn, which under the documented invalidation rules is a
messages-tier change that preserves the tools and system caches. It still wrote
the whole prefix twice and read none of it. `anthropic.go` has the numbers:
12,449 and 11,360 tokens written on a small pair, 171,690 and 170,601 on a real
one, zero read either time.

The system block is therefore ruled out, and the only remaining difference
between the two requests was `out.Schema = ruleSchema()`. The response schema
behaves as a tools-tier object: it renders ahead of the system block and a
change to it forces a full rebuild. That is what step 1 removed.

The API's three cache tiers, and what survives a change to each:

| Change | Tools cache | System cache | Messages cache |
|---|:---:|:---:|:---:|
| Tool definitions (add / remove / reorder) | ✗ | ✗ | ✗ |
| Model switch | ✗ | ✗ | ✗ |
| System prompt content | ✓ | ✗ | ✗ |
| `tool_choice` | ✓ | ✓ | ✓ (measured; the docs say ✗) |
| Message content | ✓ | ✓ | ✗ |
| `thinking` or `effort` change | model-specific | model-specific | ✗ |

The `tool_choice` row is measured, and it disagrees with the documentation.
Anthropic's invalidation table and its troubleshooting note both say a
`tool_choice` change invalidates the message blocks. If that were true, step 2
would have nothing left but the system tier — the few thousand tokens this
document rejects below as not worth caching — and the whole sequencing would
be pointless. On the wire it holds: one 86,639-token user block under a
breakpoint on `claude-sonnet-5`, tools and system constant, three calls
seconds apart.

| call | `tool_choice` | write | read |
|---|---|---:|---:|
| 1 | `review` | 86,639 | 0 |
| 2 | `review` | 0 | 86,639 |
| 3 | `ruling` | 0 | 86,639 |

Step 1 buys nothing without that third row, so it is measured here rather than
cited, and it is the first thing to re-check on another model or if a cache
read comes back zero.

### The schema cannot be moved, and does not need to be

The obvious repair is to move the schema behind the shared prefix. It cannot
be. `output_config.format` is a top-level request parameter, not a content
block: there is no position to set, the render order `tools` → `system` →
`messages` is server-side, and a decoding constraint has to be compiled into a
grammar before the first token is sampled, so "after the prompt" is not a place
it could live.

Position is not the problem. Variance at that position is. A schema that is
byte-identical on every call caches along with everything behind it and costs
nothing after the first write.

So the fix is to stop varying it, and to move the *selector* to the end
instead:

- Drop `output_config.format`. Declare every stage's output contract as a tool
  in one tools array that is byte-identical on every request.
- Select the stage with `tool_choice`, which is the one row above that changes
  what the model emits without invalidating any of the three caches. It is GA,
  needs no beta header, and works on Sonnet 5.
- Set `strict: true` on each tool. It requires `additionalProperties: false`
  plus `required`, which every object `outputSchema()` builds already satisfies,
  because "every property is required" is already the house rule in
  `schema.go`. The guarantee that a model cannot quietly drop the field
  carrying the correlation survives the move intact.
- Put the breakpoint on the last block of the *shared user-turn content*, not
  on the system block. A breakpoint may sit on any content block, and one on
  the system block caches the system block: a few thousand tokens of the
  170,000 that matter. The shared content is one text block — the fixed block
  and the envelope — and each stage's own instruction is a second text block
  after it, which is the arrangement `ruleRequest` already uses for the
  ruling's tail.

Forced `tool_choice` returns 400 on Fable 5.1 and Mythos 5.1. On those models
`staged` is refused with that reason rather than attempted; nothing in this
document is affected at Sonnet 5 or Opus 5.

### What building it changed

The OpenAI wire needed almost none of this. It never used `response_format`:
`completeOpenAI` already sent the schema as one forced function call, because a
gateway that serves one vendor's model over another's protocol does not enforce
`response_format`, and one review in four came back unparseable when it tried.
So that wire only had to grow the array from one function to all of them and
take the name from the stage. The plan billed this as work on two wires and it
was work on one.

The catalogue is priced input now, and it was not before. Every call carries
every stage's schema, which is 1,893 tokens for the two that exist, against
roughly a thousand for the single schema a call used to carry. `Assemble` adds
it to `FixedEstimate` rather than leaving it out, for the reason the system
block is priced there: a ceiling that admits a request smaller than the one
that goes out is a ceiling that gets quoted and is wrong. So the visible
estimate went up by more than the real cost did, and both moved.

Two rejected alternatives, recorded so they are not re-proposed. Asking for
JSON in prose at the tail of the user turn genuinely does move the contract to
the end and costs nothing, and it gives up the hard guarantee at precisely the
point where it is already failing: `copilot-style-review-body.md` records the
model sparse-filling `files` on large pull requests. Prose makes that worse.
And `mid-conversation-tool-changes-2026-07-01` really does introduce a tool
from inside `messages[]` without a rebuild, but it is Opus 5 onward and behind
a beta, and it is unnecessary when all the contracts can be declared up front.

### What it costs to hold the cache

The prefix has to be constant in every tier that renders ahead of the
breakpoint, and three of those bind harder than they look.

**Effort must be pinned across the pipeline.** An `effort` change always
invalidates the messages cache and, on models that render the thinking
configuration ahead of tools and system, those too. So the summarizing stage
cannot run at `low` while the judging stages run at `high`, which was one of
the attractions. The cache-preserving form — a `content: []` system message
carrying `output_config.effort` — is Opus 5 and Fable 5.1 behind
`mid-conversation-output-config-2026-07-01`, and Sonnet 5 is not on that list.
On Sonnet the pipeline runs at one effort or it does not cache.

**The system block must be byte-identical.** Per-stage instructions move into
`messages[]` after the breakpoint. The `{"role": "system"}` mid-conversation
form is also Opus 5 and up, so on Sonnet they go in the user turn, which is
what `ruleRequest` already does and is fine.

**Caches are model-scoped, with no escape hatch.** Every cached call runs on
one model. The scout is exempt by construction: it runs its own tool loop
against its own brief, so it was never going to share this prefix.

Concurrency is not a problem here, though it is elsewhere. `samples.go` records
that concurrent calls over one fresh prefix each write it and none read, which
is why sampling gets no cache benefit. The pipeline does not hit that: stage one
completes and commits its write before the cohort calls fan out, so they read.

### The arithmetic

A 170k prefix on Sonnet 5 at $2/M input, six calls:

| | Effective input | Input cost |
|---|---|---|
| No cache (six full sends) | 1,020k | $2.04 |
| 5-minute TTL (1.25x write, 0.1x reads) | 297k | $0.60 |
| 1-hour TTL (2x write, 0.1x reads) | 425k | $0.85 |

Which TTL is a narrower question than it first looks. A cache read refreshes
the entry's timer at no cost, and the lifetime is measured from the *start* of
the request that wrote or last read it, with generation time counting against
it. So what matters is not the span from first write to last read but the
start-to-start gap between consecutive requests that share the prefix, and a
pipeline whose consecutive calls start under five minutes apart keeps the
5-minute entry warm indefinitely — the 1-hour TTL buys nothing there but the
doubled write.

Walk the critical path with that rule. Stage one writes; the cohort calls start
the moment it returns, so their gap is stage one's generation time, and they
read. The merge is the exposed call: its gap from the last cohort call's start
is that cohort's generation plus the whole scout pass, and the scout refreshes
nothing because it runs against its own prefix. That one gap is the number to
watch. The default is 5 minutes; the ledger records the gap, and the 1-hour TTL
becomes the default only when the recorded p95 of that gap reaches five
minutes. A call after stage one that reports zero cache reads is a warning on
the terminal and a column in the ledger, not a silent full-price line.

The same fix pays before any of the pipeline exists, and that part is no longer
a projection. The review-then-ruling pair was run twice over one frozen
session on `claude-sonnet-5`, `--cache` against `--no-cache`, four findings
either way:

| | Base input | Write | Read | Input cost | Run |
|---|---|---|---|---|---|
| `--no-cache` | 158,065 | 0 | 0 | $0.3161 | $0.4903 |
| `--cache` (5m) | 6,022 | 76,895 | 76,895 | $0.2196 | $0.4121 |

30% off the input and 16% off the run, the difference being output and the
scout, which no breakpoint touches. The write was read back to the token:
`cache_read_input_tokens` equals `cache_creation_input_tokens` exactly, which
is the check that the shared block really is one block and really is
byte-identical.

That pair also has the same exposed gap the merge will have — the review's
generation plus the scout pass, with no refreshing read between — so it
measures the one number that decides the TTL before any new stage exists. On
this run that gap was inside five minutes and the 5-minute entry held.

## The pipeline

Stage zero is unchanged: what this pull request already heard, read back by
`run` and frozen into the session.

One rule governs the shape of every call, and it is worth stating before the
stages because the first draft of this plan violated it. **Every cached stage
sees the whole shared prefix.** The prefix is the fixed block and the context
envelope, in one text block, and it is byte-identical on every call or nothing
is cached. So a stage cannot be given less than the prefix: a cohort call
cannot be sent "only its own diff", because the prefix carries every diff, and
stage one cannot be spared the envelope, because stage one is the call that
writes the prefix the others read. Scoping is done by instruction, in the text
block after the shared one, not by withholding input.

**Stage one — synopsis and cohorts.** One call over the prefix, told to
describe and not to judge, and told that the context blocks are there for a
later pass and are not its concern. It emits the overview, exactly one line per
shown file, and a partition of the shown files into cohorts — every shown file
in exactly one cohort, no cohort empty — each with a summary of what that group
of files does together. Shown files are the ones whose diffs the prompt
carries; the tests and generated files held back today stay held back and
belong to no cohort. This is the call that writes the cache, which is why it
has to carry the envelope it is told to ignore: the write is paid once
whichever call makes it, and only a write that lands before the fan-out is one
the cohort calls can read.

**Stage two — one review per cohort, in parallel.** Each call reads the same
prefix and is told which cohort is its job: the instruction names the cohort's
files, carries the other cohorts' summaries when `cross-summaries` is on, and
says to review its own cohort and raise anything it sees against another only
as a correlation. The other cohorts' diffs are in front of it regardless, at a
tenth of the rate, which is the consequence of the rule above and a better
one than the first draft had: the correlation a cohort call can raise is
grounded in lines it was shown, not in a summary. What the fan-out buys is not
a smaller input per call but a smaller *task* per call, and whether that lowers
per-call thinking is one of the things the arm measures rather than assumes.
The calls go out together, as `runSamples` already does: independent, sharing
no state, so the money scales and the wall clock does not.

**The scout, one per cohort, in parallel.** Unchanged in kind, and it runs
against its own brief on its own prefix, so it neither reads this cache nor
refreshes it. The governor needs a decision: `DefaultMaxCostUSD` is documented
as "the scout's whole allowance for one change" at $0.25, and one scout per
cohort would spend that allowance N times. Under `shared`, the default, each
cohort's scout gets the allowance divided by the cohort count, so the whole run
spends what one scout spends today. Under `per-cohort` each gets the full
allowance and the bill is N times larger, knowingly. A scout that exhausts its
share stops, and the findings it did not reach are ruled unverifiable, which
is what happens today when the allowance runs out.

**Stage three — the merge.** One call. It receives every cohort's findings,
numbered across cohorts the way `Candidates` numbers them today, and the scout's
answers, and it emits in one contract what two calls produce today: the rulings
on every candidate, the deduplicated comments, and the final review with the
overview and walkthrough carried over from stage one. Dedup across cohorts is by
`UnionKey`, which already prefers the finding's question over its prose and is
the identity the union of samples uses. With one cohort — a small change under
`min-files`, or `--cohorts 1` — there is nothing to merge and the stage *is*
the ruling: it degenerates to `Verify` as it ships today, over the one cohort's
findings. Under `oneshot` with the synopsis on, there is likewise one review
call and `Verify` after it; the only change from today is that the review's
contract no longer carries the overview and the file lines, because stage one
wrote them.

### What fails, and what happens then

The first draft of this plan said nothing about failure, which for a pipeline
of five calls is the same as saying the whole review fails whenever any of them
does. The shipped producer already has a rule about that — `Verify` "never
fails the review", because a review that posts unchecked is the behaviour this
tool had all along and an empty one is worse — and each stage takes the same
stance.

- **Stage one fails**, or returns something that is not a partition. The run
  falls back to `oneshot` for that review, says so on the terminal, and the
  ledger line records that it fell back. A partition that is merely imperfect
  is repaired rather than rejected: a shown file placed in no cohort goes to the
  first cohort, and one placed in two stays in the first that named it.
- **One cohort call fails**, or truncates. It is recorded the way `runSamples`
  records a failed sample — a count beside the cohort count, so a merge over
  five of six is not read as a merge over six — and the merge proceeds over the
  cohorts that answered. All of them failing is the review failing.
- **The scout exhausts its share.** The findings it did not reach are ruled
  unverifiable, as today when the allowance runs out.
- **The merge fails.** The review is the union of the cohorts' findings, unruled,
  and `VerifyFailed` says why, which is exactly what a failed ruling does today.
- **A call after stage one reads no cache.** The prefix broke somewhere. It is
  a warning naming the stage and a zero in the ledger's read column, and the run
  continues at full price rather than stopping over money already spent.

### Why cohorts, and what they cost

The case for them is not cost, it is three failures already measured in this
repository.

The walkthrough is sparse or missing. `copilot-style-review-body.md`: "On large
PRs (e.g. Marketplace PR 1284), the bot body often has no walkthrough at all:
the LLM truncates or sparse-fills `files`." Description and judgment compete
inside one output budget and judgment wins, which is the right outcome for the
wrong reason — the reader loses the orientation block entirely.

Whole reviews are lost to the cap. `review.go` records three of seven real
reviews producing nothing at 16,000 and three of the next six truncating to
zero findings at 32,000. The cap is shared between fifty-odd file summaries and
the findings.

Attention skews across the diff. `potential-enhancements.md`: "the agent left 5
comments across 4 files, skewed to two SQL predicate files, while Copilot
spread across the diff. Check whether the reviewer is capped per file or per
finding-count in a way that starves later files." A cohort is a per-group
attention budget, which is the direct answer.

The cost is the finding class the producer exists for. The design doc's
argument for calling a model at all is correlation: "A migration that adds a
non-nullable column is a fact. A struct field that cannot express absence is a
fact. Neither pane can see the other." That correlation is exactly
cross-cohort. Partition the change and the partition is drawn straight through
it.

Three things hold it. Cohorts are grouped by what changed rather than by
directory, which is the whole claim: correlated files land together or the
grouping is worthless. Every cohort call has every diff in front of it and the
other cohorts' summaries beside them, so a call that notices the migration
cohort's column while reviewing the struct's cohort can raise the correlation
from lines it was shown, and the one cost of the rule above is the thing that
makes this possible. And stage three sees all of it, which is the one place the
whole change is in front of one call whose job is the whole change.

That last point decides whether stage three shares the prefix. If it is a
merger — dedup, rule, assemble — it needs the findings and the change section
and not 170k of diff, and a cache read still costs 17k effective tokens. If it
is where cross-cohort correlation is recovered, it needs the whole prefix and
that is the strongest argument in this document for the shared cache. The plan
takes the second reading, and `review.pipeline.merge.context` makes it the
caller's choice, because the first is materially cheaper and a repository that
does not care about correlation should be able to say so.

## Configuration

Every piece below is independently switchable and **every default reproduces
today's behaviour exactly**. That is not politeness to existing users, it is
what makes the eval able to run arms: an arm is a config, and a shape that
cannot be turned off cannot be scored against the shape it replaced.

Config lands under the existing `review:` section of `.redline.yml`, which
`internal/instructions` already reads for `review.instructions`, following the
established pattern of one package unmarshalling its own section. Flags go on
the single `FlagSet` in `cmd/redline/main.go` with the `with review:` prefix
the others use. Precedence is flag, then config, then built-in default.

```yaml
review:
  cache:
    # Off preserves today's behaviour: no breakpoint, every call pays full
    # input. On is a prerequisite for pipeline: staged being affordable, and
    # is worth turning on by itself for the review-then-ruling pair.
    enabled: false
    ttl: 5m          # 5m | 1h

  pipeline:
    # oneshot is today: one call writes overview, files, comments, verdicts.
    # staged runs synopsis → cohort reviews → merge.
    mode: oneshot    # oneshot | staged

    synopsis:
      # Runs on its own under oneshot too: the walkthrough stops being sparse
      # without any of the rest of this, which is the cheapest win here.
      enabled: false
      model: ""      # empty inherits --model; a cheaper model forfeits the cache
      effort: ""     # empty inherits --effort; varying it forfeits the cache

    cohorts:
      # Cohorts are what staged means; there is no separate switch. max: 1
      # is staged with no fan-out, which is one review and the ruling.
      max: 6         # upper bound on parallel review calls
      min-files: 3   # below this the change is reviewed as one cohort
      # Whether each cohort call carries the other cohorts' summaries. Off is
      # cheaper and gives up the cross-cohort correlation from the cohort side.
      cross-summaries: true

    merge:
      # prefix: stage three reads the whole cached prefix and can correlate
      # across cohorts. findings: it sees the findings and the change section
      # only, and is a merger.
      context: prefix   # prefix | findings

  scout:
    # Divided across cohorts by default so N cohorts do not spend N allowances.
    # per-cohort gives each its own, and multiplies the bill by N knowingly.
    budget: shared      # shared | per-cohort
```

Flags, each overriding its config key:

| Flag | Effect |
|---|---|
| `--cache` / `--no-cache` | force the breakpoint on or off |
| `--cache-ttl 5m\|1h` | TTL for the write |
| `--pipeline oneshot\|staged` | the whole shape |
| `--synopsis` / `--no-synopsis` | the descriptive stage, independent of `--pipeline` |
| `--synopsis-model`, `--synopsis-effort` | when they should differ, and forfeit the cache |
| `--cohorts N` | upper bound; `--cohorts 1` is staged-without-cohorts |
| `--merge-context prefix\|findings` | what stage three sees |
| `--scout-budget shared\|per-cohort` | how the lookup allowance divides |

The paired-boolean form is the idiom `--verify` / `--no-verify` already
established in `cmdReview`, and it exists for the same reason: a caller
comparing against the old behaviour must be able to say so on the command line
without editing a file. When both halves of a pair are given, the affirmative
wins, which is what `cmdReview` already does for `--verify`.

Consistency rules the command enforces rather than discovers:

- `--pipeline staged` with `--cache=false` is allowed and warns with the
  estimate, because someone will want to measure the uncached pipeline once. It
  is never the default.
- A `--synopsis-model` or `--synopsis-effort` that differs from the review's
  turns the cache off for that stage and says so, rather than silently paying
  two writes. Model and effort are the two invalidators a caller is most likely
  to reach for without knowing what they cost.
- `--pipeline staged` with `--mode explore` is refused. Explore already spends
  its budget on turns and the two are different answers to the same question.
- `--pipeline staged` with `--samples` above one is refused, for the reason
  explore refuses it today: cohorts already spend the budget on breadth, and
  sampling a fan-out multiplies a fan-out.
- `--pipeline staged` on a model where forced `tool_choice` is rejected is
  refused with that reason, before anything is sent.

The ledger needs a column for the shape. `ledger.go` already keeps `Batched`
rows out of the distribution, on the grounds that a sweep of half-price
fixtures would drag the average somewhere no interactive run can reach; a
staged run against a oneshot run is the same problem. The `Entry` gains the
pipeline shape, the cohort count and how many of them failed, cache read and
write tokens, the merge's start-to-start gap from the last cohort call, and
whether the run fell back to `oneshot`. `Summarize` groups by shape and reports
each on its own; a mean across shapes is never printed.

## Measuring it

The prerequisite has not moved since `two-stage-review.md` named it: the eval
cannot see a false positive. Unmatched comments land in `Extra`, which
`internal/eval/eval.go` documents as not scored as wrong. A cohort arm will
produce more comments than a oneshot arm almost by construction, and until the
`reject` list exists it will look better for that reason alone. Labelling the
extras costs no model calls and has to land first.

Then, per arm, against the frozen fixtures:

- **Recall** on the labelled-defect fixtures. The headline.
- **Correlation survival.** The count of labelled correlation findings the
  staged arm produces, against the count the oneshot arm produces on the same
  fixtures at the same sample count; the bar is that the first is not below the
  second. This is the bar cohorts can fail on, and an arm that
  gains recall while losing correlations has not improved anything: it has
  traded away the reason the producer exists. A drop here is a regression
  whatever else moves.
- **Clean rate.** How often a change deserving no comment gets none. A louder
  pipeline paired with a stricter merge can look better on both other columns
  while finding the same things.
- **Comment distribution across files.** The direct measure of the skew that
  motivated cohorts. Comments per changed file, and the share of changed files
  with at least one.
- **Walkthrough completeness.** The share of shown files with a non-empty
  summary. This is the one number the synopsis stage exists to move, and it
  can be scored with no model call at all.
- **Cache reads and writes**, per stage. If reads are zero the prefix broke,
  and the payload diff or the cache-diagnosis beta says where.
- **Wall time and cost**, per shape, from the ledger.

## Sequencing

0. **Settle the premise.** `redline review --stats` on a real ledger. If the
   distribution is input-bound, say so in this document and stop selling the
   rest as a cost win. Costs nothing. **S**
1. **One tools array, `tool_choice` to select, `strict: true`.** Shipped, in
   `internal/review/tools.go`. Two things it turned out differently from the
   plan are below. **M**
2. **Cache the shared prefix.** Shipped. `--cache` (on by default) and
   `--cache-ttl`, breakpoint on the shared user-turn block, `cached` in the
   ledger beside the read and write counts already there, and a warning when a
   ruling that asked for the cache reads none of it. Measured at 30% off the
   input on the A/B above. One thing it needed that this document did not say:
   the shared content and the stage's instruction were one concatenated
   string, not two blocks, so `Result` had to grow a `Tail` and the wire had to
   build a two-block user turn. A breakpoint on the old arrangement would have
   marked a block whose bytes differ per stage and read back nothing. **S**
3. **Label the extras.** The `reject` list, as `two-stage-review.md` step 2
   already called for. Nothing after this is measurable without it. **S**
4. **The synopsis stage, under `oneshot`.** Overview, per-file summaries and
   cohorts emitted by their own call; `post` and the report read the
   walkthrough from it. Ships value alone: the walkthrough stops being sparse
   and the judging call's output cap stops being shared with fifty file
   summaries. Score walkthrough completeness and the truncation rate. **M**
5. **Cohort fan-out.** `--cohorts`, the per-cohort prompt with cross-summaries,
   the scout budget split. Score against every column above, with correlation
   survival as the bar. **L**
6. **The merge stage.** `--merge-context`, dedup across cohorts, the ruling
   moved behind it. **M**
7. **Retune the defaults.** Only once 4 through 6 have been scored on the
   fixtures does any default change. Until then `oneshot` stays the default and
   every new stage is opt-in. **S**

Steps 1 and 2 are worth doing whatever happens to the rest: they make shipped
behaviour cheaper and they are the only reason a five-call pipeline is not
absurd. Step 4 is worth doing whatever happens to cohorts. Step 5 is the
experiment.

## What this does not do

It does not make the reviewer find more. Cohorts redistribute attention across
the diff and the synopsis stage frees output budget; neither teaches the
producer anything it did not know. Recall stays with the fixtures and the
prompt.

It does not remove the per-change payload. The envelope is still derived per
change and still travels in the request. The standing index the design doc
names as "the single largest structural improvement available" is a different
piece of work and this one does not substitute for it.

It does not make the pipeline a pure function of a session. It already was and
still is, with the same exception the scout already carries.

It does not settle whether cohorts are right. It makes them measurable and it
names the number they can fail on. If correlation survival drops and does not
come back, the honest outcome is that this repository's changes are too
entangled to partition, and the synopsis stage ships alone.
