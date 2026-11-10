#!/usr/bin/env bash
# swim: syntax 4
# Round:  Roll api forward to 2.4.1 (the session-leak fix)
# Owner:  planner (AI session)
# Guards: DEPLOY_ALLOW_API  deploy api 2.4.1 to prod
#   why: fixes the 2.4.0 checkout bug
_swim_lib=$("${SWIM_BIN:-swim}" _lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage snapshot
snapshot "api version now" fakecloud version api

stage check
gate "api healthy before" fakecloud health api

stage change
if guard DEPLOY_ALLOW_API; then
  gate "deploy api 2.4.1" fakecloud deploy api 2.4.1 2
fi

stage verify
step "api on 2.4.1" bash -c '[ "$(fakecloud version api)" = 2.4.1 ]'
step "api healthy after" fakecloud health api
summary
