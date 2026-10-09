#!/usr/bin/env bash
# swim: syntax 2
# Round: DAG root: fetch pages, aggregate
# Job:   6885ada8-4cda-4947-98d6-17f37df32cea
# After: 
# Lane:  swim 1    Written: 2026-10-09
#
# Goal:
#   curl four small public pages and aggregate them into combined.html.
#
# Steps:
#   0. Clear this node's done marker; 
#   1. Gate: curl, e2etool; fetch four pages (a flaky site doesn't stop the round)
#   2. Gate: aggregate into combined.html (atomic)
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/1.done
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
lane_init 1

D=.scenario/dag
N=1
PARENTS=""
NODE_START=$(e2etool now)
export D N PARENTS NODE_START
mkdir -p "$D/nodes"
run "clear own done marker" rm -f "$D/nodes/$N.done"
RUN_ID=$SWIM_RUN
export RUN_ID

P=$D/pages
export P
stage check
gate "curl available" curl --version
gate "e2etool available" e2etool version
stage change
# swim:lint-ignore destructive scratch dir under .scenario/, recreated every run
gate "reset $P" bash -c 'rm -rf "$P" && mkdir -p "$P"'
fetch() { # fetch <name> <url>
  run "fetch $1" curl -fsSL --max-time 20 -A "swim-demo/$SWIM_JOB" -o "$P/$1.html" -w '%{http_code} %{size_download}B %{url_effective}\n' "$2"
}
fetch example https://example.com/
fetch cern    http://info.cern.ch/hypertext/WWW/TheProject.html
fetch httpbin https://httpbin.org/html
fetch iana    https://www.iana.org/help/example-domains
gate "at least one page fetched" bash -c 'ls "$P"/*.html >/dev/null 2>&1 && ls -l "$P"'

gate "aggregate into combined.html" e2etool aggregate


stage verify
gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(e2etool now)" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
