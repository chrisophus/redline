# Fixtures

A fixture is a frozen change plus a hand-written account of what a good review
of it should have said.

```
testdata/fixtures/<name>/
  session.json      what `redline run` wrote: the diff, the wave-one findings,
                    the context envelopes, and the producers that did not run
  annotation.json   what a good review should catch, and what it should not say
```

`session.json` is real output, written by `redline run` and copied here
unchanged. It is never re-derived. A fixture that re-observed its repository
would measure whatever the panes do today, so the review's input would move
underneath the score, and rewriting the commit it came from would break it.
Freezing also keeps the set usable when a second provider arrives: the
envelope in the file is the one the review was scored against.

`annotation.json` carries `expect` (what a good review says), `quiet` (a topic
it must stay off) and `reject` (a particular finding somebody read and judged
wrong). Scoring is keyword and location matching. That is crude, and
it is crude on purpose: it costs nothing, runs offline, and gives the same
answer twice. A model judging another model's review costs money on every eval
run and makes the measuring stick as noisy as the thing being measured. Write
the keyword lists wide, and read a disagreement as a reason to read the review
rather than to trust the number.

## Running it

```
go test ./internal/eval                       # free, offline, runs in CI
REDLINE_EVAL_MODEL=claude-sonnet-5 \
  go test ./internal/eval -run TestSweep -v   # calls a model once per fixture
```

The free half checks that every fixture loads, budgets within the context
ceiling, and assembles the same prompt twice. The paid half produces the
comparison table configurations are chosen from.

## Adding one

From a real commit, which is the preferred kind:

```
redline run --commit <sha> --out /tmp/fx
mkdir testdata/fixtures/<name>
cp /tmp/fx/session.json testdata/fixtures/<name>/
```

Then write `annotation.json`. Say what the change is, whether it is clean, and
for each expectation the words a correct finding would plausibly use.

## Recording a false positive

`expect` and `quiet` are both written before a review runs, which is why the
set was blind to the failure the field found: everything a review said that
nobody had predicted landed in `Extra`, and `Extra` is not scored as wrong. So
a reviewer could double its output with invention and score the same.

`reject` is written afterwards, from a review that actually ran. Dump the
samples, read the unmatched comments, and give each wrong one an entry:

```json
{
  "key": "column-list-drift",
  "what": "claims a hard-coded CopyFrom column list will silently mis-stage",
  "why": "mirrors aws_account_feed.go; named columns ignore order, and the
          drift risk is the same one the team accepted there",
  "source": "author",
  "file": "internal/feed/offer_feed.go",
  "any_of": ["column list", "awsOfferFeedRawColumns"]
}
```

`source` says who judged it. `fixture` is the fixture's own author reading a
dumped sample. `author` is the reader of a posted review on a real change, and
that one is evidence about the reviewer rather than ground truth: a person
dismissing a finding about their own code has a stake in the answer, and a
dismissal that is itself wrong teaches the next review to stay quiet about a
real defect. Keep the reason in `why` so a later reader can check the label
rather than inherit it.

Write the words tightly. An entry with no `any_of` or `all_of` would match
every comment on its file, so a review that found the real defect there would
score as having repeated a known mistake. `TestRejectLabelsAreSpecificEnoughToMeasure`
fails on that, and an expectation always wins over a reject on the same
comment, but neither guard can save a label whose words are merely too broad.

`make-synthetic.sh` builds the three fixtures this repository's own history
does not contain: a migration to correlate against the code, a generated file
left stale, and a change that undoes an earlier deliberate fix. It constructs a
small repository for each and runs Redline over it, so those sessions are real
output too.

## What the set has to keep covering

`TestFixtureSetCoversTheDeliberateCases` fails if any of these leaves:

- A change that deserves no comment at all. Roughly three in ten real changes
  are clean, and that is the harder target: a reviewer that always finds
  something is a generator, and the first invented problem is the last time
  anyone reads its output.
- A finding that requires correlating two producers. That case is the whole
  argument for a second wave, and without it the wave is unjustified.
- A known gap, where nothing shipping can catch the thing yet. It still scores
  as a miss. A gap that leaves the fixture set stops being tracked.
- A finding only the history of the changed lines reveals.

## The current set

| Fixture | Origin | What it tests |
|---|---|---|
| `correlation-not-null-column` | synthetic | A NOT NULL migration against a struct field that cannot express absence |
| `golangci-action-bump` | fe6ff27 | A purely mechanical change that should return clean |
| `harness-injected-config` | 4cfafc4 | An exported signature change whose callers all moved with it |
| `mutation-overlay` | 8b6d6c6 | A wide feature change across a dozen files |
| `not-applicable-panes` | 825b052 | A change about what a report must not say |
| `reverts-a-fix` | synthetic | A deliberate guard removed as a simplification |
| `stale-generated-file` | synthetic | A generated file not regenerated (known gap) |
| `test-delta-pane` | fe2df58 | Wave-one findings against the detector's own source, not to be restated |

Eight is a starting set, not the target. The plan calls for twenty, drawn from
real pull requests across the repositories this tool is used on.
