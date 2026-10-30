#!/usr/bin/env bash
# swim: syntax 2
# Round:   Tears down with a literal terraform destroy (refused before it starts)
# After:   
# Owner:   e2e
#
# Part of the e2e scenario e2e/scenarios/blocked (run: e2e/run.sh blocked).
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1
stage check
run "tear down" terraform destroy -auto-approve
run "after the blocked command" true
summary
