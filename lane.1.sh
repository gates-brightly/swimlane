#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: fmt, lint, test, race
# Job:     5c1a0011-11a7-4c1e-8d00-000000000011
# Owner:   swim CI
# Timeout: 45m
# Workspace: live
# swim:lint-ignore-file workspace-live go build and go test read the whole checkout
#
# This repo's CI is five lanes, so its slow suites run side by side:
#   lane 1  fmt, lint, unit tests, race (first)
#   lane 2  the e2e suite and scenarios
#   lane 3  fault injection
#   lane 4  the security regression suite
#   lane 6  fuzzing
# Lanes 2-6 wait for lane 1 (After: 1) and run at the same time, so a run
# takes about as long as the slowest suite. swim ci (GitHub Actions) and
# `just ci` run them all; `swim run 2` runs one suite. Lanes 5 and 7 aren't
# CI's. Security tests fail the build on a violation; they never only warn.
# scripts/hermetic keeps this lane's own swim environment out of the tests.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1

stage check
run "gofmt -s" bash -c 'out=$(gofmt -s -l .); [ -z "$out" ] || { echo "needs gofmt -s (just fmt):"; echo "$out"; exit 1; }'
gate "go build" go build ./...
run "go vet" go vet ./...
run "swim lint (the CI rounds)" "$SWIM_BIN" lint --strict
run "shell syntax" bash -c 'for f in scripts/hermetic scripts/release.sh scripts/next-tag.sh e2e/run.sh e2e/lib/common.sh e2e/lib/fakebin/*; do bash -n "$f" || exit 1; done'
# internal/e2e runs in lanes 2-4.
run "go test (unit)" scripts/hermetic bash -c 'go test $(go list ./... | grep -v /internal/e2e)'
# The e2e suite drives the swim binary, which -race here doesn't instrument.
run "go test -race (unit)" scripts/hermetic bash -c 'go test -race $(go list ./... | grep -v /internal/e2e)'

summary
