# dag99-cascade: the dag99 graph with failures injected (E2E_FAIL). Every lane
# downstream of a failure must be skipped, never started; everything else passes.
#   23  chain head (the 15-lane chain below it)
#   38  wide fan-in (13 parents)
#   41  random node; it is itself downstream of 23/38, so it is skipped, not failed
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="dag99 with lanes 23, 38, 41 failing: skips == exactly their descendants"
FAIL_LANES="23 38 41"

scenario_run() {
  E2E_FAIL="$FAIL_LANES" swim_run $(seq 1 "$LANES")
}

scenario_check() {
  local down failed skipped passed all l want_fail="" want_pass=""
  down=" $(e2etool descendants $FAIL_LANES) "
  for l in $FAIL_LANES; do case "$down" in *" $l "*) ;; *) want_fail="$want_fail $l" ;; esac; done
  for l in $(seq 1 "$LANES"); do
    case "$down $want_fail " in *" $l "*) ;; *) want_pass="$want_pass $l" ;; esac
  done
  expect_eq "swim run exit" "$RUN_EXIT" 1
  expect_eq "failed = injected lanes not downstream of another" "$(lanes_with FAIL)" "${want_fail# }"
  down=${down# }; down=${down% }
  expect_eq "skipped = descendants of the failures" "$(lanes_with SKIP)" "$down"
  expect_eq "passed = everything else" "$(lanes_with PASS)" "${want_pass# }"
  info "$(echo $down | wc -w | tr -d ' ') lanes skipped downstream of $FAIL_LANES"
  checks_passed
}
