#!/usr/bin/env bash
# swim: syntax 2
# Round: DAG join A+B (of 2,3): report.md
# Job:   7a2fd47d-e445-4c07-944a-b7519b8ef4ff
# After: 2 3
# Lane:  swim 4    Written: 2026-10-09
#
# Goal:
#   Fan-in of two siblings: stats table (swim 3) + converted content (swim 2).
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 2 3) finished, for this run, before this node started
#   1. Gate: build report.md
#   2. Verify: stats table and a section per page
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/4.done
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
lane_init 4

D=.scenario/dag
N=4
PARENTS="2 3"
NODE_START=$(e2etool now)
export D N PARENTS NODE_START
mkdir -p "$D/nodes"
run "clear own done marker" rm -f "$D/nodes/$N.done"
stage check
gate "parents finished before this node started (swim $PARENTS)" e2etool parents-check $PARENTS
RUN_ID=$(cut -d' ' -f1 "$D/run.id")
stage change
export RUN_ID

gate "build report.md" e2etool report

run "report has stats table and a section per page" bash -c '
  n=$(e2etool stats-pages)
  grep -q "^| \*\*total\*\*" "$D/report.md" && h=$(grep -c "^## " "$D/report.md") && echo "pages=$n h2=$h" && test "$h" -ge $((n + 2))'

stage verify
gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(e2etool now)" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
