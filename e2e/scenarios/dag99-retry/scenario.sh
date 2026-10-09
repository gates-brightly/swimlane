# dag99-retry: fail lanes 23, 38, 41, then fix them and retry with `swim all`.
# Checks that swim all reruns exactly the lanes that didn't pass (the failed
# ones and everything skipped below them), never a lane that already passed;
# that swim plan predicts that retry before it happens; and that a third
# swim all has nothing left to do.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="dag99 fails, then swim all retries exactly the failed + skipped lanes; plan predicts it"
FAIL_LANES="23 38 41"

scenario_run() {
  # 1. Every lane, with failures injected.
  E2E_FAIL="$FAIL_LANES" "$SWIM" all --plain >"$OUT/first.out" 2>&1
  FIRST_EXIT=$?
  cp "$OUT/first.out" "$OUT/run.out"
  FIRST_PASS=$(lanes_with PASS)
  FIRST_REDO=$(lane_results | awk '$2 != "PASS" { print $1 }' | sort -n | tr '\n' ' ' | sed 's/ $//')
  # 2. What a retry would do.
  "$SWIM" plan >"$OUT/plan.out" 2>&1
  PLAN_EXIT=$?
  # 3. The retry, failures fixed: becomes $OUT/run.out for the helpers.
  "$SWIM" all --plain >"$OUT/run.out" 2>&1
  RUN_EXIT=$?
  # 4. Nothing should be left.
  "$SWIM" all --plain >"$OUT/third.out" 2>&1
  THIRD_EXIT=$?
}

rounds_of() { "$SWIM" log "$1" 2>/dev/null | grep -c '^== ROUND '; }

scenario_check() {
  local npass nredo l bad=""
  npass=$(echo $FIRST_PASS | wc -w | tr -d ' ')
  nredo=$(echo $FIRST_REDO | wc -w | tr -d ' ')

  expect_eq "first swim all exit (failures injected)" "$FIRST_EXIT" 1
  expect_eq "first run: the injected failures leave work to redo" "$([ "$nredo" -gt 0 ] && echo yes)" yes

  expect_eq "swim plan exit" "$PLAN_EXIT" 0
  expect_match "plan: every failed or skipped lane is a retry, nothing else" "$(cat "$OUT/plan.out")" \
    "^Plan: 0 to run, $nredo to retry, 0 to rerun, 0 to skip\.$"
  expect_match "plan: passed lanes summarised as completed" "$(cat "$OUT/plan.out")" \
    "^$npass items have completed with no remaining work\.$"
  expect_match "plan: lane 23 marked [~]" "$(cat "$OUT/plan.out")" '\[~\] swim 23 '

  expect_eq "retry exit" "$RUN_EXIT" 0
  expect_eq "retry ran exactly the lanes that didn't pass" "$(lane_results | awk '{print $1}' | sort -n | tr '\n' ' ' | sed 's/ $//')" "$FIRST_REDO"
  expect_eq "every retried lane passed" "$(lanes_with PASS)" "$FIRST_REDO"
  expect_match "audit passed on the retry" "$(step_output 99 'audit the DAG')" '^PASS: every node done, every edge honoured, every value matches'

  for l in $FIRST_PASS; do
    [ "$(rounds_of "$l")" = 1 ] || bad="$bad $l"
  done
  expect_eq "lanes that passed first time were never rerun" "${bad# }" ""

  expect_eq "third swim all exit" "$THIRD_EXIT" 0
  expect_match "third swim all: nothing to run" "$(cat "$OUT/third.out")" '^swim: nothing to run: every pending job has already passed'

  info "first run: $npass passed, $nredo to redo; retry reran exactly those $nredo"
  checks_passed
}
