# What the open stack saves

> Measured on 2026-09-21 against the four open pull requests #88, #89, #90 and
> #87, stacked in that order on `115fbc7`. Everything below came out of this
> repository's own tooling: `TestWhatTheSweepWouldCost` in `internal/eval`,
> `redline review --dry-run`, `toolsTokens`, and `Usage.Cost`. No model was
> called, because none of it needs one.

## The headline

With default flags the stack saves nothing. A `redline review` run through the
`main` binary and through the stack-tip binary assembles the same request,
part for part, and sends the same bytes. The only thing that moves is the
number the tool prints about itself, which was wrong before and is right now.

Every real saving in the stack is behind a flag or a config change, and the
largest of the three is money this repository was not spending before.

## #88 counts the tool array, and saves zero tokens

The change is one term added to the sum inside `Assemble`
(`internal/review/review.go:886` on #88, line 1072 by the stack tip). It touches the estimate and nothing else,
so what goes on the wire is unchanged. What was wrong is that `promptParts`
listed a `tools` line beside a total that left it out.

The offline fixture sweep, run at `main` and at each step of the stack:

```
go test ./internal/eval -run TestWhatTheSweepWouldCost -v
```

| fixture | main | stack | delta |
|---|---:|---:|---:|
| caller-breaks-on-new-nil | 3157 | 5611 | +2454 |
| clean-caller-already-checks-nil | 3149 | 5603 | +2454 |
| clean-guard-and-caller-removed | 1626 | 4080 | +2454 |
| clean-nullable-column | 1409 | 3863 | +2454 |
| clean-regenerated-together | 1169 | 3623 | +2454 |
| correlation-not-null-column | 2753 | 5207 | +2454 |
| golangci-action-bump | 1950 | 4404 | +2454 |
| gorefactor-changectx | 35694 | 38148 | +2454 |
| gorefactor-nil-rules | 41848 | 44302 | +2454 |
| harness-injected-config | 39953 | 42407 | +2454 |
| mutation-overlay | 51362 | 53816 | +2454 |
| not-applicable-panes | 64490 | 66944 | +2454 |
| reverts-a-fix | 2579 | 5033 | +2454 |
| staged-empty-partition | 69760 | 72214 | +2454 |
| stale-generated-file | 1114 | 3568 | +2454 |
| test-delta-pane | 28870 | 31324 | +2454 |

Sweep total: expect $1.1018 before, $1.1803 after. Worst case $10.9418 before,
$11.0203 after.

The delta is flat at 2454 across all sixteen because a fixture replayed outside
its own checkout gets no Looker (`lookerFor` returns nil for a `Target.Dir`
that no longer exists), so the catalogue carries no lookups. On a live review
it is larger. Reviewing this stack's own diff against `origin/main`, with
ripgrep and git present and gorefactor absent, so four of the five lookups
offered:

```
$ redline review --run --dry-run --allow-missing-coverage --base origin/main
main binary:  110626 input tokens estimated; expect $0.2463, at most $0.8613
stack binary: 114359 input tokens estimated; expect $0.2537, at most $0.8687
both:         request by part: system 1.2k, tools 3.7k, description 2.5k,
              checks 203, missing context 151, diff 87.6k, context 18.3k,
              this pass 650
```

The `request by part` line is identical, which is the point: the request did
not change, the total caught up with it.

The catalogue itself, measured straight off `toolsTokens`:

| lookups offered | `--defer-context` off | on |
|---|---:|---:|
| none | 2454 | 2820 |
| all five | 4017 | 4383 |

So the understatement ran from 2454 to 4383 tokens, not the 2800 to 4400 the
commit message and the changelog give. The 2800 floor holds only with
`--defer-context` on, and that flag is off by default. With the shipped
defaults and gorefactor installed the figure is 4017.

The tripwire moved by the same tokens at the cache-write rate, about a cent on
a 4k catalogue. On the live run above the ceiling went from $0.8613 to $0.8687
against a default `--max-cost` of $3.00, so no review that used to go out gets
refused now.

## #89 narrows the catalogue by 855 tokens, when the describing call runs elsewhere

`toolsTokens` with the three walkthrough calls dropped, against the same four
configurations:

| lookups offered | `--defer-context` | carries them | drops them | delta |
|---|---|---:|---:|---:|
| none | off | 2454 | 1599 | -855 |
| none | on | 2820 | 1965 | -855 |
| all five | off | 4017 | 3162 | -855 |
| all five | on | 4383 | 3528 | -855 |

Constant at 855 tokens. The 2820 to 1965 and 4383 to 3528 pairs in the
changelog are exactly right.

Confirmed on the live run, same diff, same everything but the describing
endpoint:

```
$ redline review --run --dry-run --allow-missing-coverage --base origin/main \
    --synopsis-model gpt-5.6-luna --synopsis-api openai
113504 input tokens estimated; expect $0.2520, at most $0.8670
request by part: system 1.2k, tools 2.9k, ...
```

114359 down to 113504. The `tools` line goes 3.7k to 2.9k and nothing else
moves. At Sonnet 5's cache-write rate that is $0.0021.

`judgingCatalogueDescribes` gates it, and `--reuse-synopsis` clears the gate
too, so the 855 tokens come off a reused-walkthrough run as well. That path
existed before this stack.

## #89 moves the walkthrough's output tokens, and only those

The prefix term cancels exactly. Priced through `Usage.Cost`:

- Sonnet 5: input $2.00/M, cache write $2.50/M, cache read $0.20/M, output $10.00/M
- gpt-5.6-luna: input $0.20/M, output $1.20/M

On one model the describing call writes the prefix at $2.50/M and the judging
call reads it back at $0.20/M, which is $2.70/M between them. Moved, Luna pays
$0.20/M for the prefix as full input and the judging call now writes it at
$2.50/M, which is the same $2.70/M. Luna's input rate happening to equal
Sonnet's cache-read rate is what makes that true, and it is the one coincidence
the whole move rests on.

What is left is the walkthrough's output, at $10.00/M against $1.20/M:

| walkthrough | saved |
|---|---:|
| 1k tokens | $0.0088 |
| 3k tokens | $0.0264 |
| 10k tokens | $0.0880 |

Plus the $0.0021 from the narrower catalogue, so $0.0285 on a 3k walkthrough.

As a share of the review it depends almost entirely on how much the judging
call thinks, which is the largest term in the bill and the one this does not
touch. Holding the walkthrough at `ExpectedSynopsisTokens` (3000) and the
judging output at `ExpectedOutputTokens` (2500):

| prefix | one model | moved | saved | share |
|---|---:|---:|---:|---:|
| 5k | $0.0685 | $0.0400 | $0.0285 | 41.7% |
| 25k | $0.1225 | $0.0940 | $0.0285 | 23.3% |
| 50k | $0.1900 | $0.1615 | $0.0285 | 15.0% |
| 114k (the live run) | $0.3638 | $0.3352 | $0.0285 | 7.8% |
| 250k | $0.7300 | $0.7015 | $0.0285 | 3.9% |

The changelog's 7% on a 50k prefix and 3% on a 250k one reproduce at a judging
output near 20k tokens rather than 2500. Both figures are defensible and they
are about a factor of two apart, so quote the dollars, not the share.

A describing model whose input rate is above $0.20/M pays more for the prefix
than leaving the call on Sonnet costs, and loses money at any walkthrough size.
That is what the changelog's note says, and it holds.

## #90 is new spend on this repository, not a saving

On `main` the scout is commented out in `.redline.yml` and `redline run` calls
no model. #90 uncomments it and points it at `gpt-5.6-luna`. So the measured
change to this repository's bill is from nothing to something, capped by
`--max-cost` at $0.25 a run.

The saving is against running the scout on Sonnet, which nobody was doing here.
There the rates are a tenth on input, exactly, and 12% on output.

What one turn resends before the scout has read anything, measured off
`promptFor`, `briefFor` and `openAITools` with the four tools this container
offers:

```
system 1257 + brief 47 + tools 2076 = 3380 tokens per turn
8 turns of that alone: $0.0054 on gpt-5.6-luna, $0.0541 on claude-sonnet-5
```

That is a floor and not an estimate. The part that dominates is what the tools
read back, which every later turn resends, and I could not measure it: there is
no `OPENAI_API_KEY` in this container and `redline-scout` has no dry run. The
honest statement of what the cheap rate buys is that the unchanged $0.25 cap is
now 1.25M input tokens where on Sonnet it was 125k, so the governor stops the
search ten times later rather than the same search costing a tenth.

## #87 costs 259 tokens and saves none of them on the wire

`set_recap` on the catalogue, measured the same way:

| | carries the walkthrough tools | drops them |
|---|---:|---:|
| no `--since` | 2454 | 1599 |
| `--since` given | 2713 | 1599 |

259 tokens, and only on a run given a commit, and zero when the walkthrough
tools are dropped because `set_recap` goes with them. A second review still
sends the whole diff, so there is no input saving. What `--recap` shortens is
the pull request body on GitHub.

## Two things worth fixing

`--dry-run` is the one command meant for measuring, and under
`--synopsis-model` it prints a single total at the judging model's rate and
never names the second model or prices its call. The non-dry-run path does
print that line, at `cmd/redline/review.go:301`. The dry-run path
(`cmd/redline/review.go:392`) has no equivalent.

There is no `.redline/reviews.jsonl` in this checkout, so `synopsisOutputTokens`
has no rows. The changelog says to measure there before moving anything, and
the walkthrough sizes above are `ExpectedSynopsisTokens`, a constant somebody
chose, not a measurement. One real review on one model would replace the whole
middle column of the table above with an observed number.

## How to reproduce

```
git worktree add --detach /tmp/wt/main   origin/main
git worktree add --detach /tmp/wt/stack  origin/claude/review-recap

# the offline fixture sweep, in each
go test ./internal/eval -run TestWhatTheSweepWouldCost -v

# the live comparison, both binaries in the same tree so the diff is identical
go build -o /tmp/redline-main  ./cmd/redline   # in /tmp/wt/main
go build -o /tmp/redline-stack ./cmd/redline   # in /tmp/wt/stack
cd /tmp/wt/stack
/tmp/redline-main  review --run --dry-run --allow-missing-coverage --base origin/main
/tmp/redline-stack review --run --dry-run --allow-missing-coverage --base origin/main
/tmp/redline-stack review --run --dry-run --allow-missing-coverage --base origin/main \
  --synopsis-model gpt-5.6-luna --synopsis-api openai
```

The catalogue figures came from a throwaway test in `internal/review` calling
`toolsTokens` over each combination of its arguments, and the pricing tables
from a throwaway `main` calling `review.Usage.Cost` and
`review.LookupPricing`. Neither is checked in. `toolsTokens` changes signature
three times across the stack, which is why the measurement is per-commit rather
than one program.
