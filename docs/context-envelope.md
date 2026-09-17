# The context envelope

The wire format between Redline and a language provider. Redline owns this
schema; a provider fills it in.

Redline links no language toolchain. Resolving what a change touched — which
symbols moved, who calls them, which types a signature names — needs one, so
that work happens in a separate program that Redline runs as a subprocess and
reads JSON from. This file is the whole interface.

The split is language-specific versus language-agnostic. It is not
deterministic versus probabilistic; that second split does not exist here,
because a provider is free to use a model of its own to fill an envelope.

One shipped provider does. `redline-scout` reads the diff, decides what this
particular change needs, fetches it with tools, and records where it is; the
program then copies those bytes out of the tree, so the content is source and
only the selection is a judgement. It also quotes the repository's own rules
under a `guideline` role, which is the case worth knowing about here: an
expansion may carry text a repository wrote rather than code, and the
reviewer is told it is content rather than instruction, for the same reason
`promptFragment` is framed as advice. Two consequences are worth stating where a
provider author will read them. Its envelope is not byte-identical between
runs, so `provider.version` carries the model and the effort rather than a
release, and a fixture that freezes a scouted session freezes one run of it.
And because `run` executes providers, a repository that configures a
model-backed provider has made `run` cost money. That is why nothing turns
one on by default: the opt-in is a named entry in the repository's own
`.redline.yml`, and the subprocess boundary is what keeps it explicit.

## What each side owns

| Concern | Redline | Provider |
|---|---|---|
| This schema | owns | fills |
| Expansion roles vocabulary | owns | tags |
| Ranking and token budgeting | owns | supplies priority hints |
| Which classes of context a review pays for | owns | emits regardless |
| Symbol resolution, callers, types | never | owns |
| Generated files: gitattributes, path patterns | owns | |
| Generated files: language conventions, regeneration | | owns |
| Harness prompt: schema, output rules, silence | owns | |
| Language prompt fragment | carries as data | authors |

## Invocation

Redline runs the provider's command in the tree under review, with
`{{base}}` in the arguments replaced by the merge-base SHA. The provider
writes one envelope as JSON to stdout and exits 0. Anything on stderr is
kept as diagnostic text and shown when the provider fails.

A provider that wraps every command's payload in an `{"ok": bool, "error":
string, "data": {...}}` result is unwrapped automatically: Redline reads
`data` as the envelope when those keys are present at the top level. Both
shapes are accepted so a provider need not break its own CLI contract to
serve this one.

The same revision must produce a byte-identical envelope on every run. An
eval that cannot reproduce its own input is measuring noise.

## Shape

```json
{
  "schemaVersion": 1,
  "provider": {"name": "gorefactor", "version": "v0.13.0", "language": "go"},
  "baseSHA": "9fceb02...",
  "files": [
    {"path": "internal/store/user.go", "class": "source",
     "symbols": ["Store.Insert", "Store.lookup"]},
    {"path": "internal/store/user.pb.go", "class": "generated", "generated": true}
  ],
  "expansions": [
    {"role": "enclosing", "priority": 90,
     "symbol": "Store.Insert", "scope": "internal/store.Store.Insert",
     "file": "internal/store/user.go", "startLine": 44, "endLine": 71,
     "content": "func (s *Store) Insert(...) error {\n\t...\n}\n"}
  ],
  "promptFragment": "Reviewing Go: ...",
  "notes": ["3 package(s) failed to type-check; their callers were not resolved"]
}
```

### `provider`

`name` and `version` are required. The version is not decoration: the
language half of the review knowledge lives in the provider's repository and
is consumed in Redline's, so a frozen fixture has to be able to say which
provider wrote it.

### `files`

The manifest: every changed file, classified. `class` is one of `source`,
`generated`, `vendored`, `test`, `migration`, `lockfile`, `other`.

`generated` is the answer to "should a reviewer read this", regardless of
which side decided. Redline decides the language-agnostic layers
(`git check-attr linguist-generated`, then path patterns); the provider
decides the language conventions (a `Code generated ... DO NOT EDIT` header,
`*.pb.go`, and so on). Set the flag if either applies. Generated files are
summarized in the context, never dropped silently and never pasted in full.

`symbols` are opaque strings. Redline groups and displays them and never
parses one.

### `expansions`

Context beyond the diff. `role` is the vocabulary Redline ranks by:

| Role | What it is |
|---|---|
| `enclosing` | The full declaration a changed hunk sits inside — the whole function, not the hunk |
| `caller` | A declaration outside the change that reaches a changed symbol, carried whole. `details.calls` names what it reaches |
| `callee` | What a changed declaration calls: the other half of a contract defect, and the half no caller shows |
| `removal` | History of lines the change deletes: the commits that added them |
| `type` | The definition of a type named in a changed signature |
| `sibling` | Another implementation of an interface the change touches |
| `test` | A test covering a changed symbol |
| `history` | Prior history of the changed lines |
| `indirect-caller` | A caller of a caller: the second hop out from a changed symbol. Ranks last, below history, because it is whole functions that may have nothing to do with the change |

Roles are ranked in that order, and the order is Redline's decision. A
provider cannot promote a test above an enclosing declaration; `priority`
orders expansions *within* one role and does nothing else. Higher is more
valuable. A role Redline does not know ranks after every role it does, is
kept when it fits, and is named in the report when it does not.

`history` and `removal` carry commit history rather than source, so the diff
never makes one redundant and neither makes the lines it names redundant for
anything else. `removal` is ranked apart from `history` so a deletion's
history is kept ahead of the older history of lines that survive.

Before any of that, Redline decides whether a class of context is wanted at
all. Today one class is not: test code. A `test` expansion, and any expansion
whose file is test material by path, is held back and counted, because the
review is told not to comment on test adequacy and the panes measure it
anyway. The exception is a change that touches nothing but tests, where the
tests are the change and everything is sent. Keep emitting `test` expansions:
the role stays in the vocabulary, the decision is Redline's per review, and a
provider that stopped emitting them would take the choice away.

`symbol` and `scope` are opaque strings. There is no package field, and
nothing here may be language-shaped: anything that only makes sense for one
language goes in `details`, a flat string map Redline passes through and
renders generically.

Emit expansions untruncated. Ranking and truncation are Redline's, against a
fixed token ceiling, using role and priority alone — it never reads
`content`. That is what lets one budgeting implementation serve every
language, so a provider that truncates to its own budget is throwing away
context Redline may well have had room for.

### `promptFragment`

The language half of the review prompt: how this language's code is
conventionally reviewed, what its error handling looks like, which of its
idioms are load-bearing. Redline supplies the harness half — the output
schema, the instruction not to restate prior findings, that zero findings is
a valid result — and concatenates this as opaque data.

Keeping the language half here is what stops Redline acquiring Go knowledge.
The cost, worth naming: Go review knowledge ends up next to the provider's
own rules rather than next to the harness, encoded in one repository and
consumed in another. That is why `provider.version` exists.

### What the envelope does not carry

Deterministic findings, coverage and mutation are Redline's own output, not a
provider's, and they reach the review through the prompt rather than through
here. A provider that tried to supply them would be asserting facts about a
change it did not measure. Keep to what a provider resolves: the context
around the change, and what it could not resolve.

### `notes`

Things the provider could not determine. They reach the report as unknowns.
A context pack that silently covers half a change reads exactly like one that
covers all of it.

## Configuring a provider

Providers are found the way linters are: by the config file that says the
repository opted in, plus anything declared in `.redline.yml`. Redline has no
list of provider names in a code path.

```yaml
context:
  - name: gorefactor
    command: gorefactor
    args: ["context", "--changed", "{{base}}", "--json"]
    scope: ["**/*.go"]
```

`scope` is the globs this provider speaks for. A changed file outside every
provider's scope is reported as unexamined by the context layer, the same way
the lint pane reports a file no tool covers.

A provider runs as a subprocess whose working directory is the root of the
tree under review. That is the caller's own checkout when it already sits on
the reviewed revision with nothing modified, and a detached worktree at the
head commit otherwise, so a provider must not assume the user's checkout and
must not rely on anything git does not track: a detached worktree has no
`node_modules`, no build output, and none of the artifacts a harness produces
unless `--prepare` builds them. The working directory is the top of the tree,
not wherever a language keeps its project files. A provider whose language
configures itself per directory — a `tsconfig.json` under `ui/`, a `go.mod` in
a nested module — has to find that configuration, for instance by walking up
from each changed file; searching upward from the working directory finds
nothing below it. `{{base}}` is replaced with the merge-base SHA. A provider
still running after five minutes is stopped and reported as failed.
