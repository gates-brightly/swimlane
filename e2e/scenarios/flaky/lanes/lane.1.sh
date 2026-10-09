#!/usr/bin/env bash
# swim: syntax 2
# Round:   Flaky but enough retries
# After:   
# Owner:   e2e
#
# Part of the e2e scenario e2e/scenarios/flaky (run: e2e/run.sh flaky).
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1
mkdir -p .scenario/flaky
# fail_first K NAME: fail the first K calls for NAME, then succeed (a counter file).
fail_first() { c=".scenario/flaky/$2.count"; n=$(cat "$c" 2>/dev/null || echo 0); n=$((n + 1)); echo $n > "$c"; echo "call $n"; [ $n -gt "$1" ]; }
export -f fail_first 2>/dev/null || true
stage check
run --retry 2 --backoff 0.1s "fails twice" bash -c "$(declare -f fail_first); fail_first 2 one"
summary
