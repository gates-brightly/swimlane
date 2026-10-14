# matrix25: one lane script over 25 services (Matrix: SVC=@services.txt),
# five cells at a time, with failures injected in three. The lane fails, the
# other 22 cells pass, never more than five run at once, a lane waiting on
# one passed cell (After: 1[svc01]) still runs, and `swim run 1 --failed`
# reruns exactly the three failed cells, after which the lane passes.
DESCRIPTION="25-cell matrix, Matrix-Parallel 5, 3 injected failures, --failed reruns exactly those"
LANES=2
FAIL_CELLS="svc07 svc13 svc21"

scenario_setup() {
  seq -f 'svc%02g' 1 25 > services.txt
  cat > lane.1.sh <<'LANE'
#!/usr/bin/env bash
# swim: syntax 2
# Round: Cut over $SVC
# Job: matrix25-job-0001
# Matrix: SVC=@services.txt
# Matrix-Parallel: 5
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
run "cut over $SVC" bash -c '
  mkdir -p .running && touch ".running/$SVC"
  ls .running | wc -l | tr -d " " >> concurrency.txt
  echo "$SVC" >> ran.txt
  sleep 0.2
  rm ".running/$SVC"
  case " $E2E_FAIL_CELLS " in *" $SVC "*) exit 1 ;; esac'
summary
LANE
  cat > lane.2.sh <<'LANE'
#!/usr/bin/env bash
# swim: syntax 2
# Round: after svc01
# After: 1[svc01]
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2
run "follows svc01" true
summary
LANE
}

scenario_run() {
  E2E_FAIL_CELLS="$FAIL_CELLS" "$SWIM" run 1 2 --yaml >"$OUT/run.yml" 2>"$OUT/run.err"
  RUN_EXIT=$?
  FIRST_RAN=$(sort ran.txt | tr '\n' ' ' | sed 's/ $//')
  MAX_AT_ONCE=$(sort -n concurrency.txt | tail -1)
  "$SWIM" status >"$OUT/status.first" 2>&1
  rm -f ran.txt
  "$SWIM" run 1 --failed --plain >"$OUT/retry.out" 2>&1
  RETRY_EXIT=$?
  RETRIED=$(sort ran.txt | tr '\n' ' ' | sed 's/ $//')
}

scenario_check() {
  expect_eq "first run exit (failures injected)" "$RUN_EXIT" 1
  expect_eq "every cell ran once" "$(echo $FIRST_RAN | wc -w | tr -d ' ')" 25
  expect_eq "at most Matrix-Parallel cells at once" "$([ "$MAX_AT_ONCE" -le 5 ] && echo yes)" yes
  expect_eq "lane 2 ran (it needs only 1[svc01])" "$(lanes_with PASS)" "2"
  expect_match "status lists the failed cells" "$(cat "$OUT/status.first")" "22/25 passed · failed: svc07, svc13, svc21"
  expect_eq "--failed reran exactly the failed cells" "$RETRIED" "$FAIL_CELLS"
  expect_eq "retry exit" "$RETRY_EXIT" 0
  expect_match "the lane passes after the retry" "$("$SWIM" status 2>&1)" "25/25 passed"
  expect_match "each cell has its own log" "$(ls .swim/logs)" "agent1.svc25.log"
  info "max cells at once: $MAX_AT_ONCE"
  checks_passed
}
