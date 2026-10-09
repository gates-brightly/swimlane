#!/usr/bin/env bash
# swim: syntax 2
# Round: DAG child A (of 1): combined.html to Markdown
# Job:   7353ee29-f742-41b0-bcd1-ebab167c75a2
# After: 1
# Lane:  swim 2    Written: 2026-10-09
#
# Goal:
#   Convert swim 1's combined.html to combined.md (stdlib-only converter). ~6ms.
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 1) finished, for this run, before this node started
#   1. Private copy of the input; simulated work (6ms); convert
#   2. Gate: one H1 per page, no leftover tags
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/2.done
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
lane_init 2

D=.scenario/dag
N=2
PARENTS="1"
NODE_START=$(e2etool now)
export D N PARENTS NODE_START
mkdir -p "$D/nodes"
run "clear own done marker" rm -f "$D/nodes/$N.done"
stage check
gate "parents finished before this node started (swim $PARENTS)" e2etool parents-check $PARENTS
RUN_ID=$(cut -d' ' -f1 "$D/run.id")
stage change
export RUN_ID

IN=$D/input-2.$SWIM_JOB.html
export IN
gate "copy input to $IN" cp "$D/combined.html" "$IN"
run "simulated work (6ms)" sleep 0.006
gate "convert html -> markdown" env OUT="$D" e2etool markdown
gate "one H1 per source page" bash -c 'h=$(grep -c "^# " "$D/combined.md"); s=$(grep -c "<section data-source" "$IN"); echo "h1=$h sections=$s"; test "$h" -ge "$s" -a "$h" -gt 0'
gate "no leftover HTML tags" bash -c '! grep -nE "</?(div|p|span|section|body|html)[ >]" "$D/combined.md"'

stage verify
gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(e2etool now)" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
