#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: fmt, lint, test, race, e2e, fault, security, fuzz
# Owner:   swim CI
#
# This repo's CI: lane 1, as one group file. Its jobs run in order, and the
# first failure skips the rest. fmt, lint, test and race (1.1-1.4) are
# Always jobs: swim ci (GitHub Actions) and swim all run them first, every
# time, and every other lane waits for them; then e2e (1.5), fault injection
# (1.6), the security suite (1.7) and fuzzing (1.8), each its own job so a
# failure names the gate. `swim run 1.7` runs one job; `make ci` runs them
# all. Security tests fail the build on a violation; they never only warn.
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
# Round:   CI: race (go test -race, unit)
# Job:     5c1a0015-11a7-4c1e-8d00-000000000015
# Always:  yes
# Timeout: 15m
#
# Goal: no data race in any package's unit tests (the e2e suite drives the
# swim binary, which -race here doesn't instrument).
stage check
run "go test -race (unit)" scripts/hermetic bash -c 'go test -race $(go list ./... | grep -v /internal/e2e)'

# swim job 1.5 -----------------------------------------------------------------
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
# sets it) keeps a failed scenario's scratch repo there. The fault and
# security suites run in their own jobs below.
stage check
run "go test ./internal/e2e" scripts/hermetic bash -c 'set -o pipefail; go test -json -count=1 -skip "^(TestSecurity|TestFault|TestNoSecretLeaks)" ./internal/e2e/... | go run ./e2e/cmd/e2etool gotest-report --junit e2e.xml'

# swim job 1.6 -----------------------------------------------------------------
# Round:   CI: fault injection (crashes at named points)
# Job:     5c1a0016-11a7-4c1e-8d00-000000000016
# Timeout: 15m
#
# Goal: a crash at any failpoint (mid-step, mid-claim, mid-write,
# mid-append) leaves state a restart recovers: TestFault* build a swim with
# -tags swimfailpoint and kill it there.
stage check
run "go test -run TestFault" scripts/hermetic bash -c 'set -o pipefail; go test -json -count=1 -run "^TestFault" ./internal/e2e/ | go run ./e2e/cmd/e2etool gotest-report --junit e2e-fault.xml'

# swim job 1.7 -----------------------------------------------------------------
# Round:   CI: security (the security regression suite)
# Job:     5c1a0017-11a7-4c1e-8d00-000000000017
# Timeout: 15m
#
# Goal: every claim in docs/trust-model.md's claims table holds: a lane
# can't approve itself (T3, T28), a changed manifest input stales its
# approval, the chain catches tampering, no secret leaks. A violation fails
# the build.
stage check
run "go test -run TestSecurity" scripts/hermetic bash -c 'set -o pipefail; go test -json -count=1 -run "^(TestSecurity|TestNoSecretLeaks)$" ./internal/e2e/ | go run ./e2e/cmd/e2etool gotest-report --junit e2e-security.xml'

# swim job 1.8 -----------------------------------------------------------------
# Round:   CI: fuzz (each target, 20s)
# Job:     5c1a0018-11a7-4c1e-8d00-000000000018
# Timeout: 10m
#
# Goal: the parsers of what lanes write (logs, script headers, chain lines,
# the lane socket) don't panic on any input. A crash the fuzzer finds lands
# in that package's testdata/fuzz: commit it with the fix.
stage check
run "fuzz logparse" scripts/hermetic go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 20s ./internal/logparse
run "fuzz lane" scripts/hermetic go test -run '^$' -fuzz '^FuzzParseScript$' -fuzztime 20s ./internal/lane
run "fuzz chain" scripts/hermetic go test -run '^$' -fuzz '^FuzzDecodeLine$' -fuzztime 20s ./internal/chain
run "fuzz lane socket" scripts/hermetic go test -run '^$' -fuzz '^FuzzLaneRequests$' -fuzztime 20s ./internal/cli
