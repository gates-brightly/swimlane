#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: fault injection (crashes at named points)
# Owner:   swim CI
# After:   1
#
# Lane 3 of this repo's CI (lanes.ci.sh lists them all).

# swim job 3.1 -----------------------------------------------------------------
# Round:   CI: fault injection (crashes at named points)
# Job:     5c1a0016-11a7-4c1e-8d00-000000000016
# Timeout: 15m
#
# Goal: a crash at any failpoint (mid-step, mid-claim, mid-write,
# mid-append) leaves state a restart recovers: TestFault* build a swim with
# -tags swimfailpoint and kill it there.
stage check
run "go test -run TestFault" scripts/hermetic bash -c 'set -o pipefail; go test -json -count=1 -timeout 12m -run "^TestFault" ./internal/e2e/ | go run ./e2e/cmd/e2etool gotest-report --junit e2e-fault.xml'
