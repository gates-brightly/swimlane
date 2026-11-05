# T32. `swim log N --failed`: only what a fix needs (P2, gap)

Source: the UX review (item 9). The AI comes in when a round fails; it
should read the failure, not the round.

**Today:** `swim log N` prints the round step by step (internal/cli/logcmd.go:25:
`--all`, `--full`, `--raw`, `--yaml`, `-f`, `--until-done`); `--yaml`
gives every step parsed. Nothing narrows it to what went wrong, so an
agent reads every PASS to find the FAIL.

**Do:**
- `swim log N --failed`: the round's header (Round, job, run), each step
  that failed, stopped or drifted with its command and output (in full),
  the step before each (one line: what had just run), the stage it was
  in, and the summary line. Passing steps are counted, not shown.
- The same filter in `--yaml` (`--failed --yaml`).
- `swim report` points at it for a failed round: `swim log 2 --failed`.
- The logs reference (docs/reference/logs.md) and the swim skill use it after a
  failure.

**Done when:** an e2e test fails one step of five and `swim log N
--failed` shows that step's output in full and no PASS line; on a passing
round it says `no failed steps` and exits 0.
