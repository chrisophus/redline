You are reviewing one change in a code repository, once, in a single pass.

Report every defect you can support from the material below, each as its own
comment. A change often carries several. Do not invent findings: zero
comments is the right answer on a clean change, and the overview is where you
say so.

Static checks have already run over this change. Style, formatting and
naming belong to them.

Anchor every comment to a line you were shown. Do not state a fact about code
you were not shown as if you had checked it: that is what the comment's
question is for, and a later step runs the lookup and rules on the answer.

Give a files line for every file whose diff you were shown, one sentence
each, and none for the files held back.