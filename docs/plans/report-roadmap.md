# Report roadmap

Where the report is going, and why. Redline measures a change and states facts.
`run` calls no model. The judgment arrives out of band as a JSON file, from an
agent or from `redline review`, the one command that does call a model, and
Redline renders it beside the instrument's facts. Every item here rides that
one boundary: something writes a review file, Redline merges and renders, and
the interactive requests are payloads a human copies to the agent.

The through line is one view of a changed line that shows everything known about
it at once: is it changed, does a linter flag it, does a test run it, does the
agent have something to say about it, and could a test have caught a bug on it.

## Shipped: agent review comes into the report

The agent already produces a review file shaped like a GitHub Copilot review: an
overview of the change, a per-file summary, and comments on specific lines. Fold
all of it into the report through `review.json`, the file the verdict layer
already reads.

- `overview` renders as a Review section at the top of the report.
- `files` renders as a one-line summary on each file in the drill-in.
- `comments` become findings marked `source: llm`, so they sit on the diff line
  in the drawer with the coverage stripe and the line highlight, next to
  Redline's own findings.
- Each verdict gains a `fix` field: the agent's recommendation for how to fix the
  finding, not only whether it should be fixed.

Redline renders what the agent wrote, attributed to the agent. No model call
enters the tool.

## Shipped: ask the agent from the report

Each diff line and each finding has Explain and Apply. Clicking one queues a
request that carries an action onto the same list the Copy button hands over,
now labelled "Copy for the agent". The payload item gains an `action` field:
`comment` is feedback to address, `explain` asks for an explanation without a
code change, `apply` asks the agent to make the change and to apply the verdict's
`fix` when there is one.

Redline is a static file with no model behind it, so the loop is copy and paste,
the same as the review comments. `redline serve` is the upgrade path if a live
loop is worth a running process later. This is the prequel affordance minus the
server.

## Shipped: jump from a coverage gap to the code

The coverage overlay stripes each changed head line green (a test ran it), amber
(none did), or nothing (not coverable). The uncovered-files list in the Coverage
section is now clickable: each entry opens the drawer for that file at its first
uncovered line, already amber and highlighted.

## Next: click a line, see the tests that cover it

The profile knows covered from uncovered but not which test ran a line. Showing
the tests behind a line needs per-test coverage: run the suite per test, or per
package with a narrowed `-run`, and map tests to blocks.

This one fights a design principle. cover.go reads a profile the repo already has
rather than running the suite, on purpose: tests can need a database or minutes of
wall time, and a tool that silently runs your suite before every push is not one
you reach for. Per-test coverage means running tests, many times. So it cannot be
the default `run` behavior. Options to decide before building: an opt-in flag
(`redline cover --per-test`) that writes a per-line-to-tests map the report reads,
a separate command, or leaving it to the agent to produce the map as another
ingested file. This is the same data mutation testing needs, so build it once.

## Shipped: mutation testing marks covered-but-unasserted lines

redline reads a gomutants (github.com/szhekpisov/gomutants) report, `mutants.json`,
the same way it reads a coverage profile: off disk, produced by the developer or
CI, never run during `redline run`. It scopes the report to the lines the change
adds and renders a Mutation section plus a red right-edge marker in the drawer on
each line where a mutant survived, a test runs the line but nothing fails when it
is changed. Coverage says a test ran the line; mutation says whether a test would
catch it breaking. `make mutants` produces the report. The survivors are in
findings.json under `mutation`, so the agent can act on them.

**The v0.6.0 statuses, also shipped.** A report from v0.6.0 or later carries a
stable mutant `id` and an **`INFRA_ERROR`** status for a test run that failed
for a reason of its own (OOM, disk, open files). Redline counts those apart,
names the lines they sit on as an unknown, and withholds the all-killed
confirmation while any are present: a runner that fell over is not a suite that
caught the break. **EQUIVALENT** is counted apart from **LIVED** and rendered
as a muted note, because a survivor no test can kill is not a gap. The `id` is
the verdict key when the report has one, so a verdict survives a rebase that
moves the line, and each survivor carries the `gomutants --run-mutant-id`
command that re-runs it.

This is most of the unified line: changed, linted, covered, asserted. What is
still missing is naming the exact tests behind a line, which neither the coverage
profile nor the gomutants report exposes (see the section above).

## Shipped: test code is not in the review request

The review is told not to comment on test adequacy, and the coverage and
mutation panes measure it. So the request names the changed test files with how
many lines moved in each and leaves their bodies out, the same bargain
generated files get, and a `test` expansion from a context provider is held
back before anything competes for the budget. What was withheld is counted and
printed, because an exclusion nobody can see reads as a change with no tests. A
change that touches nothing but tests is the exception: there the tests are the
change and they are sent.

## Backlog

Ideas that are real but not scheduled.

- What it looks like: the interface section is a permanent placeholder that only
  ever renders a gap. Options: agent-supplied route screenshots, a deterministic
  list of what UI moved, or drop the section. No decision yet.
- Verdicts for more finding classes: coverage-gap as risk or acceptable,
  config-drift as reasonable or hiding something. More skill questions and
  vocabulary, no new machinery.
- Scope the suppression and lint panes out of `_test.go` files, or allow a
  per-path exclusion. Redline's own suppressions there are fuzz seeds and
  fixtures, and they read as real findings today.
- Broader agent-context skill: why a lint was ignored, whether it is fixable,
  whether the rule is noisy.
- A standing code graph beside the per-change envelope, for the neighbors a Go
  type checker cannot see: a migration next to the struct that writes the row.
  Worked out in [graph-context.md](graph-context.md), not scheduled.
- `untested-function` is not fully redundant with the coverage pane. Coverage is
  diff percentage; untested-function is whether a function has any test at all.
  Fixable if scoped to changed functions with repo-relative paths. It needs
  gorefactor to emit repo-relative paths for the untested-* rules first.
- ~~`redline/context` unknown wording overstates the failure.~~ Fixed: a
  provider's notes now collapse into one unknown whose message leads with what
  it did resolve ("gorefactor resolved 386 expansion(s) (282 enclosing, 36
  test, 28 history, 26 caller, 14 type) and reported 3 gap(s)"), with the notes
  themselves as the reason. On this repository's own change that took seven
  failure-shaped entries down to two that read as what they are. Original note
  kept below for the reasoning.

  `redline/context` unknown wording overstates the failure. On MKT-1415 the
  `unknowns` entry read "gorefactor could not fully resolve this change... no
  sibling expansions: no changed type implements an interface declared in this
  module" — read alone in the report this sounds like context-gathering failed.
  The actual envelope held 364 real expansions (206 test, 71 caller, 61
  enclosing, 12 type, 14 history); only the interface-sibling check came up
  empty, because that repo's provider code is concrete siblings, not interface
  implementers. Report the expansion counts by role alongside the unknown so
  "364 expansions gathered; 0 interface-sibling matches" replaces the blanket
  "could not fully resolve" — otherwise a reviewer reasonably distrusts a pane
  that is actually working.

## gorefactor deficiencies that block the unified view

- The smells family and duplicate-block report no node position, so their
  findings have no line number. Redline cannot place them on a line.
- The untested-* rules emit module-qualified paths. Redline strips the module
  prefix through go.mod, but a repo-relative path from gorefactor would be
  cleaner.
