# Redline

Gates say pass or fail. Redline shows what they saw.

A repository's harness answers each question with one bit: the tests pass,
coverage clears the threshold, the linter is quiet. That bit is the right
shape for a merge queue and the wrong shape for a reviewer, because it drops
the detail a decision needs. Which added lines does nothing execute? Which
lint findings did this change introduce, and which did it inherit? Where did
the author tell the linter to be quiet? Did the number move, and which way?
Redline measures the change and keeps that detail, in one browser view,
beside the facts no gate computes at all: the migrations, the `openapi.yaml`
diff, and a plain account of what was examined and what was not. It hides
what reviewers skip: generated code, test bodies, findings CI already gates.

`redline run` runs no model. Judging the facts is the reviewer's job, and
the reviewer is whoever is reading: a person in the browser, an agent
reading `findings.json`, or `redline review`, the one command that calls a
model. All three see the same facts and write their decisions onto the same
report, and every finding and every ruling says which of them produced it.
See `redline-design.md` for the design and the plan.

Reviewing is read-only; posting is not, and never happens on its own.
`redline post` is the one command that writes to GitHub: it submits the
session's observed findings as one pull request review, led by a
one-line-per-pane account of what was checked. `run` never posts.

It is pre-push and non-gating, and it works at both moments: on your own
uncommitted work, and on an open pull request. `--pr` fetches via `gh`
(read-only).

## The page

1. **Interface** - captures when the pane ships; until then, a gap banner,
   present only when the change touches UI
2. **Coverage** - the diff-coverage number, its profile, and its staleness,
   present only when the change has a file a profile could cover
3. **Files** - the walkthrough, with finding counts per file
4. **What Redline observed** - the findings, each with its evidence
5. **Drill in** - per-area diffs with click-a-line comments and viewed state
6. **What could not be determined** - the section most tools omit

## Status

| Check | State |
|-------|-------|
| Diff modifies a migration that exists at merge-base | shipped |
| New migration's version prefix already exists upstream | shipped |
| OpenAPI breaking-change diff (removed operations, removed response codes, newly required inputs) | shipped |
| Diff coverage from an existing profile | shipped |
| Generated-file suppression | shipped |
| Lint delta (golangci-lint, eslint at base and head) | shipped |
| Suppression triage (directives the diff adds) | shipped |
| Lint config drift (rules disabled, downgraded, excluded) | shipped |
| Measured coverage (fresh profile, per-function findings, coverage delta) | not started |
| Migration execution against a real Postgres | not started |
| sqlc staleness, spec-vs-handler agreement, vacuum linting | not started |
| Deterministic UI capture | not started |
| Context envelope and provider registry | shipped |
| `redline review`: one model call over the run's own output | shipped |
| Migration adds a NOT NULL column with no default | shipped |
| Provider parity: a capability added to one of a set of parallel implementations | shipped |
| Regeneration verification (run the generator, diff) | not started |

## Install

```
make install          # build, symlink the binary onto PATH, symlink the skills
```

Two skills come with it. `/redline` drives a review. `/redline-setup` is
run once per repository: it walks the repo for the analysis tools it
already runs, says which ones Redline picks up on its own, writes the
`.redline.yml` entries for the rest, and suggests tools that apply to the
repo but are not in use. It installs nothing and commits nothing.

The binary and the skills are symlinks into this checkout, so `make install`
once and a later `make build` is all it takes to keep them in step. A
binary older than the skill driving it is the failure mode this prevents.

`BINDIR` (default `~/.local/bin`) and `SKILLDIR` (default `~/.claude/skills`)
override where they go. `make uninstall` removes both.

That installs the skill for every repository on this machine. To commit it
into one repository instead, for teammates without this checkout:

```
make install-repo REPO=/path/to/repo    # copies both skills into .claude/skills/
```

A project skill overrides the personal one. Teammates still need the binary.

## Use

```
go build ./cmd/redline
./redline run                     # observe; markdown report on stdout
./redline review                  # review the last run with a model
./redline review --dry-run        # print the prompt and its price; call nothing
./redline run --prepare           # run harness produce steps from .redline.yml first
./redline run --format json       # findings schema on stdout
./redline run --pr 123            # observe an open pull request
./redline post --pr 123           # post the session's findings as one PR review
./redline post --pr 123 --profile .github/redline-review.yml
./redline post --pr 123 --dry-run # print the review payload instead of posting
./redline open                    # serve and open http://127.0.0.1:8765/report.html
./redline serve --stop            # stop the server for this .redline
./redline gc                      # remove this repo's cached review worktrees
```

`run` writes `.redline/findings.json`, `.redline/report.md`,
`.redline/report.html`, and any captured artifacts under
`.redline/evidence/`. `findings.json` plus git is the whole machine
interface: an agent reviewing the change reads it so it does not re-derive
what Redline measured, and runs git for anything else.

Reviewing a `--commit`, `--branch`, `--range`, or `--pr` checks that revision
out in a detached worktree, cached under `~/.redline/worktrees` and reused
across runs so a second review of the same commit is instant. The cache is
left in place on purpose; `redline gc` reclaims it for the current repository
(`--older-than 168h` keeps recent worktrees).

Target (all subcommands; pass only one): the working tree by default,
`--commit REF` for that commit against its parent (`HEAD` for the latest),
`--range A..B` for a set of commits, `--branch REF` for a branch tip,
`--pr N|URL` for a GitHub pull request.

Flags: `--base REF` (default: commit parent, range start, PR base, else
origin/main), `--upstream REF` (default: same as base), `--migrations DIR`,
`--out DIR`, `--open`, `--no-open`, `--port N` (report server, default 8765).

## Lint

Three panes, scoped to the change, none of which re-reports what CI gates:

- **Lint delta.** When the repository carries a `.golangci.*`, eslint, or
  `.gorefactor.*` config, the linter runs at the base revision (in a cached
  detached worktree) and at head, and only the findings the change
  introduces are reported, each anchored to its head line. Findings the
  change resolves are counted as a confirmation. Identity is file, rule, and
  digit-normalized message with no line number, so moved code does not
  read as new violations. A configured linter that is missing or fails at
  head fails the pane; a base revision that cannot be linted degrades the
  delta to added-line findings and says so.

  Other tools are added in `.redline.yml`, with no code change: a command,
  the globs it covers, and how to read its output (`sarif`, `spectral`, or a
  `json` field mapping). `/redline-setup` writes these entries from what
  the repository already runs. A `kind: differ` tool (oasdiff) is run once
  comparing the file at base and head directly rather than linting one
  snapshot; a `baseline: {mode: file}` tool reads a committed accepted-debt
  file as its base set instead of a second run. See `.redline.yml.example`.
- **Suppressions.** Every silencing directive the diff adds - `//nolint`,
  `eslint-disable` in its forms, `@ts-ignore`, `@ts-expect-error`,
  `# noqa`, `# type: ignore`, `pylint: disable`, `#[allow(...)]` - becomes
  an info finding naming the rule it silences, anchored to the added line.
  Directives that merely moved are not reported.
- **Config drift.** A changed lint config is read on both sides: rules the
  change disables, downgrades, or newly excludes are named individually
  for golangci YAML, eslintrc JSON, and `.eslintignore`; formats Redline
  cannot parse still produce a finding with the config diff as evidence.
  The lint delta's summary also notes when part of the delta may be
  configuration rather than code.

## Review

`run` measures. `review` judges, in one model call over what `run` already
wrote, and it is the only command that spends money.

```
redline run
redline review
```

It reads `.redline/session.json` and observes nothing itself, which is what
makes it reproducible: the same session reviewed twice sees the same
material, and a frozen session is a fixture the eval replays. The result is
written to `.redline/review.json`, the same file a human or another agent
writes by hand, and the report is re-rendered from the session with no
second observation. Verdicts already in that file are kept.

On a pull request, the review is shown what that pull request already heard
from Redline and what people said back: the comments it posted before, the
replies under them, the reactions, and whether each thread was closed. Two
runs used to post the same defect twice in different words, and nothing could
have caught that, because a reviewer finding's fingerprint is its wording and
a push changes the commit the marker names.

A posted comment also carries the question its finding asked, in a second
marker with no commit in it. That is what lets a later review recognise the
same claim in different words: a fingerprint is file plus wording, and the
wording is exactly what moves between two runs. A candidate matching an
answered thread is ruled already-raised before the ruling call is made, and a
re-review where everything has already been answered returns without paying
for a call. Only answered threads suppress anything, because a thread nobody
replied to means the author has not looked yet.

Deduplication is the smaller half. When a reader replies that something is
deliberate, they have written down a convention that exists nowhere else, in
the one place the next review can be shown it. That reply is carried as the
author's position rather than as a ruling, because an author dismissing a
finding about their own code has a stake in the answer, and the review is told
it may still disagree where it has material the author did not. The threads
are read by `run` and saved into the session, so `review` stays a pure
function of what it was given.

One turn, no tools. Context is cheap and turns are expensive: ten tool-use
turns over a growing context cost several dollars, because every turn
re-sends the whole conversation. So the context is generous and the loop is
one call.

Test code is not part of it. Changed test files are named on the request with
how many lines moved in them, and their bodies are left out, the same bargain
generated files get; a `test` expansion from a context provider is held back
the same way, and the count of what was withheld is printed. Whether the tests
assert enough is measured, by the coverage pane and by mutation, and those
answers reach the review as findings it is told not to restate. Sending the
bodies too buys a second opinion on a settled question with the budget that
would have paid for a correlation finding. A change that is only tests is the
exception: there the tests are the change, so they are sent.

The cost target is an average across reviews, not a cap on each one. Most
changes are small and cost cents; a few are large and cost more. Holding
every review to the average would trim context from exactly the large changes
that most need it, so `--ceiling` is a tail bound rather than a budget, and
the average is measured instead of asserted. Every review appends a line to
`.redline/reviews.jsonl`, and `redline review --stats` prints the
distribution:

```
12 review(s): mean $0.1840, median $0.0910, p90 $0.4400,
range $0.0120 to $0.5100, mean wall 14.2s
```

Two numbers are reported before a call, and they answer different questions.
The expected cost prices the input against what a review actually writes; the
worst case prices it against every output token the model is allowed. The
tripwire measures the worst case, because that is what a tripwire is for. The
expected figure starts from a documented default and switches to this
installation's own measured median output as soon as the ledger has one.

The bar is GitHub Copilot's code review, in cost and in effectiveness. Cost:
Copilot's code-review model carried a published multiplier of 13 premium
requests, roughly 52 cents at the legacy overage rate; it has since moved to
AI Credits billed on token consumption at list rates, so there is no pricing
structure left to arbitrage and matching its cost means matching its token
spend. Effectiveness, from GitHub's figures over 60M reviews: 71 percent of
reviews produce actionable feedback averaging 5.1 comments, and 29 percent
return clean. The clean rate is the harder target. `go test ./internal/eval`
holds the fixture set to both halves, and the paid sweep prints them beside
Copilot's.

Every comment carries a question: the one check that would confirm or refute
it, from a closed set of what can actually be looked up. Is this pattern used
elsewhere unchanged, what calls this, is there a written rule, why was the
removed code there, what can this type represent, or `diff` when what the
reviewer was already shown settles it. The set is closed because a later stage
has to run the lookup, and an open field collects questions nothing can
answer.

Requiring it does work on its own. A model that has to say how its claim could
be falsified writes fewer claims that cannot be, and a schema enforces that
where a prompt line is forgotten by the tenth comment. `none` is the honest
answer when nothing would settle a finding, and a comment carrying it is
folded away and never posted: that is the reviewer's own account of it as
speculation.

The question is also what makes two samples' comments one finding. Sampling
measured no overlap at all, which ruled out treating agreement as confidence,
but that measurement compared prose, and two samples describing one defect
never word it the same way. They ask the same question about it. The union now
prefers that, so the measurement is worth taking again.

### Checking the findings before posting them

A review is one call with no tools, so it cannot check a claim about the rest
of the repository. That is where the false positives came from in real use:
findings that were accurate observations about code the team had deliberately
written that way, dismissed in seconds by a reader who had the repository
open. Two stages give the review the same view.

The lookups run first. Each finding's question goes to the scout in its
answering mode, which greps, reads, asks gorefactor for callers, or asks git
what happened to the removed lines, and records where the answer is. It
records locations and Redline reads the bytes, the same rule the scout has
always followed, so an answer is the repository's own text rather than a
model's recollection of it.

Then a second call rules on every finding with the answers in front of it, over
the same prefix as the first, which prompt caching serves at a fraction of the
input rate. Five verdicts:

- **kept** — the evidence supports it, quoted. The only one that is posted.
- **withdrawn** — the evidence refutes it, quoted.
- **justified** — true, and this repository does it on purpose. Precedent in
  code counts, and so does the author saying so on an earlier review.
- **unverifiable** — the lookup came back with nothing either way.
- **already-raised** — this pull request has already heard it.

The four that are not kept stay on the report with their reason, folded, and
are not posted. Nothing is deleted: a reader can see what was raised and what
became of it, which is also what makes the pass measurable.

The instruction names both ways to fail. A pass that withdraws everything has
perfect precision and no value, so withdrawing takes a line you can quote and
"I am no longer sure" is unverifiable rather than withdrawn. Keeping everything
is the failure that put the pass here.

It degrades at every point. No key, no repository to look in, a lookup that
errors, a ruling that comes back unparseable: each leaves the findings as the
review wrote them and says the check did not happen. A review that posts
unchecked is the behaviour this tool always had; an empty one would be worse.
`--no-verify` turns it off, and a clean review never pays for the second call.

Findings from `review` are advisory and marked `source: llm`. They never
reach the merge gate, whatever severity they carry. A gate that blocks on
something the author cannot reproduce gets bypassed inside a month.

Flags: `--model` (default `claude-sonnet-5`, and the checking pass runs on
the same model), `--effort` (the same, with the checking pass at `low` when
it is not set), `--scout-model` and `--scout-effort` (the checking pass
alone, when it should differ from the review), `--ceiling`
(default 250000 tokens, bounding the whole request), `--max-tokens`,
`--max-cost` (a tripwire checked against the worst-case cost before anything
is sent), `--stats`, `--dry-run`, `--api`, `--base-url`, `--debug`.

`--debug` (or `REDLINE_DEBUG`) puts every model call on stderr: the wire,
model, stage, input tokens, cap, and effort going out; the stop reason and
token counts coming back; the body the parser was handed; and each scout
tool call with its result size. The full requests and responses are written
under `--out/debug`, which is how a gateway that answers in a shape the
schema forbids is diagnosed rather than guessed at. Those files carry the
whole prompt, so they carry the diff.

Credentials come from `ANTHROPIC_API_KEY` or an `ant auth login` profile.
Without one, every other command still works.

### Sending the call through a proxy

The call goes to Anthropic's API by default. `--api openai` sends the
same request over the OpenAI chat completions protocol instead, which is
what most model gateways and proxies speak whatever model sits behind
them, and `--base-url` says where. The prompt, the output schema, the
ceiling, and the tripwire are decided before the provider is consulted, so
the review is the same review on either wire; only the one-shot mode is
carried, since `--mode explore` uses Anthropic-only features.

```
export OPENAI_BASE_URL=https://llm-gateway.example.internal/v1
export OPENAI_API_KEY=...            # optional for a proxy that authenticates some other way
redline review --api openai --model gpt-5
```

`--base-url` wins over `OPENAI_BASE_URL`, and with the default provider it
overrides `ANTHROPIC_BASE_URL` the same way. A gateway that meters by
caller and wants the user in the bearer token gets it from `--api-user`
or `OPENAI_USER`: the header is then `Bearer user=<user>&key=<key>`
instead of the bare key. The endpoint has to accept a
streamed request with `response_format: json_schema`; one that drops the
schema constraint still works as long as the model returns the JSON,
fenced or not. A proxy that reports no token usage on the stream gets its
ledger line from the request-size estimate, and the command says so. A
model name the price table does not know is sent unpriced, which also
disables the `--max-cost` tripwire, and the command says that too.

### Context providers

A review is better when it can see past the diff: the whole enclosing
function, the callers of a changed signature, the type behind it, and the
history of the changed lines. Resolving that needs a language toolchain, so
it happens in a separate program.

Redline links none. A provider is found the way a linter is, by the config
file that says the repository opted in, and it is run as a subprocess that
prints a context envelope as JSON. Redline ranks and truncates that envelope
by role and priority against a fixed token ceiling, without reading the code
inside, so one budgeting implementation serves every language. A repository
with `.gorefactor.yaml` gets the Go provider with no configuration; anything
else is a `context:` entry in `.redline.yml`. The format is in
`docs/context-envelope.md`.

A provider that is missing or fails degrades to a gap: the review runs
without it, and the report says which context was absent, because a check
that did not run and one that came back clean look identical otherwise.

### The rules the repository wrote down

One kind of context needs no provider, no subprocess and no model, because
GitHub's convention fixes where it lives. Every run reads
`.github/copilot-instructions.md` and `.github/instructions/*.md` and carries
them into the review as `guideline` context.

A file in `.github/instructions/` states its own scope in frontmatter, and
that scope is honoured: `applyTo: "**/*.go"` reaches a review whose change
touches Go, and a rule scoped to `migrations/**` does not reach one that
touches no migration. A file with no `applyTo`, and the repository-wide
`copilot-instructions.md`, apply to every change. Frontmatter is metadata
about when to read the file, so it is not sent; the prose is.

The reason to read them at all is that a review contradicting the house rules
is wrong twice: the finding is wrong, and it is evidence the tool did not read
what the team wrote. The rules are told to the model as binding, and
explicitly not as a checklist to audit the change against, because a change
that follows the rules deserves no comment saying so.

`AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md` and their neighbours are read the
same way, at the root and in every directory on the way down to a changed
file. A rule at `internal/feed/AGENTS.md` governs a change under
`internal/feed` and reaches no other review, which is what `applyTo` says
explicitly and what the location says for a file whose author never wrote
one. The repository-wide ones are sent first, so a nested rule reads as the
narrowing it is. Six is the bound and the ones nearest the change are kept,
because those are the ones with something specific to say; what is dropped is
counted in the notes.

This is not a small gap being closed. Redline's own rules live in `AGENTS.md`
and it has no `copilot-instructions.md`, so before this every review of this
repository carried none of them.

The same filename in the package next door is read under the same role, when
the two directories share enough filenames to be the same thing built twice.
That is the parity pane's test, and it is what a review of
`offerfeedingest/workflow.go` needs when `accountfeedingest/workflow.go`
answered the same three questions a year ago. The file beside it is the
stronger claim and is tried first.

The file beside a changed one is read too, under the `sibling` role. Two files
in a directory whose names share enough of their parts are usually doing the
same kind of work, and the older one is the convention the newer one follows.
That convention is often written down nowhere: it is in the code, one
directory listing away. The match is a guess from a filename, the expansion
says so, and the model is told to read it as what this repository already does
rather than as a rule. A directory whose naming singles nothing out, twenty
files sharing one word, produces nothing at all.

`redline-scout` looks for all of these too, and judges which of them bear on
the change, which needs a model and is why it costs money and is off by
default. These paths are free.

### Rules a team states, and rules it learns

`.redline.yml` carries review rules of its own, scoped to paths:

```yaml
review:
  instructions:
    - scope: ["internal/api/**"]
      text: Handlers validate input at the boundary.
    - scope: ["internal/feed/**"]
      text: Bulk ingest mirrors aws_account_feed.go; the drift risk is accepted.
      learned_from: "https://github.com/o/r/pull/412#discussion_r1"
```

The second kind is the interesting one. When somebody replies to a finding to
say the thing is deliberate, they have stated a convention written down
nowhere else, and the next review needs it. `redline learnings` drafts those
entries from the threads a `--pr` run already read back, as YAML you paste
under `review:`.

It writes nothing itself. A list that grows from moments when somebody was
defending their own change will eventually acquire a line that is simply
wrong, and the finding people most want to ignore is sometimes the one they
most need. So a person reads each draft, cuts what is wrong, and commits the
rest like any other code.

`learned_from` is what marks a rule as a learning rather than a decision, and
three things follow from it. A learned rule ranks below one the team wrote, so
it is dropped first when the budget binds. The review is told the difference,
and told that a learned rule never outranks a file and never outranks the
diff. And it suppresses nothing by itself: it arrives as context, and a ruling
made on the strength of one has to say so.

Two providers ship here. `redline-scout` is the one that costs money: a cheap
model reads the diff, works out what this change in particular needs, and uses
a handful of tools to find it. A deleted guard pulls the history of those
lines. A struct whose fields moved pulls the migration that writes the row.

What it is worth paying for is what nothing else can see, so it is told what
is already covered. `--covered enclosing,caller,type,sibling,history` with
`--covered-scope "**/*.go"` says gorefactor resolves those roles for Go from a
type checker; the scout is told so in its opening turn and refused if it
records one anyway. Guessing at a caller list somebody else knows exactly is
the expensive way to be less correct. Redline also drops a second provider's
duplicate of a line range it already kept, so the same lines are never
budgeted twice however they were found.

It also reads what the repository wrote down about itself. `AGENTS.md`,
`CLAUDE.md`, `CONTRIBUTING.md` and the rest are found at the root and beside
the changed files, and short ones go into the scout's first turn rather than
costing it a round trip; design notes and decision records are there to be
listed and read. The lines that bear on the change come back under a
`guideline` role, quoted from the file that states them. This is aimed at one
particular bad review: the one that suggests a construction the house style
forbids, or undoes something a design doc explains. A finding that contradicts
the rules the team wrote is wrong twice, and it is the kind of wrong that
teaches people to stop reading. The reviewer is told they are repository
content and not instructions to it, so a file that says to approve everything
changes nothing.

It records
locations, never code: the program reads those bytes from the tree itself, so
the reviewer gets real source under a selection a model made, and a model's
recollection of a function can never reach the review looking like source. It
explores under a cost cap and its findings are frozen into the envelope, which
is what keeps the review a single call over a fixed payload while the
exploring happens at a fraction of the rate. Running out of budget is not a
failure: it stops, says so in the notes, and the review still happens. It is
off unless a repository names it, because `run` costing nothing is worth
keeping by default.

`redline-graphify-context` reads the graph
[Graphify](https://github.com/Graphify-Labs/graphify) writes at
`graphify-out/graph.json` and answers for every changed file rather than for
one language. It parses nothing itself. Its edges match by name, so
`--defer-callers` leaves the caller question to a provider that resolves it
through types, and the rest of the graph still comes through: the type behind
a change, the other implementations of an interface it touches, and the file
of another kind sitting next to it, which is how a migration reaches a review
of the code that writes the row. Only the tree-sitter half of the graph is
read; the half a model wrote is left out, because an envelope that cannot be
reproduced makes the eval measure noise. `.redline.yml.example` has the
entry, and the harness profile that keeps the graph from going stale
underneath a review.

## Posting

`post` submits one review (event `COMMENT` - it reports, it never requests
changes or approves) against the PR the session observed. The body opens
with a verdict derived from finding severities and an evidence table: one
line per pane saying whether it ran and what it found, the diff-coverage
number with its provenance, and how many changed files any pane examined. A
pane that applied and did not run is named there in bold, which is what
makes the review's scope verifiable from the pull request alone.

When a review has been written — by `redline review` or by hand into
`review.json` — the body also carries the agent's account of the change: what
it does, and a collapsed table of one line per file. That is what makes the
pull request readable without opening the report, and it is the part a
reviewer orients on before reading a single finding.

Both are labelled as the agent's words. Redline writes no prose itself, so a
run with no review has no such section rather than a heading with nothing
under it, and the file table lists only files someone actually wrote a
sentence about — GitHub's own Files tab already lists the paths and their
line counts. The prose is bounded so that it can never be the reason a
finding or a gate marker falls off the end of a body GitHub would reject.

Line comments are for what interrupts somebody usefully. A finding opens a
thread that has to be closed, so the reviewer's own `info` findings ride in
the review body instead, and a pane's `info` still gets its line: a
measurement is a fact about the change and the line is where the fact is. A
finding whose own wording disqualifies it, "acceptable but worth noting" on a
warning, is withheld the way an unsure one is.

A reviewer finding that said it was unsure is not posted, unless the checking
pass verified it. The report folds those away behind a fold, and the pull
request used to carry them as ordinary comments, so a guess the page hid
arrived on the change looking like a measurement. The exception is what the
checking pass is for: a finding it kept on a line quoted out of the material
it was shown has been checked since the confidence field was written, so the
evidence decides and not the doubt that came before it. Otherwise the two
readers agree, and the body says how many were held back so a reviewer that
withheld something can be told from one that had nothing to say. Only the
reviewer's own findings: a pane's finding carries no confidence at all, so
this can never withhold something that was measured.
A repository that would rather see them can set `body_include: low-confidence`,
which folds them into a collapsed block on the pull request instead of holding
them back; they still never open a line comment and never gate.

A finding becomes a line-anchored comment only when its `file:line` is on a
changed line in the PR's diff; findings off the diff (or with no line) go in
the review body, so one stray line can never make GitHub reject the whole
review. It refuses unless the loaded session is that same PR. Posting is
idempotent by finding across the pull request: every comment carries a hidden
fingerprint marker, and a finding already said on the PR is not said again on
any later commit, so a push never repeats it. A new finding still posts, and
the review body restates the verdict and evidence for the current commit, so a
still-live finding does not read as fixed rather than being said twice. `gh`
supplies the credentials; Redline never handles a token. `--report-url` links
the full report (e.g. a CI artifact) from the review body.

`--profile PATH` is how a repo's merge gate reads the review. The YAML names
the hidden markers, which severities fail (default: error and warning), and
whether posting is author-only and must match current PR HEAD. A pane that
applied but did not run fails the gate unless the profile says otherwise: a
missing check must not read as pass. `post` still uses event `COMMENT` and
never approves. A `pass` review with no blocking findings still posts, so a
later HEAD can clear a previous `fail`. Info findings stay visible but do
not get the gate's finding marker. See `redline-review.yml.example`.

`body_style` chooses the layout. `evidence` (the default) is the body above.
`walkthrough` reads like the author-published Copilot and Bugbot reviews:
reviewed-by and commit, the pull request's stated intent, what the change
does, then a collapsible walkthrough of every changed file, test files aside,
with the agent's one-line summary or "No notes." A finding that names one of
those files rides under it there; only a finding with no file lands in the
list after. `body_include` decides how much of the report rides along:
`coverage` and `lint` add per-file columns, `confirmations` and `unknowns`
fold in the report sections the body otherwise drops, and `low-confidence`
folds the reviewer's unsure findings behind a chevron rather than withholding
them (the one add-on that also applies to the evidence body). All of it comes
from the session `run` already wrote, so posting still observes nothing.

## The report server

`run` prints `Report: <url>` on stderr. That URL works in Cursor and
Claude; `file://` often does not. Read the port from that line rather than
assuming 8765: the server identifies itself, so a port already held by
another repository's report is skipped for the next free one, and a server
already serving *this* `.redline` is reused. It shuts down after 30 minutes
idle, or on `redline serve --stop`.

Serving and opening are best effort. If the port cannot be had or no
browser can be launched, `run` warns and still exits 0: the report is on
disk either way, and a completed run must not report failure.

## What never reaches the screen

Generated files leave the change before any pane or the report sees them,
so the coverage denominator counts files a reviewer would actually read.
Detection prefers what generators say about themselves: `git check-attr
linguist-generated`, then `Code generated ... DO NOT EDIT` and `@generated`
markers, then filenames only a generator produces.

Every exclusion is **named** on the report. Hiding a hand-written file is the
one way this can go wrong, and listing them is what makes that recoverable.

Test file contents are not rendered either; they are counted, and the
coverage number stands in for reading them.

A pane the repository has no files for is not mentioned. A report on a
repository with no migrations says nothing about migrations, one with no
API spec says nothing about the contract, and a change with no coverable
file gets no coverage section, because naming an absence the reader already
knows about is noise and it buries the line that matters: a pane that did
apply and could not run, which is always stated. A pane the repository does
have that this change did not reach is listed, folded away, so the reader
can tell "not touched" from "not checked". `findings.json` keeps every pane
with its state (`ran`, `skipped`, `failed`, `not-applicable`) so an agent
reading it knows which panes exist.

## What use taught it

Dogfooding rung 1 on its own repository changed three things, all the same
mistake in different clothes - a report that reads better than the facts
support:

- A change no pane covered rendered as `Findings: None` with "every applicable
  check ran". Reports now carry a `coverage` count and lead with a banner when
  Redline examined none of the change.
- A confirmation said "1 migration file is byte-identical" while its sibling
  was flagged as modified. Confirmations now require the whole set to hold;
  a partial result is an unknown with the denominator stated.
- Check 1 reported `blob a007a0ec → 933f33a1`. It now captures the unified SQL
  diff and shows it beside the finding.

A fourth lesson reshaped the tool itself. Redline once ran the agent's
review: reviewer adapters, a packet of facts for the agent to judge, a
context brief, a Copilot-shaped posted review built from agent prose. None
of it made reviews better than the agents produce on their own, and it was
cut. The agent came back on the other side of the line, as a reviewer that
reads `findings.json` and writes `review.json`, which Redline renders beside
its own facts. The reasoning and the removal inventory are in
`redline-design.md`.

## Layout

```
cmd/redline           CLI
cmd/redline-graphify-context
                      context provider: a Graphify graph to an envelope
cmd/redline-scout     context provider: a model chooses what the reviewer
                      needs; this program copies the bytes
internal/change       the change under review: files, diffs, classification;
                      generated-file detection
internal/cover        diff coverage from an existing profile
internal/boundary     the check that keeps a language toolchain out of here
internal/envelope     the context contract, and budgeting it to a ceiling
internal/eval         scoring a review against annotated fixtures, offline
internal/findings     wire format: doctor's schema, reimplemented and extended
internal/gitx         git layer (observe; fetch/worktrees for PR/branch)
internal/graphify     the graph provider's own half: Graphify's schema to
                      roles, and nothing else in here may import it
internal/scout        the scout provider's own half: the tool loop, the cost
                      governor, and the rule that content comes from the tree
internal/pane         the observe/diff pane interface
internal/pane/lint         lint delta, suppression triage, config drift
internal/pane/migrations   migration hygiene
internal/pane/openapi      contract breaking-change diff
internal/pane/parity       capabilities added to one of a set of parallel
                           implementations and not its siblings
internal/post         the PR review payload and merge-gate profile
internal/provider     finding and running language context providers
internal/review       the one model call: prompt, schema, cost
internal/report       markdown and self-contained HTML
internal/run          dispatcher
internal/target       working tree, branch, or PR
skills/redline        the agent skill that drives the binary
skills/redline-setup  the skill that wires a repository's tools into it
```
