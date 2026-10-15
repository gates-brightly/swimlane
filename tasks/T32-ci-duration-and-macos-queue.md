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

## Do

- Measure first: which e2e tests and scenarios take the time
  (`go test -json`, plus `swim timeline` on the CI run).
- Options to weigh:
  - Split e2e into its own job, or shard it.
  - Run the slow scenarios (dag99, matrix25) on Linux only, with a
    bash 3.2 smoke set on macOS.
  - Run macOS e2e only on main and on PRs that touch `internal/assets`,
    `e2e/` or `scripts/`.
  - Use `swim ci --changed` for PRs.

## Done when

A green PR run finishes in under ~8 minutes, and macOS still covers
bash 3.2 for the lane library and `e2e/run.sh`.
