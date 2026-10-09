# flaky: steps that fail their first N calls (a counter file). With enough
# --retry they pass on the right attempt; with too few they fail after the
# last one, and lanes below are skipped as usual.
DESCRIPTION="--retry: enough retries pass on attempt 3/3, too few fail at 3/3; skips spread as usual"
LANES=4

scenario_setup() {
  cp "$SCENARIO_DIR"/lanes/lane.*.sh .
}

scenario_check() {
  expect_eq "swim run exit" "$RUN_EXIT" 1
  expect_eq "passed" "$(lanes_with PASS)" "1 3"
  expect_eq "failed" "$(lanes_with FAIL)" "2"
  expect_eq "skipped" "$(lanes_with SKIP)" "4"
  expect_match "lane 1 passed on its third attempt" "$("$SWIM" log 1 --raw)" '^  PASS  fails twice \(attempt 3/3\)'
  expect_match "lane 2 failed after its third attempt" "$("$SWIM" log 2 --raw)" '^  FAIL  fails three times \(exit 1, attempt 3/3\)'
  expect_match "every attempt is in the log" "$("$SWIM" log 2 --raw | grep -c '^        -- attempt ')" '^3$'
  expect_match "attempt output kept" "$(step_output 1 'fails twice')" 'call 3'
  checks_passed
}
