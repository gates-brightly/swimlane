# T29. e2e fails in GitHub Actions on main, on both OSes (P0, ci, bug)

Source: run #44, "feature writeups" (`9bf1a91`, pushed 2026-10-15).

| Job | 1.1 fmt | 1.2 lint | 1.3 unit | 1.4 e2e |
|---|---|---|---|---|
| ubuntu-latest | PASS 0:01 | PASS 0:03 | PASS 0:11 | **FAIL 7:02** |
| macos-latest | PASS 0:01 | PASS 0:09 | PASS 0:20 | **FAIL 7:46** |

The only annotation is `FAIL go test ./internal/e2e (exit 1)`. The summary
didn't say which test failed. Since `69212ab`, a red run names the failed
test and keeps what's needed to debug it, so the first red run after that
commit is pushed will name the test. Every run on GitHub up to `8d38adb`
(#46) predates it.

## What's known

Since `69212ab` a red run names its failed tests, and `swim run 5` (lane
5, `74d74e63`) downloads a run's artifacts to `.ci-runs/<run id>/`. The
public API also gives jobs and annotations without credentials:
`api.github.com/repos/gates-brightly/swimlane/check-runs/<job>/annotations`.

Found and fixed, run by run:

| Run | Commit | Failed | Cause | Fix |
|---|---|---|---|---|
| 38778399849 | `72be36b` | 5 approval tests, both OSes | T28's ancestry check took the outer `swim ci` for the nested scratch-repo lanes' launcher | `1a93259` |
| | | `dag99-interrupt`, `dag99-force` | Ctrl-C sent before swim run's handler: dropped | `f09903a` (and `b28c11b` in swim) |
| | | `TestCachePresetPersistsBetweenRounds`, macOS | the test assumed TMPDIR under /tmp | `b0db00a` |
| 38784020382, 38785127202 | `b0db00a`, `df05a6b` | `TestLaneRCDeprecated`, macOS | bash 3.2's `declare -p` format: a shell flag taken for the rc's | `c01bbe5` |
| | | `dag99-force`, macOS (2.7-4.4s) | one synced status.yml write per skipped lane | `3baf319` |
| 38788128606, 38790683405 | `3baf319`, `b28c11b` | ubuntu race job: `TestStatusBatch` | the test slept 150ms for a flush that -race on a loaded runner hadn't done | `e0045fc` |
| 38790683405 | `b28c11b` | `TestWrappedLaneHandlers`, macOS | the wrapper caught its own group SIGTERM and handed it to on_failure (2 in 40 under load) | `4d51ae9` |

Ubuntu was green on `b0db00a` and `df05a6b`; macOS green on `3baf319`
and `e70fb2b`. Still open (ubuntu): `e70fb2b`'s `TestPromptLineDeclines`
and `TestPromptIsSafe/tui`. Both pass here, alone and under fuzz load
(load average 15). The tui case waits 15s for "q to close" or "stages:
setup SKIP", and the CI screen shows the TUI closed itself with only the
summary: the test's end condition may race the view closing. They passed
on `df05a6b`; nothing since touches what they run.

## Do

- If the prompt tests fail again, read that run's artifacts for their full
  output (`CI_SHA=<sha> swim run 5` fetches a given commit's run).
- If the prompt tests race the TUI closing, wait for an end the view
  always shows, not one of two it may skip.

## Done when

main's `test` workflow is green on ubuntu and macOS for three pushes in a
row. If the cause was timing, the test also passes 20 times in a row with
`GOMAXPROCS=2` locally.
