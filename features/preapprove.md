# Pre-approving lanes in the live view

Status: proposed

## Summary

In the interactive view of `swim run` or `swim all`, select a lane that
hasn't asked yet with `←`/`→` and press `y`. Its guards are approved ahead
of time, so it won't stop for a question when it gets there. The operator
can approve a whole sequence up front and walk away:

```
swim · orders · main@64a0e07 · 0:12
 swim 1  / running 0:12        Snapshot orders stack
 swim 2  waiting on swim 1     Cut over orders-api        ✓ pre-approved
 swim 3  waiting on swim 2     Cut over billing-api       ? 1 guard
 swim 4  waiting on swim 3     Drop legacy tables         ✗ dry run
──────────────────────────────────────────────────────────────────────
swim 3 · Cut over billing-api · waiting on swim 2
  guards:  BILLING_ALLOW_SST_REMOVE  remove SST stack billing-prod
  y pre-approve · n pre-decline (dry run) · u undo · Esc all lanes
```

## Motivation

- **Sequential runs wait on a person.** With `swim run 1 2 3 4 5` in
  sequence, or `--parallel 1`, a run with five guarded lanes asks five
  questions, minutes apart. Each one stalls the run until someone comes back
  to the terminal.
- **Today's options are blunt:**
  - `-y` approves every guard in the run.
  - Setting `FLAG=1` up front means knowing each flag's name, and it records
    nothing about who decided.
  - A signed approval is meant for teammates elsewhere, not for the person
    already at the terminal.
- **The view already does most of it.** `←`/`→` already select a lane, and
  `y`/`n` already answer a lane that's asking (`internal/display/tui.go:194`).
  This extends `y`/`n` to lanes that haven't asked yet.

## Behaviour

### Keys

- **On a lane that's asking:** `y` and `n` answer it, as today.
- **On a lane that hasn't reached its guard yet** (waiting, queued, locked,
  or running before its guard):
  - **`y`** pre-approves every guard swim knows for that lane: the
    `Guards:` header plus `guard` calls found in the script (`lane.Info`).
  - **`n`** pre-declines them, so they run as dry runs without asking.
  - **`u`** clears the choice before it's used.
- **Selected lane panel:** the lane's guards and the keys are shown, as in
  the example.
- **Marks in the lane list:** `✓ pre-approved`, `✗ dry run`, or
  `? N guards` when it would ask.
- **Lanes with nothing to approve:** pressing `y` on a lane with no known
  guards says `swim 3 has no guards to approve`.
- **Finished lanes:** pressing `y` says `swim 3 already finished`.

### Which guards are covered

- **Pinned to the script:** a pre-approval covers the guards of that lane's
  exact script (by SHA-256) for this run only. If the script changes before
  the lane starts, the pre-approval is dropped and the lane asks as usual.
- **Dynamic guard names** (`guard "$FLAG" ...`) can't be known in advance.
  They still ask, and the lane's mark says `? 1 guard not known in advance`.
- **All of a lane's guards together:** a lane with several guards
  pre-approves all of them. Choosing per guard is an open question.

### Precedence

- **Where it sits:** a pre-approval is the operator's answer to the terminal
  prompt, given early. So it sits where the prompt does in the guard order:
  1. The flag (`FLAG=1` / `FLAG=0`) still wins.
  2. Then signed approvals.
  3. Then the pre-approval, in place of asking.
  4. Then `-y`.
- **`lookup.yml`:** pre-approvals count toward `lookup.yml` rules exactly as
  a prompt answer does. If a rule needs two people, the second approval
  still has to come from somewhere else.
- **With evidence-bound approvals** ([plan-approvals.md](plan-approvals.md)):
  a guard whose rule requires evidence ignores the pre-approval and asks,
  showing the evidence. Approving before the plan exists can't count as
  approving the plan.

### Records

- **The log:** `APPROVED  <action>  (pre-approved by zach at 12:00:41 in the
  live view)`, or `SKIP ... (pre-declined ...)`.
- **The chain:** an approve entry with `via: preapproved`, recording when
  the choice was made and when it was used.
- **`--yaml`:** `preapproved` and `predeclined` events, and the existing
  `approved` event when one is used.
- **Never used:** pre-approvals that are never used (the lane was skipped or
  never reached its guard) are listed in the run summary, and don't go into
  the chain.

### Where it's available

- **Interactive view only:** that's more than one lane on a terminal, the
  same condition as today's view.
- **Not in plain mode, `--yaml`, CI or `swim watch`.** Those have flags,
  `--approve` and signed approvals.

## Design

- **Display (`internal/display/tui.go`):**
  - `y`, `n` and `u` on a selected lane that isn't asking return
    `actPreYes`, `actPreNo` and `actPreClear` with the lane number.
  - `LaneView` gains `Pre` (`approved`, `declined` or empty) and
    `KnownGuards` for the marks and the panel.
- **Launcher (`internal/launcher/launcher.go:285`):**
  - The run keeps `pre map[int]preChoice{choice, scriptSHA, by, at}`, set
    from the display's actions.
  - The approval broker's handler checks `pre[req.Lane]` before
    `disp.Ask`. If the choice exists, `req.Flag` is among the lane's known
    guards, and the SHA matches, it returns the answer with
    `Via: "preapproved"`. Otherwise it asks as today.
- **Guard order:** the broker is reached only after flags and signed
  approvals, so precedence is unchanged.
- **Summary:** unused pre-approvals are listed.

## Open questions

1. **Choosing per guard:** for a lane with several guards, select a guard
   (`↑`/`↓` in the lane panel) and approve it alone? `↑`/`↓` currently
   scroll there.
2. **Single lane:** with one lane, the lane gets the terminal's stdin, so
   there's no interactive view. Should `swim run 2 --preapprove` ask about
   each known guard once, before starting?
3. **A pre-approve-all key:** something like `Y` to pre-approve every
   remaining lane? That's close to `-y`, but it's recorded per lane and
   pinned to each script.
4. **Running lanes before their guard:** allow pre-approving a lane that's
   already running but hasn't reached its guard (as above), or only lanes
   that haven't started?

## Testing

- **Pre-approve:** a lane with `y` set runs its guarded step without asking,
  and the log shows `pre-approved by`.
- **Pre-decline:** `n` makes its guards dry runs without asking. `u` clears
  the choice, and the lane asks again.
- **Pinning:** editing the script after `y` drops the pre-approval, and the
  lane asks. A dynamic guard name still asks.
- **Precedence:** `FLAG=0` beats a pre-approval. A two-person rule still
  needs a second approver. An evidence-required guard ignores the
  pre-approval.
- **Records:** chain entries have `via: preapproved`, and unused choices
  are listed in the summary but not in the chain.
- **Availability:** no effect in plain mode, `--yaml`, CI or watch.
- **TUI:** marks render, and `y`/`n` on a lane that's asking still answer as
  today.

## Out of scope

- **Lasting past the run:** pre-approvals end with the run. Use signed or
  standing approvals for anything longer.
- **Pre-approving from another terminal.** That's `swim approve`.
