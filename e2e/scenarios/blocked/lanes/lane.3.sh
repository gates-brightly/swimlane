#!/usr/bin/env bash
# swim: syntax 2
# Round:   Commits with git (swim blocks no git by itself)
# After:   
# Owner:   e2e
#
# Part of the e2e scenario e2e/scenarios/blocked (run: e2e/run.sh blocked).
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 3
stage check
export GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@example.com GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@example.com
run "commit" git -C "$SWIM_ROOT" commit -q --allow-empty -m "from lane 3"
git -C "$SWIM_ROOT" commit -q --allow-empty -m "from lane 3, outside a step"
run "after the commits" true
summary
