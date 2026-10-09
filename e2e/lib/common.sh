# Helpers for e2e scenarios. Sourced by e2e/run.sh before a scenario's
# scenario.sh; every function runs in the scenario's scratch repo (cwd).
# Keep this bash 3.2 compatible (macOS): no associative arrays, mapfile, ${x,,}.
#
# Set by run.sh:
#   E2E            e2e/ directory          SCENARIO_DIR  this scenario's directory
#   OUT            scratch dir for output  SWIM          swim binary under test
#   E2E_FIXTURES   e2e/fixtures

E2E_CHECK_FAILS=0

# swim_run LANE...  runs `swim run --plain` on the lanes; output in $OUT/run.out,
# exit code in $RUN_EXIT. Env set by the caller passes through.
swim_run() {
  "$SWIM" run --plain "$@" >"$OUT/run.out" 2>&1
  RUN_EXIT=$?
}

# lane_results  prints "<lane> <PASS|FAIL|SKIP>" for every lane in the last summary.
lane_results() {
  sed -n '/^swim summary/,$p' "$OUT/run.out" |
    awk '$1 == "swim" && $2 ~ /^[0-9]+$/ && ($4 == "PASS" || $4 == "FAIL" || $4 == "SKIP") { print $2, $4 }'
}

# lanes_with RESULT  space-separated lanes with that result, ascending.
lanes_with() {
  lane_results | awk -v r="$1" '$2 == r { print $1 }' | sort -n | tr '\n' ' ' | sed 's/ $//'
}

# step_output N LABEL  the output of step LABEL in lane N's latest round:
# the "        | " lines under its result line, prefix removed (log syntax 2).
step_output() {
  "$SWIM" log "$1" --raw 2>/dev/null | awk -v label="$2" '
    /^== ROUND / { buf = ""; f = 0 }
    /^  [A-Z]+  / { f = (index($0, label) > 0); next }
    f && /^        \| / { buf = buf substr($0, 11) "\n"; next }
    f && !/^        / { f = 0 }
    END { printf "%s", buf }'
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
