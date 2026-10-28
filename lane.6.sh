#!/usr/bin/env bash
# swim: syntax 4
# Round:   CI: fuzz (each target: 20s in CI, 5s locally)
# Job:     5c1a0018-11a7-4c1e-8d00-000000000018
# Owner:   swim CI
# After:   1
# Timeout: 10m
# Workspace: live
# Signoff: swim/fuzz
# swim:lint-ignore-file workspace-live go test reads the whole checkout
#
# Lane 6 of this repo's CI (lane.1.sh lists them all). The parsers of what
# lanes write (logs, script headers, chain lines) don't panic on any input.
# A crash the fuzzer finds lands in that package's testdata/fuzz: commit it
# with the fix. Each target runs 20s in CI (CI=true, set by GitHub Actions
# and GitLab) and 5s locally, so `swim all` stays quick; FUZZTIME overrides
# both. Two fuzz workers (-parallel 2), not one per CPU: this lane runs
# beside e2e, the slowest lane, and shouldn't take the runner's CPUs (T32).

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 6

stage check
fuzztime=5s
[ "${CI:-}" = true ] && fuzztime=20s
fuzztime=${FUZZTIME:-$fuzztime}
run "fuzz logparse" scripts/hermetic go test -run '^$' -parallel 2 -fuzz '^FuzzParse$' -fuzztime "$fuzztime" ./internal/logparse
run "fuzz lane" scripts/hermetic go test -run '^$' -parallel 2 -fuzz '^FuzzParseScript$' -fuzztime "$fuzztime" ./internal/lane
run "fuzz chain" scripts/hermetic go test -run '^$' -parallel 2 -fuzz '^FuzzDecodeLine$' -fuzztime "$fuzztime" ./internal/chain

summary
