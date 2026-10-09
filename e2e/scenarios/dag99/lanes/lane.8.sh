#!/usr/bin/env bash
# swim: syntax 2
# Round: DAG independent root: HTTP response headers
# Job:   07aee2b7-be33-4c6e-9f09-2fbbbb35af2a
# After: 
# Lane:  swim 8    Written: 2026-10-09
#
# Goal:
#   No parents: should start at the same moment as swim 1, not after it.
#   HEAD three sites; summarise status/server/content-type into headers.md.
#
# Steps:
#   0. Clear this node's done marker; 
#   1. Simulated work (4ms); HEAD three sites (read-only)
#   2. Gate: headers.md
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/8.done
#
# Guard flags this round honours (flag: action, date, reason):
#   (none)
# Test toggle (not a guard; nothing destructive):
#   E2E_FAIL="3 5"   space-separated lanes that fail on purpose (to watch skips propagate)
#
# DAG (each lane's `# After:` line), lanes 1-9:
#   1 -> 2, 3, 5    2 -> 4, 6    3 -> 4, 7    5 -> 7
#   4, 6, 7, 8 -> 9    9 -> 10..19 (generated)    8 has no parents (independent root)
# Shared dir .scenario/dag/: nodes/N.done ("<run> <start> <end>"); <run> is $SWIM_RUN.
#
# Part of the e2e scenario e2e/scenarios/dag99 (run: e2e/run.sh dag99).
# Never edit this file while it may be running: `swim status` first.
# Do not use `set -e`: failed checks keep going; only gates stop the round.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 8

D=.scenario/dag
N=8
PARENTS=""
NODE_START=$(e2etool now)
export D N PARENTS NODE_START
mkdir -p "$D/nodes"
run "clear own done marker" rm -f "$D/nodes/$N.done"
RUN_ID=$SWIM_RUN
export RUN_ID

H=$D/headers
export H
stage change
gate "reset $H" bash -c 'rm -rf "$H" && mkdir -p "$H"'
run "simulated work (4ms)" sleep 0.004
head_of() { # head_of <name> <url>
  run "HEAD $1" curl -sSI --max-time 15 -A "swim-demo/$SWIM_JOB" -o "$H/$1.txt" -w '%{http_code} %{url_effective}\n' "$2"
}
head_of example https://example.com/
head_of httpbin https://httpbin.org/get
head_of cern    http://info.cern.ch/
gate "headers.md" e2etool headers

stage verify
gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(e2etool now)" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
