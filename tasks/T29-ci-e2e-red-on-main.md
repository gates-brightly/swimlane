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

Checked 2026-10-15, on Linux arm64 with Go 1.27.1:

- **It doesn't reproduce locally at `9bf1a91`.** `scripts/hermetic go test
  -count=1 ./internal/e2e/...` passes in 267s.
- **It doesn't reproduce the way CI runs it either.** `swim ci --junit
  swim.xml` at `9bf1a91`, with a fresh HOME, no `~/.ssh`, no git identity,
  `CI=true GITHUB_ACTIONS=true` and `GITHUB_STEP_SUMMARY` set, passes. Lane
  1.4 took 4:27.
- It passes at HEAD (`3faa776`) too.
- Both runners took 7–8 minutes, against 4.5 here. Both failed on the same
  commit, so this is more likely something about the runners than a flake.
  Candidates, none confirmed:
  - A timing-sensitive test on slower runners: waits, `--timeout`, lease
    and heartbeat tests, the pty/TUI tests.
  - Something the runner image has that this machine doesn't (`TMPDIR`,
    `RUNNER_TEMP`, a newer git, `ssh-keygen` behaviour, `/bin/sh`).
  - A test that depends on order or shared state between packages.

## Do

- Read the failure from run #44's artifacts: `swim-ubuntu-latest` and
  `swim-macos-latest` → `.swim/logs/agent1.4.log`. That holds the
  `go test` output, with the `--- FAIL` lines. Or run
  `gh run download <run-id> -n swim-ubuntu-latest`.
- Check whether later pushes are also red. That tells a real regression
  in `9bf1a91` from a flaky test.
- Once the test is named, reproduce it with
  `go test -run '<Name>' -count=20 ./internal/e2e/`, under load
  (`stress`, or `GOMAXPROCS=2`) if it's timing.
- Then fix the test or the code. Raise a wait only if that's genuinely the
  fix, and say so in the commit.

## Done when

main's `test` workflow is green on ubuntu and macOS for three pushes in a
row. If the cause was timing, the test also passes 20 times in a row with
`GOMAXPROCS=2` locally.
