#!/usr/bin/env bash
# swim: syntax 2
# Round: ci demo: check the toolchain
# Job:     c1d3e000-0001-4000-8000-000000000001
# Owner:   swim e2e
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
stage check
gate "bash is here" bash --version
run "go is here" go version
stage verify
run "repo is a git checkout" git rev-parse --is-inside-work-tree
summary
