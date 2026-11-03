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
| 38792889122 | `50dc82c` | `TestHeadlessDetachStatusStop`, macOS | the test read the worker's log between consuming the batch and writing "passed; consumed" | `4720e73` |
| #63, #64, #70 | `bbb280f`, `12bff61`, `31a16a5` | `TestSDKStepHonoursTimeout`, both OSes | Python's start-up and the SDK's hello used the round's 1s Timeout before step one | `edc36e7` (alone, 3s) |
| #70 | `31a16a5` | `TestReportLoop`, macOS | a peek rewrote status.yml with a new updated_at once the clock passed a second | `9d95323` (no write when nothing changed) |
| Mac CI lane, batches #37, #41 | `31a16a5` | `TestHandlerTimeout`, `TestRetryRespectsRoundDeadline`, `TestPauseBeforeStartAndTimeout` | lanes took longer than their whole 1-3s Timeout to start beside the parallel tests | `1fb3cfe`, `8812667` (alone; more margin) |
| #74 | `81463bc` | `TestPromptIsSafe/tui`, ubuntu (15.3s) | after `n` the view sometimes closes by itself (the round ends fast): the main screen is back, and the wait for "q to close" or the lane view ran out | the wait also ends when swim exits |
| #75 | `1ae7bd9` | `ci-changed`, ubuntu: its first dag99 run failed (log cut above the checks) | likely the dag99 audit's root-start spread: two independent roots 2s+ apart on a loaded runner failed the run | the audit fails only when a root started after another ended (real serialisation); the spread is still reported |

**Green on both OSes at `0e71baf`** (run 38792820184), the first since
T29 was filed. `50dc82c` (docs only) then failed one flaky macOS test,
fixed above. `e70fb2b`'s `TestPromptLineDeclines` and
`TestPromptIsSafe/tui` (ubuntu) haven't failed since; they pass here alone
and under load.

**2026-10-16:** green at `98fc77f`, `1b5d681`, `aab1e40`, `77c74ec` and
`8812667` (runs #65, #67-69, #71), with the failures above in between.
Still seen, not yet fixed: `TestPromptIsSafe/tui` (ubuntu: #66, #70), the
`dag99-parallel` scenario's scheduler latency (ubuntu, #70: max 9.2s) and
`ci-changed` (ubuntu, #66). The GitHub API gives runs, jobs and failure
annotations without credentials:
`api.github.com/repos/gates-brightly/swimlane/actions/runs`.

**2026-10-27: the class, not one more test** (`fc07c45`..`70b04ec`).
The one-off fixes above were one cause: tests that assume an idle
machine. The e2e suite now has one timing model (e2e_test.go, "Timing"):
waits are for events, with one generous deadline (`waitDeadline`, 60s,
free while things work); a step the test acts on is held (`hold` /
`r.release`) instead of slept past; margins a test times on purpose
scale with `E2E_SLOW` (lanes 2-4 set 3 under CI). Interrupt, pause,
headless, hub, queue, report, live-log and dag99-force tests moved to it.
Two product bugs it surfaced are fixed: the hub's event heartbeat was a
package variable a test raced (`go test -race` red on both OSes since
`86778de`; `73ff0d4`), and `swim log -f` dropped a step's last output
when it printed and exited between two polls (`717fb3d`,
`TestLogFollowLateOutput`).

Local evidence on that branch (8 cores, `E2E_SLOW` unset, so stricter
than CI): the full suite passed twice (124s, 118s; it took ~240s before)
and once with all 8 cores pegged by busy loops (183s); `go test -race`
on the unit packages passed. Four tests that failed once under load on
another branch (`TestTUIManyLanesKeepsSelection`,
`TestHeadlessHubCommands`, `TestHangupForcesAtOnce`,
`TestImmediateInterruptMode`) passed in all three. On 2026-10-29, at
`9d855af`, the e2e suite passed 20 times in a row with `GOMAXPROCS=2`
(62-68s each, 8 cores, Linux). Not yet done: CI itself.

## Do

- Push, then watch the next pushes: done at three green in a row.
- If `TestPromptIsSafe/tui`, `dag99-parallel` or `ci-changed` fail again,
  read the run's annotations (above) for the failing check.
- If the prompt tests fail again, read that run's artifacts for their full
  output (`CI_SHA=<sha> swim run 5` fetches a given commit's run).
- If the prompt tests race the TUI closing, wait for an end the view
  always shows, not one of two it may skip.

## Done when

main's `test` workflow is green on ubuntu and macOS for three pushes in a
row. If the cause was timing, the test also passes 20 times in a row with
`GOMAXPROCS=2` locally.
