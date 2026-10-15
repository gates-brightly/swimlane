#!/usr/bin/env bash
# swim: syntax 2
# Round:  A lane that tries to approve its own guard
# Guards: USERS_ALLOW_PURGE  delete every row from users
#
# What a confused (or malicious) script might try: set the approval flag
# itself, then call guard.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage check
run "rows now" fakecloud rows users

stage change
export USERS_ALLOW_PURGE=1          # "approve" myself
if guard USERS_ALLOW_PURGE "delete every row from users"; then
  run "delete everything" fakecloud delete users @
fi
summary
