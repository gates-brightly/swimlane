#!/usr/bin/env bash
# swim: syntax 4
# Round:  Purge test accounts from the prod users table
# Owner:  planner (AI session)
# Guards: USERS_ALLOW_PURGE  delete the @test.example rows from users
#
# Goal: the signup load test left accounts behind in prod. Delete them, and
# only them. Everything before `stage change` is read-only.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage snapshot
snapshot "users before" fakecloud select users

stage check
gate "users table exists" fakecloud rows users
run  "test accounts to delete" fakecloud select users @test.example

stage change
if guard USERS_ALLOW_PURGE "delete the @test.example rows from users"; then
  run "delete test accounts" fakecloud delete users @test.example
fi

stage verify
run "no test accounts left" bash -c '[ -z "$(fakecloud select users @test.example)" ]'
run "real users untouched" bash -c '[ "$(fakecloud rows users)" -ge 3 ]'
summary
