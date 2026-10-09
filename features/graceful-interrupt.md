# Graceful Ctrl-C, twice to force quit

Status: proposed

## Summary

The first Ctrl-C during `swim run` / `swim all` asks every lane to stop
**at the next step boundary**:

- running steps are left to finish
- no new step starts
- lanes that haven't started are skipped
- the summary prints as usual

The second Ctrl-C **force quits**: running steps are signalled, then killed
after a short grace period. A third Ctrl-C kills them at once.

```
^C swim: stopping after current steps (6 running). Ctrl-C again to force quit.
   swim 3   stopping  tf plan (1m12s)
   swim 17  stopping  fetch example (0.4s)
^C swim: force quitting: interrupting 6 steps, killing in 5s. Ctrl-C again to kill now.
```

## What happens today

Lanes run in swim's own process group (`internal/launcher/launcher.go`). That
group is the terminal's foreground group, so the terminal sends Ctrl-C to
swim, every lane's bash, every `swim step` and every step command at the same
moment:

- the launcher marks the run interrupted and skips lanes that haven't started
- `lib.sh` traps INT with `exit 130`
- `swim step` records `--- interrupted (signal …)` and flushes the log

The result is always an immediate interrupt of whatever was mid-flight. A
second Ctrl-C adds nothing, because everything already got the first.

## Motivation

- **Steps are cut off mid-change.** A step interrupted halfway is the worst
  place to stop an operational round:
  - a migration halfway through a batch
  - `aws s3 sync` partway through
  - a `kubectl rollout` that's been asked to stop
  - a `curl` POST whose response is lost

  The safety rules ask rounds to be rerunnable, but a clean step boundary is
  much easier to reason about than "somewhere inside step 7".
- **A second Ctrl-C can force-abort tools.** Terraform, for example, treats one
  SIGINT as "stop gracefully" and a **second** as "abort now, possibly leaving
  state locked". Today an operator's second Ctrl-C (pressed because the first
  "didn't seem to work") reaches terraform directly and does exactly that.
  Swim should decide what reaches step commands, not the terminal.
- **There's no feedback.** After the first Ctrl-C, nothing says what swim is
  waiting for.

## Behaviour

### First Ctrl-C: stop gracefully

- **Lanes that haven't started** (waiting, queued, locked) are skipped with
  reason `interrupted before start`, as today.
- **Running lanes** finish their **current step** and then stop:
  - nothing is sent to the step command
  - no further `run`/`gate`/`snapshot` starts; each refuses with a reserved
    exit code, and the library records
    `STOP  interrupted by operator (after: <last step label>)`
  - the lane ends `interrupted`, with exit 130, and its `summary` still runs,
    so the log and status are complete
- **A step that is waiting between retry attempts**
  ([step-timeout-retry.md](step-timeout-retry.md)) stops waiting and doesn't
  retry.
- **A `confirm` prompt** that is waiting for input is cancelled (the lane is
  idle, not mid-change). The answer is recorded as no, so the round stops.
- **Display:**
  - the panel title becomes `stopping · 6 steps running · Ctrl-C again to force quit`
  - running lanes show `stopping <step> (<elapsed>)`
  - plain mode prints that line once, then each lane as it stops
  - the YAML stream adds `stop_requested` and per-lane `stopping` events

### Second Ctrl-C: force quit

- Every running lane's process group gets **SIGINT** (so tools that clean up
  on SIGINT, like terraform's first-signal handling, get that one chance).
  After `interrupt_grace` (default 5s), anything still alive gets **SIGKILL**.
- Logs are flushed and steps record
  `--- interrupted (signal interrupt) exit 130` or `(signal killed) exit 137`,
  as today.
- The summary still prints, and the terminal is restored.

### Third Ctrl-C

SIGKILL to every lane's process group at once. Swim then prints the summary
from what it has and exits.

### Other signals

| signal | treated as |
|---|---|
| SIGINT (Ctrl-C) | as above: first graceful, then force, then kill |
| SIGTERM (CI cancel, `kill`) | graceful, escalating to force after `term_grace` (default 10s) without a second signal. CI runners give a grace period before their own SIGKILL, so it should be shorter than that |
| SIGHUP (terminal closed) | force quit: nobody is left to wait for |

The time between Ctrl-C presses isn't limited: the second press always
escalates, however long after the first.

### Config

```yaml
defaults:
  interrupt: graceful     # graceful (new default) | immediate (today's behaviour)
  interrupt_grace: 5s     # force quit: SIGINT -> SIGKILL
  term_grace: 10s         # SIGTERM: graceful -> force
```

`--interrupt immediate` on a single run restores today's behaviour: the first
Ctrl-C goes straight to the force-quit step.

## Design

- **Own process groups:** each lane starts with
  `SysProcAttr{Setpgid: true}`, so the terminal's Ctrl-C reaches only swim, and
  swim decides what to forward. Today's comment in the launcher ("Ctrl-C
  reaches the lanes directly (same process group)") describes the design this
  changes.
- **The stop request:** this is a file, not a signal. The launcher writes
  `.swim/laneN.stop` (holding the run id) for each running lane.
  - At the start of every step, `swim step` checks for its lane's stop file
    and refuses to start (reserved exit code, e.g. 86).
  - `_swim_after_step` in `lib.sh` sees that code and calls
    `stop "interrupted by operator …"`.
  - A file avoids bash trap timing issues (bash runs a trap only after the
    foreground command returns) and also works for `bash lane.N.sh` run by
    hand: `swim interrupt N` would write the same file.
  - The file is removed when the lane finishes or a new round starts.
- **Retry waits and `confirm`:** these need to wake up immediately. Swim sends
  `SIGUSR1` to the lane's `swim step` processes and to the bash running the
  lane, and nothing else.
  - The step's backoff timer is cancelled.
  - `lib.sh` traps USR1 during `confirm`'s `read` and treats it as no answer.
  - Step commands never get USR1, because the signal goes to specific pids,
    not the process group.
- **Force quit:** `syscall.Kill(-pgid, SIGINT)` for each lane, a timer for
  `interrupt_grace`, then `Kill(-pgid, SIGKILL)`. The launcher's `procs` map
  becomes `pgids`.
- **`swim step` run directly:** behaviour is unchanged when it isn't under the
  launcher, i.e. a user running `swim step -- cmd` in a shell.

## Open questions

1. **Lanes that own the terminal's input.** A single-lane run passes stdin to
   the lane, so `confirm` can read it. A process in a background process group
   that reads the terminal gets SIGTTIN and stops. Options:
   - (a) give the lane's group the terminal (`tcsetpgrp`) and take it back
     after; but then Ctrl-C goes to the lane, not swim
   - (b) keep single-lane runs in swim's process group, with today's
     immediate behaviour
   - (c) swim reads the terminal itself and passes input through to the lane

   Recommend (c) if [tui.md](tui.md) lands, since the TUI reads the keyboard
   anyway. Until then, (b), documented.
2. **Default.** Should `graceful` become the default? It changes documented
   behaviour ("Ctrl-C reaches running lanes"). Recommend yes, with
   `interrupt: immediate` as the escape hatch, and the change called out in the
   release notes and guide.
3. **A step that never ends** (a hung `kubectl logs -f`). Graceful waits
   forever, but the operator has a second Ctrl-C and the step's `--timeout`.
   Should there be a `stop_timeout` that escalates automatically, like
   SIGTERM's? Recommend no for Ctrl-C (a human is present) and yes for SIGTERM
   (already `term_grace`).
4. **Stopping one lane.** Should `swim interrupt N` request a graceful stop for
   just that lane, from another terminal? It's cheap given the stop-file
   design, and useful for the TUI's "stop selected lane".

## Testing

- **Unit:** the escalation state machine (graceful → force → kill; SIGTERM's
  timer; SIGHUP goes straight to force); handling the reserved code in
  `lib.sh`, run in bash 3.2.
- **Go e2e:** these use the existing `Setpgid` harness in
  `internal/e2e/e2e_test.go`, which signals swim's group like a terminal does.
  - **One Ctrl-C:** a lane with `run "a" sleep 2; run "b" true`, Ctrl-C at
    0.5s. Step "a" finishes with exit 0 and **was not signalled**, "b" never
    starts, the log has `STOP interrupted by operator (after: a)`, and the
    lane is `interrupted` with exit 130. Lanes that hadn't started are skipped.
  - **Two Ctrl-C:** a step running `trap 'echo got-int; sleep 1; exit 3' INT; sleep 30`.
    The second Ctrl-C gives `got-int` in its output; with a 0.5s grace and a
    trap that sleeps longer, the step is killed (`exit 137`); the summary
    prints.
  - **A terraform-like tool:** a fake command that exits cleanly on its first
    SIGINT and records whether it ever got a second. After one Ctrl-C it got
    none; after two, it got exactly one.
  - **SIGTERM:** escalates to force after `term_grace`.
  - **SIGHUP:** forces at once.
  - **Retry wait:** a step in its backoff wakes and doesn't retry.
  - **`confirm`:** a waiting prompt is cancelled.
  - **No stray processes:** no step processes survive any of these; check
    `pgrep -g <pgid>`.
- **Scenario:** `dag99-interrupt`. Start dag99, send SIGINT at a random point
  between 1s and 3s, and check:
  - every lane is `passed`, `interrupted` (at a step boundary) or `skipped`
  - no step in any log was interrupted mid-command
  - no lane started after the signal
  - the summary counts add up to 99

  A second variant sends two SIGINTs and checks that every process is gone
  within the grace period plus 1s.
