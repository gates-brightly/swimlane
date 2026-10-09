#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: lint (build, gofmt, vet, swim lint, shell syntax)
# Job:     5c1a0001-11a7-4c1e-8d00-000000000001
# Owner:   swim CI
# Created: 2026-10-09
# Timeout: 10m
#
# Goal: the tree builds and is clean before the tests spend minutes on it.
# lane.2.sh (unit) and lane.3.sh (e2e) wait for this round to pass.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage check
gate "go build" go build ./...
gate "gofmt -s" bash -c 'out=$(gofmt -s -l .); [ -z "$out" ] || { echo "needs gofmt -s:"; echo "$out"; exit 1; }'
run "go vet" go vet ./...
run "swim lint (the CI rounds)" "$SWIM_BIN" lint --strict
run "shell syntax" bash -c 'for f in scripts/hermetic scripts/release.sh e2e/run.sh e2e/lib/common.sh e2e/lib/fakebin/*; do bash -n "$f" || exit 1; done'

summary
