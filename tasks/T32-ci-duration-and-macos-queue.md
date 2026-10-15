# T32. CI takes 14 minutes, and macOS jobs queue (P2, ci)

Source: run #44 (`9bf1a91`, 2026-10-15). It took 14m 26s in total. The
e2e lane (1.4) took 7:02 on ubuntu and 7:46 on macOS. There was also a
notice:

> Due to capacity constraints, jobs targeting macOS arm64 runners may
> experience longer queue times.

## Today

- Every push and PR runs the full matrix on both OSes (`ubuntu-24.04`,
  `macos-15`): fmt, lint, unit tests and the whole e2e suite (the Go suite
  plus every `e2e/scenarios/` scenario). Since `9f84c91` it also runs race,
  fault-injection, security and fuzz jobs, each its own job, which adds to
  the total.
- macOS is there for bash 3.2 and BSD userland, and that coverage matters.
  But it's the slowest job, and now the most likely to queue.

## Measured (2026-10-15)

Since `df05a6b` the slow suites run side by side: lane 1 (fmt, lint, unit,
race) first, then e2e (2), fault (3), security (4) and fuzz (6) together,
and e2e runs its tests in parallel. A job went from 11.2m (`b0db00a`) to
6.4-8.5m (`df05a6b`..`e70fb2b`, the `swim ci` step 5.6-7.5m).

From `df05a6b`'s JUnit files (ubuntu / macOS):

| Lane | ubuntu | macOS |
|---|---|---|
| 1: fmt, lint, unit | 27s | 21s |
| 1.4: race | 55s | 47s |
| 2: e2e | **367s** | **269s** |
| 3: fault | 45s | 32s |
| 4: security | 45s | 47s |
| 6: fuzz (4 targets, 20s each, plus builds) | 151s | 135s |

The critical path is lane 1 (~80s, 55s of it race) then e2e. In e2e the
seven dag99 scenarios dominate: 110-130s each on ubuntu (dag99-retry,
-locks, plain, -parallel), against ~25s each on an idle 4-core machine.
They share the runner's CPUs with the rest of e2e and with fault,
security and fuzz; `go test -fuzz` starts a worker per CPU.

Done: fuzz runs two workers (`-parallel 2`), so it no longer takes every
CPU from e2e while it runs.

## Do

- Check the next few runs' `swim ci` step against the 6-7.5m above.
- If e2e is still over ~6m, the next steps, cheapest first:
  - Move race (55s) out of lane 1 into its own lane beside e2e: the
    critical path loses it, but the gate before e2e is then fmt, lint and
    unit only.
  - Run dag99-locks, -retry and -parallel on Linux only, with plain dag99,
    -interrupt and -force (the bash 3.2 and signal coverage) on macOS too.
  - Shard e2e (`-run` by prefix) into two lanes.
- The macOS capacity notice is GitHub's; nothing in the workflow changes
  it short of running macOS less often (main only, or PRs touching
  `internal/assets`, `e2e/` or `scripts/`).

## Done when

A green PR run finishes in under ~8 minutes, and macOS still covers
bash 3.2 for the lane library and `e2e/run.sh`.
