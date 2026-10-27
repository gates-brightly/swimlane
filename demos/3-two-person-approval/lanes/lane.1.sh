#!/usr/bin/env bash
# swim: syntax 4
# Round:  Delete 2019 orders (past the 5-year retention)
# Owner:  planner (AI session)
# Guards: FIN_ALLOW_DELETE  delete the 2019 rows from orders
#
# Goal: finance's retention policy says orders older than five years go.
# The export to cold storage happened in swim 1's previous round.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage snapshot
snapshot "orders before" fakecloud select orders

stage check
gate "orders table exists" fakecloud rows orders
run  "2019 rows to delete" fakecloud select orders ,2019-

stage change
if guard FIN_ALLOW_DELETE "delete the 2019 rows from orders"; then
  run "delete 2019 orders" fakecloud delete orders ,2019-
fi

stage verify
run "no 2019 rows left" bash -c '[ -z "$(fakecloud select orders ,2019-)" ]'
summary
