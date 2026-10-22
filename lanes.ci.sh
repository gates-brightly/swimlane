#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: fmt, lint, test, race
# Owner:   swim CI
#
# This repo's CI is five group files, so its slow suites run side by side:
#   lane 1  lanes.ci.sh        fmt, lint, unit tests, race (Always: first, every run)
#   lane 2  lanes.e2e.sh       the e2e suite and scenarios
#   lane 3  lanes.fault.sh     fault injection
#   lane 4  lanes.security.sh  the security regression suite
#   lane 6  lanes.fuzz.sh      fuzzing
# Lanes 2-6 wait for lane 1 (After: 1) and run at the same time, so a run
# takes about as long as the slowest suite. swim ci (GitHub Actions) and
# `just ci` run them all; `swim run 2` runs one suite, `swim run 1.2` one job.
# Lane 5 isn't CI's. Security tests fail the build on a violation; they
# never only warn.
#
# Each job starts at its "# swim job 1.K" line. swim loads its library,
# calls lane_init and summary, and runs .swim/groups/lane.1.K.sh.

# swim job 1.1 -----------------------------------------------------------------
# Round:   CI: fmt (gofmt -s)
# Job:     5c1a0011-11a7-4c1e-8d00-000000000011
# Always:  yes
# Timeout: 5m
#
# Goal: every Go file is gofmt -s clean. It only checks; `just fmt` rewrites.
stage check
gate "gofmt -s" bash -c 'out=$(gofmt -s -l .); [ -z "$out" ] || { echo "needs gofmt -s (just fmt):"; echo "$out"; exit 1; }'

# swim job 1.2 -----------------------------------------------------------------
# Round:   CI: lint (build, vet, swim lint, shell syntax)
# Job:     5c1a0012-11a7-4c1e-8d00-000000000012
# Always:  yes
# Timeout: 10m
#
# Goal: the tree builds and is clean before the tests spend minutes on it.
stage check
gate "go build" go build ./...
run "go vet" go vet ./...
run "swim lint (the CI rounds)" "$SWIM_BIN" lint --strict
run "shell syntax" bash -c 'for f in scripts/hermetic scripts/release.sh scripts/next-tag.sh e2e/run.sh e2e/lib/common.sh e2e/lib/fakebin/*; do bash -n "$f" || exit 1; done'

# swim job 1.3 -----------------------------------------------------------------
# Round:   CI: test (unit)
# Job:     5c1a0013-11a7-4c1e-8d00-000000000013
# Always:  yes
# Timeout: 15m
#
# Goal: every package's unit tests pass (internal/e2e runs in lane 2).
# scripts/hermetic keeps this lane's own swim environment out of the tests.
stage check
run "go test (unit)" scripts/hermetic bash -c 'go test $(go list ./... | grep -v /internal/e2e)'

# swim job 1.4 -----------------------------------------------------------------
# Round:   CI: race (go test -race, unit)
# Job:     5c1a0015-11a7-4c1e-8d00-000000000015
# Always:  yes
# Timeout: 15m
#
# Goal: no data race in any package's unit tests (the e2e suite drives the
# swim binary, which -race here doesn't instrument).
stage check
run "go test -race (unit)" scripts/hermetic bash -c 'go test -race $(go list ./... | grep -v /internal/e2e)'
