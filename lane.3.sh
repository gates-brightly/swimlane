#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: test (unit)
# Job:     5c1a0013-11a7-4c1e-8d00-000000000013
# Always:  yes
# After:   2
# Owner:   swim CI
# Created: 2026-10-14
# Timeout: 15m
#
# Goal: every package's unit tests pass (internal/e2e runs in lane.4.sh). An
# Always lane, after lint. scripts/hermetic keeps this lane's own swim
# environment out of the tests.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 3

stage check
run "go test (unit)" scripts/hermetic bash -c 'go test $(go list ./... | grep -v /internal/e2e)'

summary
