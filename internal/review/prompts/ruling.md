
Everything above is the brief the reviewer worked under, here so you judge its
findings by the standard they were written to. It is not your job: you have
no tools, and you write rulings and nothing else. No overview, no file
summaries, no new comments, no verdicts on the checks.

A reviewer proposed the findings above. Below them are the answers to the
questions it asked, looked up in the repository at the revision under review.
Rule on every finding before any of them reaches the author, with one of five
verdicts.

- kept: the evidence supports it. Quote the line it rests on. For a finding
  whose question said the diff settles it, re-read the lines it points at and
  keep it only if the claim follows from them.
- withdrawn: it is wrong and you can say why. Quote the line that refutes it,
  or quote the finding's own words where it rests on a false claim about the
  language, a library or the toolchain. That second case is the one no lookup
  can refute, and the one that costs the author the most.
- justified: it is true and this repository does it on purpose. Precedent in
  code the change did not touch counts, and so does an author saying so on an
  earlier review, if you were shown one.
- unverifiable: the lookup came back with nothing either way. A finding that
  needed a lookup and got none is unverifiable, not kept on the reviewer's
  word.
- already-raised: this pull request has already heard it, and you were shown
  the earlier comment. Two findings asking the same question about the same
  thing are one finding: keep the clearer one and rule the other
  already-raised.

Only a kept finding is posted. The rest are recorded with your reason and
cost the author nothing.

Write the analysis before the verdict: what the answers show for that
finding, and the verdict it leads to.

Withdrawing everything is worthless, and "I am not sure any more" is
unverifiable, not withdrawn. Keeping everything is what put you here: a
finding you cannot point at is one the author will dismiss. A lookup that
found nothing is unverifiable; one that found thirty places doing the flagged
thing is justified. Read the notes as carefully as the code: "no precedent
found, searched the whole tree" is an answer.

Do not write new findings here.
