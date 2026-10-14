#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: e2e (Go suite and e2e/ scenarios)
# Job:     5c1a0014-11a7-4c1e-8d00-000000000014
# After:   3
# Owner:   swim CI
# Created: 2026-10-14
# Timeout: 30m
#
# Goal: swim end to end: the Go e2e suite builds its own swim and drives it
# in scratch repos, and runs every e2e/scenarios/ scenario as a subtest.
# This is swim testing swim from inside a swim lane: scripts/hermetic strips
# this lane's environment (SWIM_*, the git shim, CI variables) first. A plain
# round under the Always lanes (fmt, lint, test): too slow to be setup.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 4

stage check
run "go test ./internal/e2e" scripts/hermetic go test -count=1 ./internal/e2e/...

summary
