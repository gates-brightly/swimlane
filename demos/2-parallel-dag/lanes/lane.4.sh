#!/usr/bin/env bash
# swim: syntax 2
# Round:  Release 2.4.0: deploy worker (runs a db migration)
# After:  1
# Locks:  db-migrations
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 4

stage snapshot
snapshot "worker version now" fakecloud version worker

stage check
gate "image worker:2.4.0 pushed" echo "registry.local/worker:2.4.0"

stage change
run "migrate jobs schema" sleep 1
gate "deploy worker" fakecloud deploy worker 2.4.0 2

stage verify
run "worker healthy" fakecloud health worker
summary
