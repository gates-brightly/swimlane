# dag99-force: start dag99 and press Ctrl-C twice (force quit), with
# interrupt_grace 1s. swim must exit, with every lane process gone, within
# the grace plus 2s (scaled by E2E_SLOW: 99 lanes stop beside the rest of
# the suite) of the second press.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="dag99, Ctrl-C twice: force quit; every process gone within the grace + 2s"

scenario_run() {
  "$SWIM" config interrupt_grace 1s >/dev/null
  local hold=$PWD/.scenario/hold
  E2E_HOLD=$hold "$SWIM" all --yaml >"$OUT/run.yml" 2>"$OUT/run.err" &
  local pid=$! t0 i
  dag99_held "$hold" "$pid" || { wait "$pid"; RUN_EXIT=$?; return 1; }
  kill -INT "$pid"
  # The second press counts as one only once swim has taken the first.
  wait_until grep -q '^event: stop_requested$' "$OUT/run.yml"
  kill -INT "$pid"
  t0=$(e2etool now)
  FORCE_LIMIT=$(awk -v k="$(scaled 2)" 'BEGIN { print 1 + k }')
  # Timed to the exit (or the limit and a second more).
  e2etool exited "$pid" "$(awk -v l="$FORCE_LIMIT" 'BEGIN { print l + 1 }')" || true
  FORCE_TOOK=$(awk -v a="$t0" -v b="$(e2etool now)" 'BEGIN { printf "%.2f", b - a }')
  touch "$hold" # a held step force quit missed would end now, not linger
  wait "$pid"
  RUN_EXIT=$?
  # A killed group can take a moment to go: one still there after 2s
  # (scaled) survived.
  local p
  STRAY=
  for p in $(pgrep -f "$PWD/\.swim/runs/"); do
    e2etool exited "$p" "$(scaled 2)" || STRAY="$STRAY$p "
  done
}

scenario_check() {
  expect_eq "force-quit run exits non-zero" "$([ "$RUN_EXIT" -ne 0 ] && echo yes)" yes
  expect_eq "swim exited within the grace + 2s (scaled)" "$(awk -v t="$FORCE_TOOK" -v l="$FORCE_LIMIT" 'BEGIN { print (t <= l) ? "yes" : "no (" t "s > " l "s)" }')" yes
  expect_eq "no lane process survived" "${STRAY% }" ""
  expect_match "force quit was announced" "$(cat "$OUT/run.yml")" '^event: force_quit$'
  expect_eq "lanes passed, interrupted or skipped" \
    "$(e2etool run-results --raw <"$OUT/run.yml" | grep -cE ' (passed|interrupted|skipped)$')" 99
  info "force quit took ${FORCE_TOOK}s"
  checks_passed
}
