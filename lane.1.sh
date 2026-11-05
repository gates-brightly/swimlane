#!/usr/bin/env swim
# swim: syntax 4
# Round:   CI: checks (fmt, build, vet, deadcode, lint, shell syntax)
# Round-ID:     5c1a0011-11a7-4c1e-8d00-000000000011
# Owner:   swim CI
# Timeout: 10m
# Workspace: live
# Includes: lib/swim/tool.sh
# swim:lint-ignore-file workspace-live go build and go test read the whole checkout
#
# This repo's CI is seven lanes, each its own job, so the slow suites run
# side by side and a red run names the one that failed:
#   lane 1  checks: fmt, build, vet, deadcode, lint, shell syntax (first, seconds)
#   lane 2  the e2e suite and scenarios
#   lane 3  fault injection
#   lane 4  the security regression suite
#   lane 6  fuzzing
#   lane 8  unit tests
#   lane 9  unit tests under -race
# The others wait for lane 1 (After: 1) and run at the same time, so a run
# takes about as long as the slowest suite. swim ci (GitHub Actions) and
# `just ci` run them all; `swim run 8` runs one. Lanes 5 and 7 aren't CI's.
# Security tests fail the build on a violation; they never only warn.

source lib/swim/tool.sh
stage check
# The Go go.mod names (major.minor), as setup-go and the flake install it:
# another Go fails here, by name, not as build errors in every lane after.
tool_gate go "$(sed -n 's/^go \([0-9]*\.[0-9]*\).*/\1/p' go.mod)"
step "gofmt -s" bash -c 'out=$(gofmt -s -l .); [ -z "$out" ] || { echo "needs gofmt -s (just fmt):"; echo "$out"; exit 1; }'
gate "go build" go build ./...
step "go vet" go vet ./...
step "deadcode" bash -c 'out=$(go run golang.org/x/tools/cmd/deadcode@v0.52.0 ./cmd/swim) || exit 1; [ -z "$out" ] || { echo "nothing in swim reaches these:"; echo "$out"; exit 1; }'
step "swim lint (the CI rounds)" "$SWIM_BIN" lint --strict
step "shell syntax" bash -c 'for f in $(git ls-files "*.sh" e2e/lib/fakebin demos/_lib/bin); do bash -n "$f" || exit 1; done; sh -n scripts/install.sh'
# The unit tests are lanes 8 (plain) and 9 (-race); internal/e2e is lanes 2-4.
