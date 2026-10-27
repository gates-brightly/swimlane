#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: security (the security regression suite)
# Job:     5c1a0017-11a7-4c1e-8d00-000000000017
# Owner:   swim CI
# After:   1
# Timeout: 15m
# Workspace: live
# swim:lint-ignore-file workspace-live go test reads the whole checkout
#
# Lane 4 of this repo's CI (lane.1.sh lists them all). Every claim in
# docs/trust-model.md's claims table holds: what swim run can refuse it
# refuses (T3, T28), what it can't swim verify fails, a changed manifest
# input stales its approval, the chain catches tampering, no secret leaks.
# A violation fails the build.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 4

stage check
# On a CI runner this lane shares 3-4 CPUs with the others: the suites'
# timed margins scale by E2E_SLOW (internal/e2e/e2e_test.go, "Timing").
[ "${CI:-}" = true ] && export E2E_SLOW=${E2E_SLOW:-3}
run "go test -run TestSecurity" scripts/hermetic bash -c 'set -o pipefail; go test -json -count=1 -timeout 12m -run "^(TestSecurity|TestNoSecretLeaks)$" ./internal/e2e/ | go run ./e2e/cmd/e2etool gotest-report --junit e2e-security.xml'

summary
