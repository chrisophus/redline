# Reading back what happened to a finding

Nothing measures whether a finding was any good.

The eval scores eight frozen fixtures against hand-written keyword lists. That
catches a regression and it cannot settle a configuration question: the set is
small, the annotations are one person's reading, and every fixture was chosen
because someone already knew what it should say. The design doc names the
number that matters and says configuration is unchosen until it exists.

Meanwhile Redline already posts reviews to GitHub, and the people reading them
already say what they think of each one. They resolve a thread, or react to
it, or reply explaining why it is wrong, or close the PR without touching it.
That is a precision signal produced by the exact audience whose attention is
the cost, at no token spend and no extra work from anyone.

This is how to collect it. Nothing here is committed.

## Why it is worth more than more fixtures

Twenty fixtures is the plan of record and it is still worth doing. It is not a
substitute for this, for three reasons.

A fixture measures the review a fixture's author expected. Read-back measures
the review the author of the change accepted, which is the thing the product
is for.

A fixture is fixed. Read-back grows with use, so a repository that adopts
Redline is generating its own evidence about whether Redline works on it by
the second week.

And a fixture cannot see a false positive that a reviewer quietly ignored.
That is the failure this tool cares about most, because the design doc's whole
argument is that the first invented problem is the last time anyone reads the
output. An ignored finding leaves no trace in a fixture set and an obvious one
in a thread.

## What GitHub gives back

`redline post` submits one review with a `finding_marker` per comment, which
is what makes each posted comment traceable to a fingerprint. Everything below
reads from the PR's review threads and needs no new marker.

| Signal | What it means | Confidence |
|---|---|---|
| Thread resolved, no reply | Read and dealt with | Weak positive |
| Reply then a commit touching those lines | Acted on | Strong positive |
| Reply disputing it, thread resolved | Told it was wrong | Strong negative |
| 👍 / 👎 reaction | Read and judged | Medium, and cheap to give |
| PR merged, thread never touched | Ignored | Weak negative |
| Thread still open at merge | Ignored, or deferred | Weak negative |

None of these is clean on its own. A resolved thread with no reply might mean
"fixed" or "dismissed with a click". The combination is what carries: a
finding that is never replied to, never reacted to, and sits on lines the
author never touched again is the shape of noise, and a finding replied to and
then followed by a commit on those lines is the shape of value.

The one signal worth adding rather than inferring is a reaction convention,
documented in the review body: 👍 useful, 👎 wrong. It costs a reader one
click and it is the only unambiguous input on the list.

## Where the numbers go

Not into the report. A per-change report is the wrong place for a
cross-change statistic, and mixing them invites the report to argue for
itself.

`redline review --ledger` already keeps per-run cost in `~/.redline`. The same
shape fits: append one row per posted finding when it is posted, and update it
when read back. Keyed by fingerprint, which already survives line shifts,
plus the repository and the PR number.

```
finding    fp-9fceb02
repo       chrisophus/redline
pr         412
substrate  redline/lint-delta
severity   warning
source     deterministic
posted     2026-09-09T11:02Z
outcome    disputed | acted-on | ignored | resolved-silently
observed   2026-09-11T08:41Z
```

`redline stats` then answers the questions the tables in
[potential-enhancements.md](potential-enhancements.md) keep asking and nobody
can settle: which substrates get disputed most, whether `source: llm` findings
land better or worse than deterministic ones, whether the clean rate on real
changes is anywhere near the three-in-ten the prompt assumes.

## Sequencing

1. **Record what was posted.** A row per finding at `post` time, with the
   fingerprint and the PR. Useless alone, and it is what makes everything
   after it possible, so it is worth shipping before the reading side exists.
   Without it there is no denominator. **S**
2. **Read the threads back.** `redline feedback --pr N`, or a scheduled job
   over recently posted PRs, matching threads to fingerprints by marker and
   writing outcomes. **M**
3. **Report it.** `redline stats`: per substrate, per severity, per source,
   over a window. **S**
4. **Act on it.** Two obvious uses, both of which need the data first, and
   neither of which should be built before there is enough of it to argue
   with. A substrate whose findings are disputed more often than acted on is
   a candidate for demotion to info. A rule disputed on the same grounds
   repeatedly is a candidate for a scope exclusion. **M**

Step 4 is where this could go wrong. A tool that tunes itself toward what
reviewers accept will learn to say less, and saying less is not the same as
being right: the finding people most want to ignore is sometimes the one they
most need. So the numbers should inform a person changing a default, and
should not close the loop on their own. That is a decision worth writing down
before step 4 rather than after.

## What this does not measure

Whether the review found what it should have found. Read-back sees only what
was posted, so a missed finding leaves no trace here at all, exactly as it
leaves none in production. Recall stays the fixture set's job, which is the
argument for doing both and the reason the twenty-fixture plan does not go
away.
