#!/usr/bin/env bash
# swim: syntax 4
# Round:  Release 2.4.0: deploy api (runs a db migration)
# After:  1
# Locks:  db-migrations
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage snapshot
snapshot "api version now" fakecloud version api

stage check
gate "image api:2.4.0 pushed" echo "registry.local/api:2.4.0"

stage change
run "migrate orders schema" sleep 2
gate "deploy api" fakecloud deploy api 2.4.0 2

stage verify
run "api healthy" fakecloud health api
summary
