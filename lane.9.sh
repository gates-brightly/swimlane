#!/usr/bin/env bash
# swim: syntax 4
# Round:   CI: unit tests under -race
# Job:     5c1a0019-11a7-4c1e-8d00-000000000019
# Owner:   swim CI
# After:   1
# Timeout: 25m
# Workspace: live
# Signoff: swim/race
# swim:lint-ignore-file workspace-live go test reads the whole checkout
#
# Lane 9 of this repo's CI (lane.1.sh lists them all): the unit tests
# again under the race detector, after lane 1's checks and alongside the
# other suites (lane 8 runs them without it). The e2e suite drives the swim
# binary, which -race here doesn't instrument, so it isn't run here.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 9

stage check
run "go test -race (unit)" scripts/hermetic bash -c 'go test -race $(go list ./... | grep -v /internal/e2e)'

summary
