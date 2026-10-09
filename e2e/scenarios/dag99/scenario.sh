# dag99: a 99-lane DAG through the scheduler, with every edge and value audited.
#   lanes 1-9   real work over fixture pages (fetch, markdown, stats, links, ...)
#   lanes 10-98 generated: fan-out, deep chain, wide fan-in, random layers, 3 more roots
#   lane 99     audit (waits on every sink); the shape is in e2e/cmd/e2etool/gen.go
DESCRIPTION="99-lane DAG: ordering, data flow, parallel roots, scheduler latency"
LANES=99
DAG99_DIR="$E2E/scenarios/dag99"   # fixed, so scenarios that source this file reuse it

scenario_setup() {
  cp "$DAG99_DIR"/lanes/lane.*.sh . && e2etool gen "$DAG99_DIR"
}

scenario_check() {
  local audit
  audit=$(step_output 99 "audit the DAG")
  expect_eq "swim run exit" "$RUN_EXIT" 0
  expect_eq "lanes passed" "$(lanes_with PASS)" "$(seq_list 1 99)"
  expect_match "audit passed" "$audit" '^PASS: every node done, every edge honoured, every value matches'
  expect_match "edges all ok" "$audit" '^edges: 197 checked, 197 ok'
  expect_match "values all match" "$audit" '^values: 89 recomputed, 89 match'
  expect_match "roots started together" "$audit" '^roots in this run \[1, 8, 20, 21, 22\]: .* ok$'
  printf '%s\n' "$audit" | grep -E '^(scheduler latency|makespan)' | while IFS= read -r l; do info "$l"; done
  checks_passed
}
