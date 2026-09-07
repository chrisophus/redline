# Report roadmap

Where the report is going, and why. Redline measures a change and states facts.
It never calls a model. The agent writes prose and judgment out of band, drops a
JSON file, and Redline renders it beside the instrument's facts. Every item here
rides that one boundary: the agent writes a file, Redline merges and renders,
and the interactive requests are payloads a human copies to the agent.

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

Redline composes none of this. It renders what the agent wrote, attributed to the
agent. No model call enters the tool.

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

## Now: see coverage and click through

The coverage overlay stripes each changed head line green (a test ran it), amber
(none did), or nothing (not coverable). Two things are missing.

- The uncovered-files list in the Coverage section is not clickable. Make each
  entry open the drawer for that file at its first uncovered line.
- Click a line, see the tests that cover it. The profile knows covered from
  uncovered but not which test ran a line. That needs per-test coverage: run the
  suite per test or per package with `-coverprofile` and map tests to blocks.
  This is the same data mutation testing needs, so it is worth building once.

## Later: mutation testing

Run gremlins to find lines where no test fails when the line is mutated: covered
but not actually asserted. Mark those lines in the same per-line view. This is the
last layer of the unified line: changed, linted, covered, asserted.

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
- `untested-function` is not fully redundant with the coverage pane. Coverage is
  diff percentage; untested-function is whether a function has any test at all.
  Fixable if scoped to changed functions with repo-relative paths. It needs
  gorefactor to emit repo-relative paths for the untested-* rules first.

## gorefactor deficiencies that block the unified view

- The smells family and duplicate-block report no node position, so their
  findings have no line number. Redline cannot place them on a line.
- The untested-* rules emit module-qualified paths. Redline strips the module
  prefix through go.mod, but a repo-relative path from gorefactor would be
  cleaner.
