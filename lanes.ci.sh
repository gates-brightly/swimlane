#!/usr/bin/env bash
# swim: syntax 2
# This repo's CI rounds, in one group file. fmt, lint and test are Always
# lanes: swim ci (GitHub Actions) and swim all run them first, in that order,
# before every other lane; e2e runs once tests pass. Lanes 5 and up are free
# for rounds. `make ci` runs the same rounds locally.
#
# Each lane starts at its "# swim lane N" line. swim loads its library,
# calls lane_init and summary, and runs the generated .swim/groups/lane.N.sh.

# swim lane 1 -----------------------------------------------------------------
# Round:   CI: fmt (gofmt -s)
# Job:     5c1a0011-11a7-4c1e-8d00-000000000011
# Always:  yes
# Owner:   swim CI
# Timeout: 5m
#
# Goal: every Go file is gofmt -s clean. It only checks; `make fmt` rewrites.
stage check
gate "gofmt -s" bash -c 'out=$(gofmt -s -l .); [ -z "$out" ] || { echo "needs gofmt -s (make fmt):"; echo "$out"; exit 1; }'

# swim lane 2 -----------------------------------------------------------------
# Round:   CI: lint (build, vet, swim lint, shell syntax)
# Job:     5c1a0012-11a7-4c1e-8d00-000000000012
# Always:  yes
# After:   1
# Owner:   swim CI
# Timeout: 10m
#
# Goal: the tree builds and is clean before the tests spend minutes on it.
stage check
gate "go build" go build ./...
run "go vet" go vet ./...
run "swim lint (the CI rounds)" "$SWIM_BIN" lint --strict
run "shell syntax" bash -c 'for f in scripts/hermetic scripts/release.sh e2e/run.sh e2e/lib/common.sh e2e/lib/fakebin/*; do bash -n "$f" || exit 1; done'

# swim lane 3 -----------------------------------------------------------------
# Round:   CI: test (unit)
# Job:     5c1a0013-11a7-4c1e-8d00-000000000013
# Always:  yes
# After:   2
# Owner:   swim CI
# Timeout: 15m
#
# Goal: every package's unit tests pass (internal/e2e runs in lane 4).
# scripts/hermetic keeps this lane's own swim environment out of the tests.
stage check
run "go test (unit)" scripts/hermetic bash -c 'go test $(go list ./... | grep -v /internal/e2e)'

# swim lane 4 -----------------------------------------------------------------
# Round:   CI: e2e (Go suite and e2e/ scenarios)
# Job:     5c1a0014-11a7-4c1e-8d00-000000000014
# After:   3
# Owner:   swim CI
# Timeout: 30m
#
# Goal: swim end to end: the Go e2e suite builds its own swim and drives it
# in scratch repos, and runs every e2e/scenarios/ scenario as a subtest.
# scripts/hermetic strips this lane's environment (SWIM_*, the git shim, CI
# variables) first. A plain round, not an Always lane: too slow to be setup.
stage check
run "go test ./internal/e2e" scripts/hermetic go test -count=1 ./internal/e2e/...
