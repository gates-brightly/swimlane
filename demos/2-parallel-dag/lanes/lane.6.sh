#!/usr/bin/env bash
# swim: syntax 4
# Round:  Release 2.4.0: announce in #releases
# After:  5
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 6

stage snapshot
snapshot "release notes" echo "2.4.0: faster checkout, new jobs schema"

stage check
gate "api is on 2.4.0" bash -c '[ "$(fakecloud version api)" = 2.4.0 ]'

stage change
run "post to #releases" echo "2.4.0 is live: api, web, worker"
summary
