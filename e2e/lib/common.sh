# Helpers for e2e scenarios. Sourced by e2e/run.sh before a scenario's
# scenario.sh; every function runs in the scenario's scratch repo (cwd).
# Keep this bash 3.2 compatible (macOS): no associative arrays, mapfile, ${x,,}.
#
# Set by run.sh:
#   E2E            e2e/ directory          SCENARIO_DIR  this scenario's directory
#   OUT            scratch dir for output  SWIM          swim binary under test
#   E2E_FIXTURES   e2e/fixtures

E2E_CHECK_FAILS=0

# swim_run LANE...  runs `swim run --yaml` on the lanes; the event stream in
# $OUT/run.yml, stderr in $OUT/run.err, exit code in $RUN_EXIT. Env set by the
# caller passes through.
swim_run() {
  "$SWIM" run --yaml "$@" >"$OUT/run.yml" 2>"$OUT/run.err"
  RUN_EXIT=$?
}

# lane_results  prints "<lane> <PASS|FAIL|SKIP>" for every lane in the last
# run's summary event ($OUT/run.yml, from swim run --yaml).
lane_results() {
  e2etool run-results <"$OUT/run.yml"
}

# lanes_with RESULT  space-separated lanes with that result, ascending.
lanes_with() {
  lane_results | awk -v r="$1" '$2 == r { print $1 }' | sort -n | tr '\n' ' ' | sed 's/ $//'
}

# step_output N LABEL  the output of step LABEL in lane N's latest round
# (from swim log N --yaml).
step_output() {
  "$SWIM" log "$1" --yaml 2>/dev/null | e2etool step-output "$2"
}

# info TEXT  a line for the report (shown under the scenario's result).
info() { echo "    $*" >>"$OUT/info"; }

# expect_eq WHAT GOT WANT  records a failed check unless GOT == WANT.
expect_eq() {
  if [ "$2" = "$3" ]; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1"
    echo "        got:  ${2:-<empty>}"
    echo "        want: ${3:-<empty>}"
    E2E_CHECK_FAILS=$((E2E_CHECK_FAILS + 1))
  fi
}

# expect_match WHAT TEXT REGEX  records a failed check unless TEXT matches REGEX (grep -E).
expect_match() {
  if printf '%s\n' "$2" | grep -Eq -- "$3"; then
    echo "  ok    $1"
  else
    echo "  FAIL  $1 (no line matching /$3/)"
    E2E_CHECK_FAILS=$((E2E_CHECK_FAILS + 1))
  fi
}

# checks_passed  exit status for scenario_check: 0 if every expect_* passed.
checks_passed() { [ "$E2E_CHECK_FAILS" -eq 0 ]; }

# seq_list A B  "A A+1 ... B" (seq is fine on macOS and Linux, this just joins).
seq_list() { seq "$1" "$2" | tr '\n' ' ' | sed 's/ $//'; }

# Timing, as internal/e2e's: a scenario waits for an event (a file, an
# event in run.yml), never a fixed time; a bound it checks on purpose (how
# long a force quit takes) is scaled by E2E_SLOW (default 1; CI sets 3).

# scaled SECONDS  SECONDS times E2E_SLOW.
scaled() { awk -v s="$1" -v k="${E2E_SLOW:-1}" 'BEGIN { if (k < 1) k = 1; print s * k }'; }

# wait_until CMD...  runs CMD again whenever a file under the scenario's
# directory (its repo and $OUT) changes, never on a timer, until it
# succeeds (0), or fails (1) once 60s (scaled) pass. The fingerprint is
# taken before CMD runs, so a change while it does isn't missed.
wait_until() {
  local dir fp end
  dir=$(dirname "$OUT")
  end=$(( $(date +%s) + $(scaled 60 | cut -d. -f1) ))
  while :; do
    fp=$(e2etool fingerprint "$dir")
    "$@" && return 0
    [ "$(date +%s)" -ge "$end" ] && return 1
    e2etool changed "$dir" "$fp" || return 1
  done
}
