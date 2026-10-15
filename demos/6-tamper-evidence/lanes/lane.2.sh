#!/usr/bin/env bash
# swim: syntax 2
# Round:  Raise the worker's connection pool to 50
# Owner:  planner (AI session)
# After:  1
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage snapshot
snapshot "worker version" fakecloud version worker

stage check
gate "worker healthy" fakecloud health worker

stage change
run "set pool_size=50" fakecloud deploy worker 2.4.0-pool50 1

stage verify
run "worker healthy after" fakecloud health worker
summary
