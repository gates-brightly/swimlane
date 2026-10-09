# dag99-locks: the dag99 graph with resource locks. Generated lanes take locks
# from a pool of five (some none, some two). Lanes sharing a lock must never
# overlap (the audit checks every sharing pair against the nodes' start and
# end times), and lock waits must never cause a skip: every lane passes.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="dag99 with locks from a pool of five: sharers never overlap, lock waits never skip a lane"

scenario_setup() {
  cp "$DAG99_DIR"/lanes/lane.*.sh . && e2etool gen "$DAG99_DIR" && e2etool lock-lanes
}

scenario_check() {
  local audit
  audit=$(step_output 99 "audit the DAG")
  expect_eq "swim run exit" "$RUN_EXIT" 0
  expect_eq "every lane passed (lock waits never skip)" "$(lanes_with PASS)" "$(seq_list 1 99)"
  expect_match "audit passed" "$audit" '^PASS: every node done, every edge honoured, every value matches'
  expect_match "edges all ok" "$audit" '^edges: 197 checked, 197 ok'
  expect_match "no lanes sharing a lock overlapped" "$audit" '^locks: [0-9]+ lanes hold locks, [0-9]+ sharing pairs checked, 0 overlapped$'
  expect_match "lanes waited for locks" "$(cat "$OUT/run.out")" 'waiting for lock pool/'
  printf '%s\n' "$audit" | grep -E '^(locks|makespan)' | while IFS= read -r l; do info "$l"; done
  checks_passed
}
