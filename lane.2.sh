#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: lint (build, vet, swim lint, shell syntax)
# Job:     5c1a0012-11a7-4c1e-8d00-000000000012
# Always:  yes
# After:   1
# Owner:   swim CI
# Created: 2026-10-14
# Timeout: 10m
#
# Goal: the tree builds and is clean before the tests spend minutes on it.
# An Always lane, after fmt.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage check
gate "go build" go build ./...
run "go vet" go vet ./...
run "swim lint (the CI rounds)" "$SWIM_BIN" lint --strict
run "shell syntax" bash -c 'for f in scripts/hermetic scripts/release.sh e2e/run.sh e2e/lib/common.sh e2e/lib/fakebin/*; do bash -n "$f" || exit 1; done'

summary
