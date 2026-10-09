# dag99-parallel: the dag99 graph with --parallel 8. No more than 8 lanes may
# run at the same instant (measured by the audit from every node's start and
# end), and every edge and value check of dag99 must still hold.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="dag99 with --parallel 8: never more than 8 lanes at once, every dag99 check still holds"
PARALLEL=8

scenario_run() {
  E2E_MAX_PARALLEL=$PARALLEL "$SWIM" run --yaml --parallel "$PARALLEL" $(seq 1 "$LANES") >"$OUT/run.yml" 2>"$OUT/run.err"
  RUN_EXIT=$?
}

scenario_check() {
  local audit
  audit=$(step_output 99 "audit the DAG")
  expect_eq "swim run exit" "$RUN_EXIT" 0
  expect_eq "lanes passed" "$(lanes_with PASS)" "$(seq_list 1 99)"
  expect_match "audit passed" "$audit" '^PASS: every node done, every edge honoured, every value matches'
  expect_match "edges all ok" "$audit" '^edges: 197 checked, 197 ok'
  expect_match "values all match" "$audit" '^values: 89 recomputed, 89 match'
  expect_match "never more than $PARALLEL at once" "$audit" "^concurrency: at most [1-8] lanes running at once \(limit $PARALLEL\)  ok$"
  expect_match "lanes queued for a slot" "$(cat "$OUT/run.yml")" '^event: queued$'
  printf '%s\n' "$audit" | grep -E '^(concurrency|scheduler latency|makespan)' | while IFS= read -r l; do info "$l"; done
  checks_passed
}
