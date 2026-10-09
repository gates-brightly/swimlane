#!/usr/bin/env bash
# swim: syntax 2
# Round:   Commits through a command built at runtime (refused at the step)
# After:   
# Owner:   e2e
#
# Part of the e2e scenario e2e/scenarios/blocked (run: e2e/run.sh blocked).
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 2
stage check
CMD="git com""mit -m results"
gate "commit results" bash -c "$CMD"
run "after the blocked command" true
summary
