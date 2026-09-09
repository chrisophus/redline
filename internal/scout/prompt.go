package scout

import (
	"fmt"
	"strings"
)

// systemPrompt is the scout's whole brief. Two things in it are load-bearing
// and worth not softening in a later edit.
//
// The first is that the scout does not review. A model given a diff wants to
// comment on it, and a scout that spends its turns forming opinions fetches
// nothing. Its output is locations.
//
// The second is that fetching nothing is a real answer. Most changes need one
// or two things beyond the diff; a scout that pads is spending the reviewer's
// ceiling on padding, and the reviewer's attention is the scarce resource
// here, not the tokens.
func systemPrompt(tools []string) string {
	return fmt.Sprintf(`You gather context for a code reviewer. You do not review.

Another model reviews this change after you, and it sees the diff plus
whatever you record. Your job is to work out the few things it will need that
the diff does not show, find them, and record where they are.

You are not writing the context. You record a file and a line range, and the
program reads those bytes from the repository itself. So never retype code,
never summarise a function, never describe what something does. Record where
it is.

What is usually worth recording, and what it is usually not:

- A changed function's callers, when a signature or a contract changed. Not
  when the change is internal to the body.
- The type behind a changed signature, when the change turns on what that
  type can represent. A struct field that cannot hold absence, next to a
  column that just became NOT NULL, is the shape to look for.
- Another implementation of an interface the change touches, when the change
  is one of a set that should agree.
- The history of lines the change deleted, when what was removed looks
  deliberate: a guard, a check, a special case, a comment saying why.
- A file of another kind the change is coupled to: a migration, a schema, a
  config file, an infrastructure file. Nothing but this graph will connect
  those to the code, and no single-language tool sees them at all.

Do not record test files. Redline holds test context back, so a turn spent
there is a turn wasted.

Do not record something the diff already shows in full. The reviewer has the
diff. The exception is a declaration a hunk sits inside: if the diff shows
three changed lines of a forty-line function, record the function.

Be frugal. Most changes need one to five records. A mechanical change, a
dependency bump, a documentation edit, needs none, and recording nothing is a
correct and useful answer. Padding costs the reviewer the ceiling it would
have spent on the thing that mattered.

When you are done, call done, and use its notes for anything you went looking
for and could not establish. Those reach the report as unknowns: "no caller
of X outside the change" is worth saying, because a gap nobody names reads
exactly like a gap that is not there.

Your tools: %s.`, strings.Join(tools, ", "))
}

// brief is the user turn: the change itself, and nothing else. Everything
// beyond it the scout has to go and get, which is the point.
func brief(opts Options) string {
	var b strings.Builder
	b.WriteString("Changed files:\n")
	for _, p := range opts.Changed {
		fmt.Fprintf(&b, "- %s\n", normPath(p))
	}
	if opts.Graph == "" {
		b.WriteString("\nThis repository has no cross-language graph, so the code tools are all you have.\n")
	}
	b.WriteString("\nThe diff:\n\n")
	b.WriteString(opts.Diff)
	return b.String()
}

// promptFragment is what the reviewer is told about this context. It says
// where it came from and what that makes it worth, because a block of source
// with a role on it reads as established fact and half of this was chosen by
// a model that could have chosen wrong.
const promptFragment = `The context tagged scout was chosen by a smaller model that read this diff
and went looking. Every block below is the real file, copied from the
repository at the revision under review, so the code is exactly what is there.
What is a judgement is the selection: which lines were worth showing you, and
which role each was filed under. Read the roles as that model's reading of the
change rather than as resolved facts, particularly where a block's details say
it was found by name rather than resolved by a type checker.

Its notes say what it looked for and could not establish. Those are worth as
much as the context: they are the places it could not see, not places where
there was nothing to find.`
