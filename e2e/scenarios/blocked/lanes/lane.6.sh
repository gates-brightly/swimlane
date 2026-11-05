#!/usr/bin/env bash
# swim: syntax 2
# Round:   Reads git, never writes
# After:   
# Owner:   e2e
#
# Part of the e2e scenario e2e/scenarios/blocked (internal/e2e/scenarios_test.go).
_swim_lib=$("${SWIM_BIN:-swim}" _lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 6
stage check
run "git log" git log --oneline -1
run "after the blocked command" true
summary
