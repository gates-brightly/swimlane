#!/usr/bin/env bash
# swim: syntax 2
# Round:  Release 2.4.0: deploy web
# After:  1
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 3

stage snapshot
snapshot "web version now" fakecloud version web

stage check
gate "image web:2.4.0 pushed" echo "registry.local/web:2.4.0"

stage change
gate "deploy web" fakecloud deploy web 2.4.0 3

stage verify
run "web healthy" fakecloud health web
summary
