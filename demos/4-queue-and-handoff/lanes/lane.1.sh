#!/usr/bin/env bash
# swim: syntax 2
# Round:  Roll api forward to 2.4.1 (the session-leak fix)
# Owner:  planner (AI session)
# Guards: DEPLOY_ALLOW_API  deploy api 2.4.1 to prod
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage snapshot
snapshot "api version now" fakecloud version api

stage check
gate "api healthy before" fakecloud health api

stage change
if guard DEPLOY_ALLOW_API "deploy api 2.4.1 to prod"; then
  gate "deploy api 2.4.1" fakecloud deploy api 2.4.1 2
fi

stage verify
run "api on 2.4.1" bash -c '[ "$(fakecloud version api)" = 2.4.1 ]'
run "api healthy after" fakecloud health api
summary
