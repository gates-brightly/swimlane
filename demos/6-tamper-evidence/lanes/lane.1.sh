#!/usr/bin/env bash
# swim: syntax 4
# Round:  Raise the api's connection pool to 50
# Owner:  planner (AI session)
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage snapshot
snapshot "api version" fakecloud version api

stage check
gate "api healthy" fakecloud health api

stage change
run "set pool_size=50" fakecloud deploy api 2.4.0-pool50 1

stage verify
run "api healthy after" fakecloud health api
summary
