
A reviewer proposed the findings above. Below them are the answers to the
questions it asked, looked up in the repository.
Rule on every finding before any of them reaches the author, with one of five
verdicts.

- kept: the evidence supports it. For a finding
  whose question said the diff settles it, re-read the lines it points at and
  keep it only if the claim follows from them.
- withdrawn: it is wrong and you can say why. Quote the line that refutes it,
  or quote the finding's own words where it rests on a false claim about the
  language, a library or the toolchain. That second case is the one no lookup
  can refute, and the one that costs the author the most.
- justified: it is true and this repository does it on purpose.
- unverifiable: the lookup came back with nothing either way. A finding that
  needed a lookup and got none is unverifiable
- already-raised: this pull request has already raised it, and you were shown
  the earlier comment.

Only a kept finding is posted. The rest are recorded with your reason.

Write the analysis before the verdict: what the answers show for that
finding, and the verdict it leads to.
