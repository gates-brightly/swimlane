# dag99-interrupt: start dag99 and press Ctrl-C (SIGINT to swim) once, at a
# random point after lane 1 is held mid-run (E2E_HOLD), so the run is still
# going however fast the machine is: when a random lane outside lane 1's
# subtree (it runs while lane 1 holds) starts or ends. Every lane must end passed,
# interrupted at a step boundary, or skipped: no step is cut off
# mid-command, and no lane starts after the signal.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="dag99, one Ctrl-C mid-run: lanes stop at step boundaries, none start after it"

scenario_run() {
  local hold=$PWD/.scenario/hold
  E2E_HOLD=$hold "$SWIM" all --yaml >"$OUT/run.yml" 2>"$OUT/run.err" &
  local pid=$!
  dag99_held "$hold" "$pid" || { wait "$pid"; RUN_EXIT=$?; return 1; }
  local free lane
  free=$(comm -23 <(seq 2 98 | sort) <(e2etool descendants 1 | tr ' ' '\n' | sort))
  lane=$(echo "$free" | awk -v r="$RANDOM" 'NF { l[++n] = $1 } END { print l[r % n + 1] }')
  if [ $((RANDOM % 2)) = 0 ]; then
    WHEN="swim $lane started"
    wait_until test -e ".swim/logs/agent$lane.log"
  else
    WHEN="swim $lane ended"
    wait_until grep -q '^== END' ".swim/logs/agent$lane.log"
  fi
  kill -INT "$pid"
  touch "$hold" # lane 1's held step ends; the lane stops at the next boundary
  wait "$pid"
  RUN_EXIT=$?
}

scenario_check() {
  local n words cut counts
  expect_eq "interrupted run exits non-zero" "$([ "$RUN_EXIT" -ne 0 ] && echo yes)" yes
  words=$(e2etool run-results --raw <"$OUT/run.yml" | awk '{ print $2 }' | sort | uniq -c | awk '{ printf "%s=%s ", $2, $1 }')
  n=$(e2etool run-results --raw <"$OUT/run.yml" | grep -cE ' (passed|interrupted|skipped)$')
  expect_eq "every lane passed, interrupted or skipped" "$n" 99
  expect_eq "no lane started after the signal" "$(e2etool run-results --after-stop <"$OUT/run.yml" | tr '\n' ' ')" ""
  cut=$(cat .swim/logs/agent*.log | grep -cE '^  FAIL  .*interrupted\)' || true)
  expect_eq "no step was cut off mid-command" "$cut" 0
  expect_match "the stop was requested" "$(cat "$OUT/run.yml")" '^event: stop_requested$'
  info "Ctrl-C once $WHEN (lane 1 held): $words"
  checks_passed
}
