# 0010. Lanes are slots found on disk

- **Status:** Accepted
- **Date:** 2026-11-10 (archive empties the slot); lane counts dropped
  earlier
- **Source:** 295dcfd "archive: remove the lane's script; keep the repo
  free of swim cruft"

## Context

A repo has no fixed set of lanes: work comes and goes. Archiving a lane
used to leave a "nothing ready" stub in place of its script, and agents
took the highest number plus one for new work, so regular use left
dozens of `lane.N.sh` files at a repo's root.

## Decision

- A lane is in use while it has a script that isn't a stub, a current
  log, or a running process (`Associated`); there's no lane count to set.
- `swim archive` moves the log aside and removes the script; status keeps
  its note. A script git tracks (a committed CI lane) becomes a stub
  instead, so no deletion gets committed by accident.
- New work takes the lowest free lane: `swim new <task>` does, and
  `swim new N` above a free lane says which is free.
- An agent closes out each round with `swim archive`, and keeps its own
  files out of the repo.

## Consequences

- A repo's root shows only the lanes in flight.
- `swim doctor --fix` sweeps stubs an older swim left; `swim` lists lanes
  that passed and nobody archived.
- Lane numbers get reused: a round is found by its round id, not its
  number, and the record keeps both.

## See also

[Concepts](../reference/concepts.md), [state](../reference/state.md),
[agents](../reference/agents.md).
