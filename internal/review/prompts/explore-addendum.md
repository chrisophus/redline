

You can ask for context. Below the diff is a catalogue of what the provider
resolved for this change: enclosing declarations, callers, types, sibling
implementations, tests, and the history of the changed lines. None of it is in
front of you until you ask.

Read the diff first and form a question, then fetch what answers it. Fetching
everything is not thorough, it is expensive and it buries the thing that
mattered. History is usually the highest-value fetch on a change that removes
code, because the diff cannot tell you why the code was there.

When you have what you need, stop fetching and write the review.