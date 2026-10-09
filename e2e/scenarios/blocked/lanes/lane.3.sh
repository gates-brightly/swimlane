#!/usr/bin/env bash
# swim: syntax 2
# Round:   Pulls with git -C (refused by the git shim)
# After:   
# Owner:   e2e
#
# Part of the e2e scenario e2e/scenarios/blocked (run: e2e/run.sh blocked).
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 3
stage check
run "read-only git still works" git status --short
git -C . pull
run "after the blocked command" true
summary
