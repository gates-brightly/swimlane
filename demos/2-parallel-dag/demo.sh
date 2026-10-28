#!/usr/bin/env bash
# Demo 2: a release as a DAG. Independent lanes run side by side, dependent
# ones wait, a lock keeps two migrations apart, and a failure stops only
# what depends on it. Fix it and rerun: what passed isn't redone.
. "$(dirname "$0")/../_lib/common.sh"
demo_init 2-parallel-dag

for s in api web worker; do fakecloud deploy $s 2.3.9 0 >/dev/null; done
demo_lanes
demo_commit "release 2.4.0 rounds"

scene "1. The plan"
say "Six lanes for one release. Each declares what it waits for, and two" \
    "share a lock because both migrate the database:"
show 'grep -H -E "^# (Round|After|Locks):" lane.*.sh'
pause
say "Build first, three deploys in parallel, then smoke test, then announce."
pause

scene "2. Run it, with one service broken"
say "Someone broke the worker's health check. Watch what swim does with that."
show 'fakecloud break worker'
if [ -t 1 ] && [ -z "${DEMO_AUTO:-}" ]; then
  show 'swim all --parallel 4'
else
  show 'swim all --parallel 4 --plain'
fi
pause
show 'swim status'
say "api and web are deployed. worker FAILED, so smoke test and announce were" \
    "SKIPPED rather than run against a half-finished release."
pause

scene "3. Fix it and carry on"
show 'fakecloud fix worker'
say "Only the failed lane and what waits on it are left. Build, api and web aren't redone."
pause
if [ -t 1 ] && [ -z "${DEMO_AUTO:-}" ]; then
  show 'swim all --parallel 4'
else
  show 'swim all --parallel 4 --plain'
fi
show 'fakecloud ls'
demo_done
