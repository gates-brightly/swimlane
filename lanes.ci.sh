#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: fmt, lint, test, e2e
# Owner:   swim CI
#
# This repo's CI: lane 1, as one group file. Its jobs run in order, and the
# first failure skips the rest. fmt, lint and test (1.1-1.3) are Always:
# swim ci (GitHub Actions) and swim all run them first, every time, and
# every other lane waits for them; e2e (1.4) runs after them, alongside the
# other lanes. `swim run 1.2` runs one job; `make ci` runs them all.
#
# Each job starts at its "# swim job 1.K" line. swim loads its library,
# calls lane_init and summary, and runs .swim/groups/lane.1.K.sh.

# swim job 1.1 -----------------------------------------------------------------
# Round:   CI: fmt (gofmt -s)
# Job:     5c1a0011-11a7-4c1e-8d00-000000000011
# Always:  yes
# Timeout: 5m
#
# Goal: every Go file is gofmt -s clean. It only checks; `make fmt` rewrites.
stage check
gate "gofmt -s" bash -c 'out=$(gofmt -s -l .); [ -z "$out" ] || { echo "needs gofmt -s (make fmt):"; echo "$out"; exit 1; }'

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
# Goal: every package's unit tests pass (internal/e2e runs in 1.4).
# scripts/hermetic keeps this lane's own swim environment out of the tests.
stage check
run "go test (unit)" scripts/hermetic bash -c 'go test $(go list ./... | grep -v /internal/e2e)'

# swim job 1.4 -----------------------------------------------------------------
# Round:   CI: e2e (Go suite and e2e/ scenarios)
# Job:     5c1a0014-11a7-4c1e-8d00-000000000014
# Timeout: 30m
#
# Goal: swim end to end: the Go e2e suite builds its own swim and drives it
# in scratch repos, and runs every e2e/scenarios/ scenario as a subtest.
# scripts/hermetic strips this lane's environment (SWIM_*, the git shim, CI
# variables) first. Not Always: too slow to be setup.
# go test -json through e2etool gotest-report: the step's last lines name
# each failed test with its output (swim ci puts them in the annotation),
# and e2e.xml has a JUnit case per Go test. E2E_KEEP_FAILED (the workflow
# sets it) keeps a failed scenario's scratch repo there.
stage check
run "go test ./internal/e2e" scripts/hermetic bash -c 'set -o pipefail; go test -json -count=1 ./internal/e2e/... | go run ./e2e/cmd/e2etool gotest-report --junit e2e.xml'
