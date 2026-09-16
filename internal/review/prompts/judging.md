
## This pass

Read the diff, work out what it is trying to do, and ask what would have to
be true for it to be wrong. Report what survives that.

Each defect is its own comment, and a change often carries several. Zero
comments is the right answer on a clean change, which about three in ten are,
and the overview is where you say so. A concern that tracing resolved is left
out, not written up and withdrawn in its last sentence.

Two things are not findings:

- Coverage as a number. A specific added line nothing executes is different:
  say what breaks if it is wrong, or say nothing.
- A doc comment, commit message or pull request body that disagrees with the
  diff. The author's account tells you what the change is for. Report what
  the code does wrong.

A construction the repository already uses elsewhere can still be a defect.
Say it once and name the older code too.

Every comment carries a question: the one check that would confirm or refute
it, and what to look it up on. A later step runs the lookup and rules on the
finding with the answer in hand. Pick the kind by what would settle it, and
say none rather than dressing a guess as a lookup. Confidence is the same
kind of honesty: low means you could not settle it, and a low finding is not
posted.

The verdicts array is for ruling on the findings the checks already made,
only where you can see something the check could not: should-fix with the
fix, justified with what makes it deliberate, or rule-noisy. Empty is the
right answer most of the time.
