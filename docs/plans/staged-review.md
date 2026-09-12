# A review that reads the change before it judges it

What to do about a review whose wall time and output budget are spent
rebuilding an understanding of the change that nothing then keeps. The
producer is one turn today, and the turn does two jobs at once: it works out
what the change is, and it says what is wrong with it. This is the case for
splitting those, running the judgment per cohort, and paying for the shared
material once instead of once per call.

Status: **planned**. Nothing here has shipped. The cache finding in the first
section is measured and is the reason the rest is affordable; everything after
it is a design that has to be scored before it becomes a default.

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
median output times five, the out-to-in rate ratio on Sonnet, does not beat
median input, then no rearrangement of stages saves money and everything below
is a quality and latency argument rather than a cost one. It is still worth
doing on those grounds. It is not worth selling as cheaper.

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

### Why the shared prefix does not cache today

It was tried and it missed. `ruleRequest` deliberately keeps the system block
byte-identical (`out.System = r.System`) and appends the ruling instruction to
the tail of the user turn, which under the documented invalidation rules is a
messages-tier change that preserves the tools and system caches. It still wrote
the whole prefix twice and read none of it. `anthropic.go` has the numbers:
12,449 and 11,360 tokens written on a small pair, 171,690 and 170,601 on a real
one, zero read either time.

The system block is therefore ruled out, and the only remaining difference
between the two requests is `out.Schema = ruleSchema()`. The response schema
behaves as a tools-tier object: it renders ahead of the system block and a
change to it forces a full rebuild.

The API's three cache tiers, and what survives a change to each:

| Change | Tools cache | System cache | Messages cache |
|---|:---:|:---:|:---:|
| Tool definitions (add / remove / reorder) | ✗ | ✗ | ✗ |
| Model switch | ✗ | ✗ | ✗ |
| System prompt content | ✓ | ✗ | ✗ |
| `tool_choice` | ✓ | ✓ | ✗ |
| Message content | ✓ | ✓ | ✗ |
| `thinking` or `effort` change | model-specific | model-specific | ✗ |

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
  what the model emits while preserving both the tools and the system cache. It
  is GA, needs no beta header, and works on Sonnet 5.
- Set `strict: true` on each tool. It requires `additionalProperties: false`
  plus `required`, which every object `outputSchema()` builds already satisfies,
  because "every property is required" is already the house rule in
  `schema.go`. The guarantee that a model cannot quietly drop the field
  carrying the correlation survives the move intact.

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

The critical path is stage one, then the cohort reviews, then a scout per
cohort, then the merge. Reviews here already run two to five minutes each, so
the gap between the first write and the last read will routinely exceed five
minutes and the 1-hour TTL is worth its heavier write to guarantee the last
read lands. Both are configurable; the default should follow what the ledger
shows about real pipeline wall time.

The same fix pays before any of the pipeline exists. Today's review-then-ruling
pair sends roughly 340k uncached; with one write and one read it is about 230k,
a third off the input of shipped behaviour, and the `cache_read_input_tokens`
field says whether it worked before anything is built on top of it.

## The pipeline

Stage zero is unchanged: what this pull request already heard, read back by
`run` and frozen into the session.

**Stage one — synopsis and cohorts.** One call over the fixed block: the pull
request description and commit bodies, the file list, the prior findings, the
diffs. Not the context envelope, which exists to support judgment rather than
description. It emits the overview, one line per changed file, and a partition
of the changed files into cohorts, each with a summary of what that group of
files does together. This is the call that writes the cached prefix.

**Stage two — one review per cohort, in parallel.** Each call gets the full
diff for its own cohort, and for every other cohort only stage one's summary,
plus the whole prior-findings list and the whole file list. The fan-out is the
shape `runSamples` already implements: independent calls that share no state,
so the money scales and the wall clock does not.

**The scout, per cohort.** Unchanged in kind. Note the governor: `DefaultMaxCostUSD`
is documented as "the scout's whole allowance for one change" at $0.25, and one
scout per cohort spends that allowance N times. It must be divided across
cohorts or raised deliberately, or cohort four's lookups get starved by cohort
one's and the rulings on its findings come back unverifiable for a reason that
has nothing to do with the findings.

**Stage three — the merge.** Takes every cohort's findings and the answers, and
writes the final review: dedup across cohorts, the ruling, the overview and the
walkthrough assembled from stage one.

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

Three things hold it, and none of them is free. Cohorts are grouped by what
changed rather than by directory, which is the whole claim: correlated files
land together or the grouping is worthless. Every cohort call carries the full
prior-findings list and every other cohort's summary, so a call can see that a
migration cohort exists and what it did, and raise a correlation against it
from its own side. And stage three sees all of it, which is the one place the
whole change is in front of one call again.

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
      enabled: false
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
without editing a file.

Three consistency rules the command enforces rather than discovers:

- `--pipeline staged` with `--cache=false` is allowed and warns with the
  estimate, because someone will want to measure the uncached pipeline once. It
  is never the default.
- A `--synopsis-model` or `--synopsis-effort` that differs from the review's
  turns the cache off for that stage and says so, rather than silently paying
  two writes. Model and effort are the two invalidators a caller is most likely
  to reach for without knowing what they cost.
- `--pipeline staged` with `--mode explore` is refused. Explore already spends
  its budget on turns and the two are different answers to the same question.

The ledger needs a column for the shape. `ledger.go` already keeps `Batched`
rows out of the distribution, on the grounds that a sweep of half-price
fixtures would drag the average somewhere no interactive run can reach; a
staged run against a oneshot run is the same problem. The `Entry` gains the
pipeline shape, the cohort count, and cache read and write tokens, and
`Summarize` reports per shape or refuses to mix them.

## Measuring it

The prerequisite has not moved since `two-stage-review.md` named it: the eval
cannot see a false positive. Unmatched comments land in `Extra`, which
`internal/eval/eval.go` documents as not scored as wrong. A cohort arm will
produce more comments than a oneshot arm almost by construction, and until the
`reject` list exists it will look better for that reason alone. Labelling the
extras costs no model calls and has to land first.

Then, per arm, against the frozen fixtures:

- **Recall** on the labelled-defect fixtures. The headline.
- **Correlation survival.** How many labelled correlation findings a cohorted
  arm still produces. This is the bar cohorts can fail on, and an arm that
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
1. **One tools array, `tool_choice` to select, `strict: true`.** Replace
   `output_config.format` on both the anthropic and openai wires; `absorb`
   reads a `tool_use` block rather than `textOf`. No behaviour change, no new
   stage. **M**
2. **Cache the shared prefix.** Breakpoint on the last system block, `--cache`
   and `--cache-ttl`, cache tokens into the ledger. Verify on the existing
   review-then-ruling pair that `cache_read_input_tokens` is non-zero. This is
   about a third off the input of shipped behaviour and it de-risks everything
   after it. Amend the design doc's caching decision with the within-run
   arithmetic, so the next reader does not re-derive the old conclusion. **S**
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
