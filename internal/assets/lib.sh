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
# Timeout: line, and SWIM_RUN / SWIM_RUN_LANES (the swim run and the lanes
# in it; a lane run directly with bash is a run of its own). Refuses if lane
# N is already running.
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
  # "<job> <deadline epoch, 0 for none> <run> <step timeout, 0 for none> <timeout text>"
  SWIM_JOB=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_DEADLINE=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_RUN=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_STEP_TIMEOUT=${_swim_start%% *}
  SWIM_TIMEOUT=${_swim_start#* }
  SWIM_RUN_LANES=${SWIM_RUN_LANES:-$SWIM_LANE}
  export SWIM_JOB SWIM_DEADLINE SWIM_RUN SWIM_RUN_LANES SWIM_STEP_TIMEOUT SWIM_TIMEOUT
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
# Timeout: ran out. swim step exits 124 for any time limit, and leaves
# $STEP_LOG.round-timeout when it was the round's (not the step's own).
_swim_after_step() {
  _swim_last=$1
  if [ "$_swim_last" -ne 0 ]; then
    _swim_fail=$((_swim_fail + 1))
  fi
  if [ "$_swim_last" -eq 124 ] && [ -e "$STEP_LOG.round-timeout" ]; then
    rm -f "$STEP_LOG.round-timeout"
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

# _swim_run_step MODE [options] "<label>" cmd [args...] — shared by run and
# snapshot (MODE is "" or --snapshot). Options, before the label:
#   --timeout D        stop the step after D (30s, 5m); 0 turns off Step-Timeout:
#   --retry N          try up to N more times on a non-zero exit
#   --backoff D        wait D before the first retry, doubling (default 2s)
#   --retry-on CODES   retry only on these exit codes (e.g. 1,255)
#   --no-retry-timeout don't retry an attempt the step limit stopped
_swim_run_step() {
  _swim_mode=$1
  shift
  _swim_o=
  while [ $# -gt 0 ]; do
    case "$1" in
      --timeout|--retry|--backoff|--retry-on)
        if [ $# -lt 2 ]; then
          echo "swim: $1 needs a value" >&2
          return 2
        fi
        _swim_o="$_swim_o $1 $2"
        shift 2 ;;
      --no-retry-timeout) _swim_o="$_swim_o $1"; shift ;;
      --) shift; break ;;
      --*) echo "swim: unknown step option $1" >&2; return 2 ;;
      *) break ;;
    esac
  done
  if [ $# -lt 2 ]; then
    echo 'swim: usage: run|gate|snapshot [options] "<label>" cmd [args...]' >&2
    return 2
  fi
  _swim_label=$1
  shift
  # $_swim_o is unquoted on purpose: option values (durations, codes) have no spaces.
  "$SWIM_BIN" step $_swim_mode $_swim_o --label "$_swim_label" -- "$@"
  _swim_after_step $?
}

# run [options] "<label>" cmd [args...] — run one step through `swim step`,
# recording PASS/FAIL <label>. A failure does not stop the round; use gate
# for that. For pipes or redirects, wrap them: run "<label>" bash -c 'a | b'.
# Options (--timeout, --retry, --backoff, --retry-on, --no-retry-timeout):
# see _swim_run_step. Only retry read-only or idempotent steps.
run() {
  _swim_run_step "" "$@"
}

# gate [options] "<label>" cmd [args...] — like run, but a failure (after
# the last attempt) stops the round here, before any later (destructive)
# step. Fail-closed.
gate() {
  _swim_run_step "" "$@"
  if [ "$_swim_last" -ne 0 ]; then
    stop "gate failed: $_swim_label"
  fi
  return 0
}

# snapshot [options] "<label>" cmd [args...] — run a read-only command and
# also save its output (the last attempt's) to
# .swim/snapshots/laneN-<time>-<label>.txt for comparison.
snapshot() {
  _swim_run_step --snapshot "$@"
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
