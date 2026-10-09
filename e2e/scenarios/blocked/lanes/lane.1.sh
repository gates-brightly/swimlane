#!/usr/bin/env bash
# swim: syntax 2
# Round:   Publishes with a literal git push (refused before it starts)
# After:   
# Owner:   e2e
#
# Part of the e2e scenario e2e/scenarios/blocked (run: e2e/run.sh blocked).
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1
stage check
run "push results" git push origin main
run "after the blocked command" true
summary
