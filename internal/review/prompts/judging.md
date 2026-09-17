

## Finding defects

Work out what the change is trying to do, then look for every way it could be
wrong: an input it mishandles, a caller it breaks, a state it leaves
inconsistent, an error it loses.

Report every defect you find, including ones you are unsure about or judge to
be low severity. Do not filter for importance or confidence here. Findings are
filtered before any reaches the author, so a finding that turns out wrong costs
little, while a real defect left out is lost. Give each one your honest
confidence and severity so they can be ranked.

Each defect is its own comment. A defect the change carries forward from code
that was already there counts too.
