# T34. `TestRunKeyKeepsSigningFromLanes` is flaky (P1, bug)

Source: swim watch batch #76 (2026-11-03), on a Mac swimmer: lane 2 (e2e)
failed with `runkey_test.go:74: ssh-add -c: exit status 2 Error connecting
to agent: Connection refused`. The same tree passed in #75 and on a rerun
(#77), and three times in a row on Linux.

**Was** (until 2026-11-04, below): the test was skipped (`t.Skip("flaky:
T34 ...")` at the top of `TestRunKeyKeepsSigningFromLanes`,
internal/e2e/runkey_test.go). Its helper `newConfirmAgent` started
`ssh-agent -D -a <sock>`, waited until the socket *file* existed, then
ran `ssh-add -c`. The file can exist before the agent listens on it, so
on a loaded machine `ssh-add` connected too early and was refused.

**Do:**
- Wait until the agent answers, not until the socket exists: retry
  `ssh-add -l` against the socket (exit 1, "no identities", means it's up;
  2 means it isn't yet) within `waitIn`'s limit, then `ssh-add -c`.
- Remove the skip.

**Done when:** the test passes 20 times in a row with `-count=20` under
load (`GOMAXPROCS=2`, alongside the race lane), on macOS and Linux, and
the skip is gone. The claim the test proves (a lane can't sign as the
runner key) is in docs/reference/trust-model.md; until then it's unproven by CI.

**2026-11-04:** fixed and unskipped. `newConfirmAgent` now waits for
ssh-agent's `SSH_AUTH_SOCK=` line, which it prints once it listens (an
event the test's output Signal reports; no polling), not for the socket
file, which exists from bind. Linux: 20 of 20 with `-count=20
GOMAXPROCS=2`, and 40 of 40 with all 16 cores pegged by busy loops.
**Left:** the same on macOS; the next CI runs (T29) show it. Delete this
task when they do.
