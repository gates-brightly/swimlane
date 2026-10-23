# When something looks wrong

Look first, with read-only commands. Then tell the operator what you
found, quoting the lines, and give the exact fix from this page or from
`docs/recovery.md` in the swimlane repo. Don't run the fixes yourself:
they change swim's state or the target. Never edit `.swim/`, `.swim.log`
or the chain to make a symptom go away.

## First look

```sh
swim status            # each lane's state; "running?" (process gone) is a dead lane
swim doctor            # environment and repo checks: [stale-pid], lock, config, PATH (no --fix)
swim queue             # waiting, running, orphaned and done batches
swim report --earmark  # what happened since you last looked
swim log N             # the lane's last round, step by step
swim verify            # only when the record itself is in question
```

## Symptoms

| You see | What it means | Hand the operator |
|---|---|---|
| `running?` / `process gone`, or `[stale-pid]` in doctor | The lane's process died without ending its round. The next `swim run`, `swim all`, `swim ci` or `swim watch` records it as interrupted by itself, and never reruns anything. | `swim status --rebuild` (or just the next run), then decide on a rerun after reading `swim log N` |
| `interrupted`, `END INTERRUPTED` | Ctrl-C, `swim interrupt`, a CI cancel, or a closed terminal | Read the log first. `swim run N` reruns it; `swim run N --failed` reruns a matrix's failed cells. A queued batch: `swim queue requeue ID` |
| An `orphaned` batch in `swim queue` | Its swimmer stopped renewing its lease | `swim queue requeue ID`, or `swim queue rm ID` |
| `SKIP ... (dry run ...)` | A guard wasn't approved. Nothing changed. | Nothing is broken. Re-hand the command with the flag explained (handoff.md) |
| `BLOCKED` (exit 87) | The step's text matched a blocked command (`git push`, ...) | Rewrite the step; even an `echo "git push"` matches |
| `DRIFT` / `drift: observed facts changed` | An `Observe:` probe changed between approval and use (another account, another binary) | Find out why before anything else. Never re-approve blindly |
| A lock mismatch (`.swim.lock`) | The repo's lanes need a different swim syntax than this binary | `swim lock`, `swim changelog --since <version>`, then the operator upgrades swim or the lock |
| `swim verify`: a level says `no` | The record was changed, cut or signed by an unknown key | Quote the `BROKEN:` lines verbatim, and point to "`swim verify` fails" in `docs/recovery.md`. Don't guess, and don't repair |

## A round that failed partway through a change

This is the dangerous case: the target is now half-changed. Work it out
before writing anything:

1. **Reconstruct what happened from the log** (`swim log N`):
   - which steps in the `change` stage passed;
   - which one failed, with its exit code and output;
   - whether a guard was approved;
   - whether `on_failure` / `on_exit` handlers ran (syntax 3), and how they
     ended.
2. **Find the before state:** the round's `snapshot` steps saved their
   output (`saved: .swim/snapshots/...` in the log).
3. **Don't rerun blindly.** Rerunning is safe only if the round's gates tell
   "already done by us" from "not done" (resources tagged with
   `$SWIM_JOB`). If they don't, a rerun can double-apply or trip over its
   own half-made resources.
4. **Write a recovery round:**
   - snapshot the current deployed state;
   - gate on what's actually there;
   - then either finish the change forward or roll it back, behind its own
     guard, with the choice and the reason in the header.

   Tell the user which of the two you recommend, and why.
5. **Say plainly what state the target is probably in,** with the log lines
   that show it, and what's unknown. If something is unknown, the next
   round reads it before it changes anything.
6. **For the next time:** add `on_failure` handlers (`# swim: syntax 3`) that
   undo the steps of the change stage, and gates that make the round
   rerunnable.
