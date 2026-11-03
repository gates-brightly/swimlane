# T34. `TestRunKeyKeepsSigningFromLanes` is flaky (P1, bug)

Source: swim watch batch #76 (2026-11-03), on a Mac swimmer: lane 2 (e2e)
failed with `runkey_test.go:74: ssh-add -c: exit status 2 Error connecting
to agent: Connection refused`. The same tree passed in #75 and on a rerun
(#77), and three times in a row on Linux.

**Today:** the test is skipped (`t.Skip("flaky: T34 ...")` at the top of
`TestRunKeyKeepsSigningFromLanes`, internal/e2e/runkey_test.go). Its
helper `newConfirmAgent` (runkey_test.go:22) starts `ssh-agent -D -a
<sock>`, waits until the socket *file* exists (runkey_test.go:43), then
runs `ssh-add -c`. The file can exist before the agent listens on it, so
on a loaded machine `ssh-add` connects too early and is refused.

**Do:**
- Wait until the agent answers, not until the socket exists: retry
  `ssh-add -l` against the socket (exit 1, "no identities", means it's up;
  2 means it isn't yet) within `waitIn`'s limit, then `ssh-add -c`.
- Remove the skip.

**Done when:** the test passes 20 times in a row with `-count=20` under
load (`GOMAXPROCS=2`, alongside the race lane), on macOS and Linux, and
the skip is gone. The claim the test proves (a lane can't sign as the
runner key) is in docs/trust-model.md; until then it's unproven by CI.
