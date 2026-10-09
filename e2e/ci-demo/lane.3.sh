#!/usr/bin/env bash
# swim: syntax 2
# Round: ci demo: parallel with lane 2
# Job:     c1d3e000-0003-4000-8000-000000000003
# After:   1
# Owner:   swim e2e
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 3
stage check
run "count lane scripts" bash -c 'ls lane.*.sh | wc -l'
run --retry 1 --backoff 1s "a retried step" true
summary
