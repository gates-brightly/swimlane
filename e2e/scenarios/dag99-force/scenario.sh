# dag99-force: start dag99 and press Ctrl-C twice (force quit), with
# interrupt_grace 1s. swim must exit, with every lane process gone, within
# the grace plus a second of the second press.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="dag99, Ctrl-C twice: force quit; every process gone within the grace + 1s"

scenario_run() {
  "$SWIM" config interrupt_grace 1s >/dev/null
  "$SWIM" all --yaml >"$OUT/run.yml" 2>"$OUT/run.err" &
  local pid=$! t0 i
  sleep 2
  kill -INT "$pid"
  sleep 0.3
  kill -INT "$pid"
  t0=$(e2etool now)
  # swim must be gone within the grace (1s) + 1s.
  for i in $(seq 1 40); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.05
  done
  FORCE_TOOK=$(awk -v a="$t0" -v b="$(e2etool now)" 'BEGIN { printf "%.2f", b - a }')
  wait "$pid"
  RUN_EXIT=$?
  sleep 0.3
  STRAY=$(pgrep -f "$PWD/lane\." | tr '\n' ' ')
}

scenario_check() {
  expect_eq "force-quit run exits non-zero" "$([ "$RUN_EXIT" -ne 0 ] && echo yes)" yes
  expect_eq "swim exited within the grace + 1s" "$(awk -v t="$FORCE_TOOK" 'BEGIN { print (t <= 2.2) ? "yes" : "no (" t "s)" }')" yes
  expect_eq "no lane process survived" "${STRAY% }" ""
  expect_match "force quit was announced" "$(cat "$OUT/run.yml")" '^event: force_quit$'
  expect_eq "lanes passed, interrupted or skipped" \
    "$(e2etool run-results --raw <"$OUT/run.yml" | grep -cE ' (passed|interrupted|skipped)$')" 99
  info "force quit took ${FORCE_TOOK}s"
  checks_passed
}
