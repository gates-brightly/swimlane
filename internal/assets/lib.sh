# swim lane script library. Load it at the top of a lane script with:
#   _swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
# Must stay compatible with macOS bash 3.2: no associative arrays, no
# mapfile, no negative indexes, no ${var,,}.
export SWIM_BIN
_swim_fail=0
_swim_last=0
_swim_done=
SWIM_LANE=

# lane_init N — claim lane N: cd to the repo root, point STEP_LOG at
# .swim/logs/agentN.log, source .lane.N.rc if present, write the round marker and mark
# the lane running in .swim/status.yml. Exports SWIM_JOB (the job id from
# the script's Job: line), and SWIM_DEADLINE / SWIM_TIMEOUT from its
# Timeout: line. Refuses if lane N is already running.
lane_init() {
  case "$1" in
    ''|*[!0-9]*) echo "swim: lane_init needs a lane number, e.g. lane_init 1" >&2; exit 2 ;;
  esac
  SWIM_LANE=$1
  SWIM_ROOT=$(cd "$(dirname "$0")" && pwd -P) || exit 1
  cd "$SWIM_ROOT" || exit 1
  STEP_LOG="$SWIM_ROOT/.swim/logs/agent$SWIM_LANE.log"
  export SWIM_LANE SWIM_ROOT STEP_LOG
  if [ -f "$SWIM_ROOT/.lane.$SWIM_LANE.rc" ]; then
    . "$SWIM_ROOT/.lane.$SWIM_LANE.rc"
  fi
  if ! _swim_start=$("$SWIM_BIN" _start "$SWIM_LANE" --pid $$ --script "$0"); then
    _swim_done=1
    exit 1
  fi
  # "<job> <deadline epoch, 0 for none> <timeout text>"
  SWIM_JOB=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_DEADLINE=${_swim_start%% *}
  SWIM_TIMEOUT=${_swim_start#* }
  export SWIM_JOB SWIM_DEADLINE SWIM_TIMEOUT
  trap '_swim_on_exit' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
}

# Runs on any exit: a round that ends without `summary` (Ctrl-C, gate,
# stop, a typo) still gets a summary and a final state.
_swim_on_exit() {
  _swim_code=$?
  if [ -n "$_swim_done" ] || [ -z "$SWIM_LANE" ]; then
    return
  fi
  _swim_done=1
  if [ "$_swim_code" -eq 0 ]; then
    "$SWIM_BIN" _finish "$SWIM_LANE"
  else
    "$SWIM_BIN" _finish "$SWIM_LANE" --exit "$_swim_code"
  fi
  exit $?
}

# _swim_after_step CODE — count a failure; stop the round if the round's
# Timeout: ran out (swim step exits 124 then).
_swim_after_step() {
  _swim_last=$1
  if [ "$_swim_last" -ne 0 ]; then
    _swim_fail=$((_swim_fail + 1))
  fi
  if [ "$_swim_last" -eq 124 ] && [ "${SWIM_DEADLINE:-0}" -gt 0 ] && [ "$(date +%s)" -ge "$SWIM_DEADLINE" ]; then
    stop "timeout: the round's Timeout ($SWIM_TIMEOUT) ran out"
  fi
  return "$_swim_last"
}

# stage NAME — start a stage: snapshot, check, change or verify (in that
# order). Steps before the first stage belong to setup. Out of order, twice,
# or a change stage without a snapshot and check before it records WARN.
stage() {
  if ! "$SWIM_BIN" _stage "$SWIM_LANE" "$1"; then
    stop "bad stage: ${1:-<none>} (stages are snapshot, check, change, verify)"
  fi
}

# run "<label>" cmd [args...] — run one step through `swim step`, recording
# PASS/FAIL <label>. A failure does not stop the round; use gate for that.
# For pipes or redirects, wrap them: run "<label>" bash -c 'a | b'.
run() {
  if [ $# -lt 2 ]; then
    echo 'swim: usage: run "<label>" cmd [args...]' >&2
    return 2
  fi
  _swim_label=$1
  shift
  "$SWIM_BIN" step --label "$_swim_label" -- "$@"
  _swim_after_step $?
}

# gate "<label>" cmd [args...] — like run, but a failure stops the round
# here, before any later (destructive) step. Fail-closed.
gate() {
  run "$@"
  if [ "$_swim_last" -ne 0 ]; then
    stop "gate failed: $1"
  fi
  return 0
}

# snapshot "<label>" cmd [args...] — run a read-only command and also save
# its output to .swim/snapshots/laneN-<time>-<label>.txt for comparison.
snapshot() {
  if [ $# -lt 2 ]; then
    echo 'swim: usage: snapshot "<label>" cmd [args...]' >&2
    return 2
  fi
  _swim_label=$1
  shift
  "$SWIM_BIN" step --snapshot --label "$_swim_label" -- "$@"
  _swim_after_step $?
}

# last_failed — true if the previous run/gate/snapshot failed.
last_failed() {
  [ "$_swim_last" -ne 0 ]
}

# any_failed — true if any step in this round has failed so far.
any_failed() {
  [ "$_swim_fail" -gt 0 ]
}

# guard FLAG "<action — date, reason>" — true only if the operator set
# FLAG=1. Otherwise records SKIP with a dry-run message showing how to
# approve, and returns false. One flag per destructive action.
guard() {
  if [ $# -lt 2 ]; then
    echo 'swim: usage: guard FLAG "<action - date, reason>"' >&2
    return 2
  fi
  case "$1" in
    ''|[0-9]*|*[!A-Za-z0-9_]*) echo "swim: bad guard flag name: $1" >&2; return 2 ;;
  esac
  eval "_swim_val=\${$1:-}"
  if [ "$_swim_val" = "1" ]; then
    "$SWIM_BIN" _mark "$SWIM_LANE" APPROVED "$2" "$1=1"
    return 0
  fi
  "$SWIM_BIN" _mark "$SWIM_LANE" SKIP "$2" "dry run: set $1=1 to approve"
  return 1
}

# confirm "<question>" [answer] — ask the operator; anything other than the
# exact answer (default: yes), including no input, stops the round.
confirm() {
  _swim_want=${2:-yes}
  printf '%s [type %s to continue]: ' "$1" "$_swim_want" >&2
  _swim_ans=
  read -r _swim_ans || _swim_ans=
  if [ "$_swim_ans" = "$_swim_want" ]; then
    "$SWIM_BIN" _mark "$SWIM_LANE" APPROVED "$1" "operator typed $_swim_want"
    return 0
  fi
  stop "not confirmed: $1"
}

# drift "<what differs>" — record a DRIFT line (deployed state differs from code).
drift() {
  "$SWIM_BIN" _mark "$SWIM_LANE" DRIFT "$1"
}

# stop "<reason>" — record STOP <reason> and end the round now.
stop() {
  "$SWIM_BIN" _mark "$SWIM_LANE" STOP "${1:-stopped}"
  exit 1
}

# summary — print the round summary, append it to the log, record the
# final state. Exits non-zero if any step failed. Put it last.
summary() {
  "$SWIM_BIN" _finish "$SWIM_LANE"
  _swim_rc=$?
  _swim_done=1
  return $_swim_rc
}
