#!/usr/bin/env bash
# swim: syntax 2
# Round:   Waits on lane 1
# After:   1
# Owner:   e2e
#
# Part of the e2e scenario e2e/scenarios/blocked (run: e2e/run.sh blocked).
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 4
stage check
run "x" true
run "after the blocked command" true
summary
