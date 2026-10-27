#!/usr/bin/env bash
# swim: syntax 4
# Round:   CI: fault injection (crashes at named points)
# Job:     5c1a0016-11a7-4c1e-8d00-000000000016
# Owner:   swim CI
# After:   1
# Timeout: 15m
# Workspace: live
# swim:lint-ignore-file workspace-live go test reads the whole checkout
#
# Lane 3 of this repo's CI (lane.1.sh lists them all). A crash at any
# failpoint (mid-step, mid-claim, mid-write, mid-append) leaves state a
# restart recovers: TestFault* build a swim with -tags swimfailpoint and
# kill it there.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 3

stage check
# On a CI runner this lane shares 3-4 CPUs with the others: the suites'
# timed margins scale by E2E_SLOW (internal/e2e/e2e_test.go, "Timing").
[ "${CI:-}" = true ] && export E2E_SLOW=${E2E_SLOW:-3}
run "go test -run TestFault" scripts/hermetic bash -c 'set -o pipefail; go test -json -count=1 -timeout 12m -run "^TestFault" ./internal/e2e/ | go run ./e2e/cmd/e2etool gotest-report --junit e2e-fault.xml'

summary
