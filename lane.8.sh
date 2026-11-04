#!/usr/bin/env bash
# swim: syntax 4
# Round:   CI: unit tests
# Job:     5c1a0018-11a7-4c1e-8d00-000000000018
# Owner:   swim CI
# After:   1
# Timeout: 20m
# Workspace: live
# Signoff: swim/unit
# swim:lint-ignore-file workspace-live go test reads the whole checkout
#
# Lane 8 of this repo's CI (lane.1.sh lists them all): every package's
# unit tests, after lane 1's checks and alongside the other suites.
# internal/e2e runs in lanes 2-4.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 8

stage check
run "go test (unit)" bash -c 'go test $(go list ./... | grep -v /internal/e2e)'

summary
