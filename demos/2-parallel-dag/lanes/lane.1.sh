#!/usr/bin/env bash
# swim: syntax 4
# Round:  Release 2.4.0: build and push images
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage snapshot
snapshot "registry tags" echo "2.3.9 2.3.8 2.3.7"

stage check
gate "tests pass" sleep 1

stage change
run "build images" sleep 2
run "push images" sleep 1
summary
