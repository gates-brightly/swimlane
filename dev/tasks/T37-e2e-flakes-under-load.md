# T37. e2e tests that fail under load (P1, bug)

**Today:** during the 2026-11-05 cleanup, up to six agents ran lane 2 (the
e2e suite) at once on a 16-core container. Each test below failed once and
passed on a rerun of the lane. Each also passes alone, with `-count` of 40
or more where it was tried.

| Test | What it did |
|---|---|
| `TestInterruptRecordsAndStopsRound` | Hung until the suite's 25m timeout. `swim` under test had exited (defunct), and its lane's `bash -c 'trap "exit 130" INT TERM; ... cat <fifo>'` stayed blocked: the SIGINT to the group didn't reach or end `cat`. 80/80 alone. |
| `TestHangupForcesAtOnce` | "SIGHUP should force at once; took 5.0s" (twice). |
| `TestGracefulInterrupt` | agent3.log missing "STOP interrupted by operator". |
| `TestImmediateInterruptMode` | Expected "exit 130, interrupted", got "cut off: swim was killed mid-step". |
| `TestTUIRestoresTerminal/panic` | 120s timeout. |
| `TestShortFormShebang` | A lane run from another directory left an empty log. 5/5 alone. |

Four of the six are interrupt and signal tests. Each has a race between
a signal and a step starting: a signal sent before the step's trap is
set, or before the step's process group exists.

**Do:** for each test, find the race and wait on the event, not the
clock, as the e2e suite already does elsewhere (`testwait`). A hang must fail
fast: give interrupt tests their own short deadline instead of the
suite's 25 minutes.

**Done when:** lane 2 passes 20 times in a row with six copies running
at once (`E2E_SLOW=2`), and on macOS CI three pushes in a row (with T29).
