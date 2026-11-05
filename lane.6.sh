#!/usr/bin/env swim
# swim: syntax 4
# Round:   CI: fuzz (each target: 20s in CI, 5s locally)
# Round-ID:     5c1a0018-11a7-4c1e-8d00-000000000018
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

stage check
fuzztime=5s
[ "${CI:-}" = true ] && fuzztime=20s
fuzztime=${FUZZTIME:-$fuzztime}
step "fuzz logparse" go test -run '^$' -parallel 2 -fuzz '^FuzzParse$' -fuzztime "$fuzztime" ./internal/logparse
step "fuzz lane" go test -run '^$' -parallel 2 -fuzz '^FuzzParseScript$' -fuzztime "$fuzztime" ./internal/lane
step "fuzz chain" go test -run '^$' -parallel 2 -fuzz '^FuzzDecodeLine$' -fuzztime "$fuzztime" ./internal/chain
