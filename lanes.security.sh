#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: security (the security regression suite)
# Owner:   swim CI
# After:   1
#
# Lane 4 of this repo's CI (lanes.ci.sh lists them all).

# swim job 4.1 -----------------------------------------------------------------
# Round:   CI: security (the security regression suite)
# Job:     5c1a0017-11a7-4c1e-8d00-000000000017
# Timeout: 15m
#
# Goal: every claim in docs/trust-model.md's claims table holds: a lane
# can't approve itself (T3, T28), a changed manifest input stales its
# approval, the chain catches tampering, no secret leaks. A violation fails
# the build.
stage check
run "go test -run TestSecurity" scripts/hermetic bash -c 'set -o pipefail; go test -json -count=1 -timeout 12m -run "^(TestSecurity|TestNoSecretLeaks)$" ./internal/e2e/ | go run ./e2e/cmd/e2etool gotest-report --junit e2e-security.xml'
