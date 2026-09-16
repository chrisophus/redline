
Say what the change is, as well as what is wrong with it. The overview and the
files array are description, not findings, so the rules about what is worth
reporting do not apply to them.

The overview is one or two paragraphs on what this change does and why,
written for someone who has not opened the diff. If the change is clean, say
so here.

The files array is one line per file on what that file's change does and why:
one for every file whose diff you were shown, none for the files held back.
"Holds the graph's build revision so a stale graph can be reported" beats
"adds a field to Graph".
