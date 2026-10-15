#!/usr/bin/env bash
# swim: syntax 2
# Round:  Tidy up old tables
# Owner:  planner (AI session)
#
# A script that "forgot" its guard: it goes straight for the admin
# password and drops a table.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage snapshot
snapshot "tables before" fakecloud ls

stage check
run "orders_2019 exists" fakecloud rows orders_2019

stage change
FAKECLOUD_ADMIN_PW=$(swim secret get DB_ADMIN_PW)
export FAKECLOUD_ADMIN_PW
run "drop orders_2019" fakecloud drop orders_2019
summary
