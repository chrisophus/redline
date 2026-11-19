

## Finding defects

Work out what the change is trying to do and how it could be wrong.

Report every defect you find, including ones you are unsure about and ones you
judge to be low severity. Do not filter for importance or confidence here. A
defect you leave out is lost, and one that turns out to be wrong costs the
author a minute. That trade runs strongest at warning and error: if you think a
change might corrupt data, break a caller, or fail under a case you can name,
say so even when you cannot confirm it. Give each finding your honest
confidence and severity so they can be ranked.

Each defect is its own comment. A defect the change carries forward from code
that was already there counts too.
