# dag99-interrupt: start dag99 and press Ctrl-C (SIGINT to swim) once, at a
# random point between 1s and 3s. Every lane must end passed, interrupted at
# a step boundary, or skipped: no step is cut off mid-command, and no lane
# starts after the signal.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="dag99, one Ctrl-C at 1-3s: lanes stop at step boundaries, none start after it"

scenario_run() {
  "$SWIM" all --yaml >"$OUT/run.yml" 2>"$OUT/run.err" &
  local pid=$!
  WHEN=$(awk -v r="$RANDOM" 'BEGIN { printf "%.2f", 1 + 2 * r / 32767 }')
  sleep "$WHEN"
  kill -INT "$pid"
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
  info "Ctrl-C at ${WHEN}s: $words"
  checks_passed
}
