#!/usr/bin/env swim
# swim: syntax 4
# Round:   CI: unit tests
# Round-ID:     5c1a0015-11a7-4c1e-8d00-000000000015
# Owner:   swim CI
# After:   1
# Timeout: 20m
# Workspace: live
# swim:lint-ignore-file workspace-live go test reads the whole checkout
#
# Lane 8 of this repo's CI (lane.1.sh lists them all): every package's
# unit tests, after lane 1's checks and alongside the other suites.
# internal/e2e runs in lanes 2-4.

stage check
step "go test (unit)" bash -c 'go test $(go list ./... | grep -v /internal/e2e)'
