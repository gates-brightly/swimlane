# T33. Failures that don't reach the record (P1, gap)

Source: the claude module's test rounds and the CI lanes on 2026-10-16.

swim's value is its record: the lane logs, `swim status`, `.swim.log`, the
chain. Some failures never reach it. They show only on the terminal that
ran swim, which is often a worker's or nobody's: errors before a round
starts, failures outside steps, and refusal reasons. A tool whose value is
its record should never have a failure only a terminal saw.

## Seen

- **Before the round starts.** On 2026-10-16 the repo's `.swim.lock` moved
  to script syntax 4 while the Mac's worker ran a swim reading syntax 3.
  Every job of lane 1 refused to start (`swim _start` refused the lock),
  and all `swim status` recorded was "the cell's script exited before its
  round started" (`internal/launcher/matrix.go:411`). The cause, "this repo
  needs a newer swim", went to the worker's stderr. Lanes 2-6 were then
  skipped as "swim 1 failed". It took reading the lock by hand to find it.
- **Outside a step.** Round 1 of lane 7 ran on a swim without modules:
  `mod` wasn't a function, bash printed "command not found" to the
  terminal, and the log showed only the guard approved, then nothing.
  Since `31a16a5` a failure outside a step is a WARN in the log (an ERR
  trap in `lane_init`). Not covered: failures before `lane_init`
  (`swim lib` failing, a bad `_swim_lib` line), in a handler, or in a lane
  that doesn't use the library (a wrapped program's output outside `swim
  run` is logged as one step only if it never calls swim).
- **Refusal reasons on the worker's terminal.** `swim watch` prints why it
  can't run a batch, or why a lane was refused, to its own output: a
  headless or backgrounded worker loses it.

## Do

- **Every exit path of a lane leaves a reason** in its status (`reason:`)
  and its log: what swim was doing and the error's first line. A lane
  process that exits before its round header is written gets a short
  round of its own in the log ("== ROUND ... not started: <why>"), so
  `swim log N` and `swim report` show it.
- **Capture what the lane printed before its round started** (stderr up to
  `lane_init`'s `_start`) and keep its last lines with the reason.
- **Refusals swim makes for a batch or a run** (a lock mismatch, a lane it
  can't stage, a worker that can't take a batch) go to `.swim.log` and the
  batch's queue entry, not only the terminal.
- **`swim watch` refuses up front** when the repo's lock needs a newer
  swim, instead of failing each job (see also
  [features/module-versions.md](../features/module-versions.md)).
- Extend the ERR-trap WARN to handlers, and say in the guide where a
  lane's output outside steps goes.

## Done when

e2e tests, each read back with `swim status`, `swim log N` and `swim
report`, never the terminal:
- a lane refused by a lock mismatch says so, with the fix;
- a lane whose `lane_init` fails (no swim on PATH, a broken library line)
  has a reason and its last stderr lines;
- a batch a worker couldn't run says why in its queue entry and
  `.swim.log`;
- a failure outside a step, in a handler, is a WARN.
