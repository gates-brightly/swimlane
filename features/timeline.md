# swim timeline

Status: proposed

## Summary

`swim timeline [<run>|N|JOB]` draws when each lane ran in a run (the last one
by default). It shows how long each lane waited after its parents finished,
and the longest dependency chain. All of it comes from data swim already
records, so it works for any past run, not just a live one.

```
$ swim timeline
run r-20261009T141118Z-7c2e · 99 lanes · 7.67s · longest chain 5.65s (overhead 2.02s)

swim  1 |█▌                                     | 0.00 → 0.30  
swim  3 |  ███████▉                             | 0.31 → 1.60  after 1         ◆ longest chain
swim  4 |           █                           | 1.61 → 1.70  after 2 3       ◆
swim  8 |██▏                                    | 0.00 → 0.42  (root)
swim 60 |  ███                                  | 0.33 → 0.80  after 22
…
longest chain: 1 → 3 → 4 → 9 → 19 → 23 → … → 37 → 70 → 99
start delay after last parent: median 0.06s · p95 0.17s · max 0.34s (swim 38)
waiting on dependencies: 61% of lane-time · queued: 0% · lock waits: 0%
```

## Motivation

The dag99 audit (lane 99) computes this inside a test lane, because there was
no other way to see it:

- the time each lane started after its parents finished
- the longest dependency chain, against how long the whole run took
- a text timeline

The questions it answers are ones every operator has: why did this round take
20 minutes, which lane held everything up, and was the scheduler slow or the
work?

## Behaviour

- **Which run:**
  - no argument means the last run
  - a run id (see [run-id.md](run-id.md)) picks one run
  - a lane or job shows that lane's rounds across runs, one bar per round
- **Bars:**
  - they cover the time from start to finish
  - waiting on dependencies (`░`), being queued (`▒`, see
    [parallel-limit.md](parallel-limit.md)) and waiting on locks (`▓`, see
    [resource-locks.md](resource-locks.md)) are drawn before the bar where
    they apply
  - failed lanes are red, skipped ones are dim, and lanes on the longest chain
    are marked `◆`
- **Long runs:** they collapse to the slowest N lanes plus the longest chain
  with `--top N`. `--all` shows every lane.
- **Steps:** `--steps` breaks a lane's bar into its steps, or its stages, when
  the round uses `stage`.
- **Other formats:**
  - `--yaml` emits the data: per-lane times, waits, the longest chain and its
    totals
  - `--html FILE` writes a self-contained HTML Gantt chart, for sharing after
    an incident

## Design

- **Where the data comes from:**
  - `ROUND START` and `SUMMARY` lines in each lane's log
  - step timestamps
  - the run's `.swim.log` events (`start`, `pass`/`fail`, `skip`, plus the new
    `queued`/`lock` events where those features exist)
  - each lane's `# After:` line from the script as it was. The run records the
    dependencies it resolved, so a later edit to a script can't change an old
    timeline. That needs a `deps` detail on the `start` event.
- **No new runtime instrumentation**, other than recording resolved
  dependencies and the run id.
- **The longest chain:** a longest-path pass over the dependency graph,
  weighted by each lane's own duration (start to finish). Overhead is the
  run's total time minus that chain's length. It's the same calculation as
  `e2etool audit`, which then shrinks to checking correctness and calling
  `swim timeline --yaml` for the numbers.
- **Rendering:** reuses `internal/display` helpers (width, ANSI handling)
  for the text output. The HTML output is a Go `html/template` with inline
  SVG and no external assets.

## Open questions

1. **Archived logs:** should the timeline use them? A round archived with
   `swim archive` is still the run's history. Recommend yes: look up logs by
   run id across the current and archived logs.
2. **Steps:** `--steps` could be very wide. Should it switch to a vertical
   per-lane view?
3. **Should the TUI ([tui.md](tui.md)) show this live**, as a third view?
   Probably later, once both exist.

## Testing

- **Unit:**
  - the longest chain and the start delays on small fixed graphs with known
    answers
  - golden text rendering at fixed widths
  - YAML schema golden
- **Go e2e:** run a diamond with known sleeps; `swim timeline --yaml` reports
  the right order, a longest chain of root → slow child → join, and delays
  ≥ 0; it still works after `swim archive`.
- **Scenarios:** dag99's audit compares its own numbers with
  `swim timeline --yaml` for the same run (they must agree), then the
  duplicated calculation moves out of `e2etool`.
