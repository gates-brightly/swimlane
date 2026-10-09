# Run id: one id per `swim run`, given to every lane

Status: shipped (unreleased)

## Summary

Every `swim run` / `swim all` (and later `swim ci`) gets a run id. Swim exports
it to every lane it starts as `SWIM_RUN` and records it everywhere that
results are recorded. Lanes can then tell "output from my run" apart from
"output left over from an earlier run" without inventing their own markers.

```
== ROUND 2026-10-09T14:11:18Z  job=d68714a7-…  DAG99 node 60
   run: r-20261009T141118Z-7c2e | script: lane.60.sh | ...
```

## Motivation

The dag99 scenario hit exactly this.

- **The bug:** lanes stamped their outputs with a `run.id` file that lane 1
  wrote at the end of its round. A lane descended only from an independent
  root (lane 60, after 22) started before lane 1 had written the new id, read
  the previous run's id, and its children rejected it as stale. Three lanes
  failed, and eight more were skipped.
- **The fix was awkward:** run ids had to be inherited through every parent's
  done marker.
- **Inference elsewhere:** the lane 99 audit had to parse `.swim.log` for the
  last `run` event to learn which lanes were in its run.

Swim already knows both things at launch: that this is one run, and which
lanes are in it. Handing them over is cheap and removes a class of race.

## Behaviour

- **Format:** `r-<UTC yyyymmddThhmmssZ>-<4 hex>`, sortable and unique enough on
  one machine. `swim run --run-id ID` overrides it, e.g. so CI can pass its own
  pipeline id.
- **Exported to every lane process:**
  - `SWIM_RUN`: the run id
  - `SWIM_RUN_LANES`: the lanes in this run, space-separated

  `swim step` passes both through to step commands.
- **Recorded:**
  - **`agentN.log`:** `run: <id>` first in the round's context line, and
    `run=<id>` at the end of the `== END` line (log syntax 2)
  - **`status.yml`:** `run:` on each lane, and `last_run:` at the top level
  - **`.swim.log`:** `run=<id>` on `run`, `start`, `pass`/`fail`/`interrupted`,
    `skip` and `run-done` events
- **Lanes run directly** with `bash lane.N.sh`, outside `swim run`, get a run
  id of their own from `lane_init`, so `SWIM_RUN` is always set.
- **Queries:**
  - `swim status --run <id>` shows each lane as of that run
  - `swim log <id>` prints every lane's round from that run, in lane order

## Design

- The launcher (`internal/launcher.Run`) generates the id before resolving
  dependencies. It passes it into each lane's environment next to `SWIM_LANE`
  and `SWIM_JOB`, and into `history.Entry`.
- `logparse.RoundHeader` gets a `run` key in its context. Parsers treat an
  unknown key as optional, so old logs still parse.
- `status.Lane` gets `Run string \`yaml:"run,omitempty"\``.

## Decisions

1. A pinned rerun of one lane (`swim run 3`) does **not** reuse the last run's
   id: each invocation is its own run, and queries group by id.
2. `SWIM_RUN_LANES` **includes** lanes skipped before they started: it's the
   selection, and the outcomes are in status.
3. `swim log <id>` matches a round by job or by run id. `swim status --run`
   reads the logs (archives included), because `status.yml` holds only the
   latest state; lanes skipped before starting come from `.swim.log`.
4. dag99 now uses `SWIM_RUN`/`SWIM_RUN_LANES`: lane 1 no longer stamps a
   `run.id`, the `indep:` lineage is gone, and a parent outside the current run
   counts as fresh (swim only starts a lane whose outside dependencies passed).

## Testing

- **Go e2e:**
  - two lanes in one run see the same `SWIM_RUN`
  - two consecutive runs see different ids
  - the id appears in ROUND START, status.yml and every `.swim.log` event of
    the run
  - `--run-id` is honoured
- **Scenario:** simplify dag99 to use `SWIM_RUN` instead of inherited ids. The
  audit's run-membership check should then read `SWIM_RUN_LANES`, not parse
  `.swim.log`. Keep `E2E_CURL_DELAY=0.7` passing.
