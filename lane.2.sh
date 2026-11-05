#!/usr/bin/env swim
# swim: syntax 4
# Round:   CI: e2e (Go suite and e2e/ scenarios)
# Round-ID:     5c1a0014-11a7-4c1e-8d00-000000000014
# Owner:   swim CI
# After:   1
# Timeout: 30m
# Workspace: live
# swim:lint-ignore-file workspace-live go test reads the whole checkout
#
# Lane 2 of this repo's CI (lane.1.sh lists them all). The Go e2e suite
# builds its own swim and drives it in scratch repos, the whole-workflow
# scenarios (e2e/README.md) included. Tests run in parallel (each has its
# own repo; SWIM_E2E_SERIAL=1 runs them one at a time). -timeout 25m: go
# test's default 10m panics the whole package on a slow runner.
# go test -json through e2etool gotest-report: the step's last lines name
# each failed test with its output (swim ci puts them in the annotation),
# and e2e.xml has a JUnit case per Go test. The fault and security suites
# are lanes 3 and 4.

stage check
# On a CI runner this lane shares 3-4 CPUs with the others: the suites'
# timed margins scale by E2E_SLOW (internal/e2e/e2e_test.go, "Timing").
[ "${CI:-}" = true ] && export E2E_SLOW=${E2E_SLOW:-3}
step "go test ./internal/e2e" bash -c 'set -o pipefail; go test -json -count=1 -timeout 25m -skip "^(TestSecurity|TestFault|TestNoSecretLeaks)" ./internal/e2e/... | go run ./e2e/cmd/e2etool gotest-report --junit e2e.xml'
