#!/usr/bin/env bash
# swim: syntax 4
# Round:   CI: checks (fmt, build, vet, lint, shell syntax)
# Job:     5c1a0011-11a7-4c1e-8d00-000000000011
# Owner:   swim CI
# Timeout: 10m
# Workspace: live
# Signoff: swim/ci
# swim:lint-ignore-file workspace-live go build and go test read the whole checkout
#
# This repo's CI is seven lanes, each its own job, so the slow suites run
# side by side and a red run names the one that failed:
#   lane 1  checks: fmt, build, vet, lint, shell syntax (first, seconds)
#   lane 2  the e2e suite and scenarios
#   lane 3  fault injection
#   lane 4  the security regression suite
#   lane 6  fuzzing
#   lane 8  unit tests
#   lane 9  unit tests under -race
# The others wait for lane 1 (After: 1) and run at the same time, so a run
# takes about as long as the slowest suite. swim ci (GitHub Actions) and
# `just ci` run them all; `swim run 8` runs one. Lanes 5 and 7 aren't CI's.
# Security tests fail the build on a violation; they never only warn.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1

stage check
run "gofmt -s" bash -c 'out=$(gofmt -s -l .); [ -z "$out" ] || { echo "needs gofmt -s (just fmt):"; echo "$out"; exit 1; }'
gate "go build" go build ./...
run "go vet" go vet ./...
run "swim lint (the CI rounds)" "$SWIM_BIN" lint --strict
run "shell syntax" bash -c 'for f in scripts/hermetic scripts/next-tag.sh scripts/mirror-tags.sh scripts/tag-remotes.sh scripts/release-notes.sh e2e/run.sh e2e/lib/common.sh e2e/lib/fakebin/*; do bash -n "$f" || exit 1; done; sh -n scripts/install.sh'
# The unit tests are lanes 8 (plain) and 9 (-race); internal/e2e is lanes 2-4.

summary
