#!/usr/bin/env bash
# Round: DAG child C (of 1): extract every link
# Job:   e07e8da0-451c-44d1-ba98-df2151d7ba72
# After: 1
# Lane:  swim 5    Written: 2026-10-09
#
# Goal:
#   Every <a href> in combined.html, per source page, into links.tsv. ~0.1s: the
#   fast branch, so swim 7 waits on swim 3 long after this is done.
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 1) finished, for this run, before this node started
#   1. Simulated work (0.1s)
#   2. Gate: extract links.tsv (source, kind, href)
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/5.done
#
# Guard flags this round honours (flag: action, date, reason):
#   (none)
# Test toggle (not a guard; nothing destructive):
#   E2E_FAIL="3 5"   space-separated lanes that fail on purpose (to watch skips propagate)
#
# DAG (each lane's `# After:` line), lanes 1-9:
#   1 -> 2, 3, 5    2 -> 4, 6    3 -> 4, 7    5 -> 7
#   4, 6, 7, 8 -> 9    9 -> 10..19 (generated)    8 has no parents (independent root)
# Shared dir .scenario/dag/: run.id (swim 1), nodes/N.done ("<run> <start> <end>").
#
# Part of the e2e scenario e2e/scenarios/dag99 (run: e2e/run.sh dag99).
# Never edit this file while it may be running: `swim status` first.
# Do not use `set -e`: failed checks keep going; only gates stop the round.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 5

D=.scenario/dag
N=5
PARENTS="1"
NODE_START=$(e2etool now)
export D N PARENTS NODE_START
mkdir -p "$D/nodes"
run "clear own done marker" rm -f "$D/nodes/$N.done"
gate "parents finished before this node started (swim $PARENTS)" e2etool parents-check $PARENTS
RUN_ID=$(cut -d' ' -f1 "$D/run.id")
export RUN_ID

run "simulated work (0.1s)" sleep 0.1
gate "extract links.tsv" e2etool links

gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(e2etool now)" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
