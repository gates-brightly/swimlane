# swim lane script library. Load it at the top of a lane script with:
#   _swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
# Must stay compatible with macOS bash 3.2: no associative arrays, no
# mapfile, no negative indexes, no ${var,,}.
export SWIM_BIN
_swim_fail=0
_swim_last=0
_swim_label=
_swim_prev_label=
_swim_done=
_swim_rc_file=
_swim_pre_rc=
_swim_soft=
_swim_no_hard=
_swim_stage=
_swim_reached=
_swim_stop_reason=
_swim_first_failed=
_swim_first_failed_stage=
_swim_in_handler=
_swim_handler_abort=
_swim_round_code=
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
  if [ -n "${SWIM_STAGE:-}" ] && [ -n "${SWIM_ROOT:-}" ]; then
    # swim run runs the staged copy (.swim/runs/<run>/<lane>/): the repo is
    # the one the launcher names, not the script's directory.
    SWIM_ROOT=$(cd "$SWIM_ROOT" && pwd -P) || exit 1
  else
    SWIM_ROOT=$(cd "$(dirname "$0")" && pwd -P) || exit 1
    # A lane from a group file runs as .swim/groups/lane.N.sh: its repo is
    # two directories up.
    case "$SWIM_ROOT" in */.swim/groups) SWIM_ROOT=${SWIM_ROOT%/.swim/groups} ;; esac
  fi
  cd "$SWIM_ROOT" || exit 1
  # A read-only check (swim plan --check) logs apart from the round log.
  STEP_LOG="$SWIM_ROOT/.swim/logs/agent$SWIM_LANE${SWIM_CHECK_ONLY:+.check}.log"
  # A matrix cell logs to its own file (swim passes it).
  if [ -n "${SWIM_CELL:-}" ] && [ -n "${SWIM_CELL_LOG:-}" ]; then
    STEP_LOG=$SWIM_CELL_LOG
  fi
  # The git shim (.swim/bin/git, installed by swim _start) refuses push,
  # commit and pull and signals this script (SWIM_PID) to stop the round.
  SWIM_PID=$$
  PATH="$SWIM_ROOT/.swim/bin:$PATH"
  export SWIM_LANE SWIM_ROOT STEP_LOG SWIM_PID PATH
  # .lane.N.rc is deprecated (env files replace it; swim _start logs a
  # WARN). What the shell held before it is kept so that a guard flag it
  # sets is refused (see _swim_from_rc).
  if [ -f "$SWIM_ROOT/.lane.$SWIM_LANE.rc" ]; then
    _swim_rc_file="$SWIM_ROOT/.lane.$SWIM_LANE.rc"
    _swim_pre_rc=$(declare -p 2>/dev/null)
    . "$_swim_rc_file"
  fi
  # A lane run directly (bash lane.N.sh) gets its caches (# Cache:) here;
  # under swim run the launcher has set them.
  if [ -z "${SWIM_LAUNCHED:-}" ]; then
    _swim_cache=$("$SWIM_BIN" _cache-env "$SWIM_LANE") || exit 1
    eval "$_swim_cache"
  fi
  # Before _start marks the lane running: the launcher only signals a lane
  # (SIGUSR2, a stop request) once status shows its pid, so the trap is
  # always in place by then.
  trap '_swim_operator_stop' USR2
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
  _swim_start=${_swim_start#* }
  SWIM_FAIL_MODE=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_HANDLER_TIMEOUT=${_swim_start%% *}
  SWIM_TIMEOUT=${_swim_start#* }
  SWIM_RUN_LANES=${SWIM_RUN_LANES:-$SWIM_LANE}
  # Where the round's outputs go (# Outputs:), fresh for each round.
  SWIM_OUT="$SWIM_ROOT/.swim/out/$SWIM_JOB/$SWIM_RUN"
  export SWIM_OUT SWIM_JOB SWIM_DEADLINE SWIM_RUN SWIM_RUN_LANES SWIM_STEP_TIMEOUT SWIM_TIMEOUT SWIM_FAIL_MODE SWIM_HANDLER_TIMEOUT
  trap '_swim_on_exit' EXIT
  trap 'stop "blocked command: git push/commit/pull (swim never writes to git)"' USR1
  trap '_swim_on_signal 130' INT
  trap '_swim_on_signal 143' TERM
}

# source FILE / . FILE — under swim run, a repo file the manifest pinned is
# read from the run's stage (the bytes swim checked before the lane
# started), not the working tree. Anything else is sourced as usual. A
# `declare` at the top of a file sourced this way is local to this
# function: use plain assignments there.
source() {
  if [ -n "${SWIM_STAGE:-}" ] && [ $# -gt 0 ]; then
    case $1 in
      /*) _swim_src=${1#"$SWIM_ROOT"/} ;;
      *) _swim_src=$1 ;;
    esac
    case $_swim_src in
      /*|../*|*/../*) ;;
      *)
        if [ -f "$SWIM_STAGE/$_swim_src" ]; then
          shift
          set -- "$SWIM_STAGE/$_swim_src" "$@"
        fi
        ;;
    esac
  fi
  builtin source "$@"
}
.() { source "$@"; }

# _swim_on_signal CODE — Ctrl-C (130) or SIGTERM (143): end the round; in a
# handler, skip the handlers' remaining steps instead (the running one has
# the signal too).
_swim_on_signal() {
  if [ -n "$_swim_in_handler" ]; then
    _swim_handler_abort="interrupted"
    return
  fi
  exit "$1"
}

# Runs on any exit: a round that ends without `summary` (Ctrl-C, gate,
# stop, a typo) still runs its handlers and gets a summary and a final state.
_swim_on_exit() {
  _swim_code=$?
  if [ -z "$SWIM_LANE" ]; then
    return
  fi
  if [ -n "$_swim_in_handler" ]; then
    # A handler called exit: the rest of the handlers are skipped, and the
    # round ends as it would have.
    "$SWIM_BIN" _mark "$SWIM_LANE" STOP "handler exited (exit $_swim_code); remaining handlers skipped"
    _swim_in_handler=
    _swim_finish "$_swim_round_code"
    exit $?
  fi
  if [ -n "$_swim_done" ]; then
    return
  fi
  _swim_end "$_swim_code"
  exit $?
}

# _swim_end [CODE] — end the round: run its outcome handlers, then record
# the summary and final state. CODE is how the script exited ("" from
# summary: the log decides).
_swim_end() {
  _swim_done=1
  _swim_round_code=$1
  _swim_handlers "${1:-0}"
  _swim_finish "$1"
}

_swim_finish() {
  if [ -z "$1" ] || [ "$1" -eq 0 ]; then
    "$SWIM_BIN" _finish "$SWIM_LANE"
  else
    "$SWIM_BIN" _finish "$SWIM_LANE" --exit "$1"
  fi
}

# _swim_handlers CODE — call the round's outcome handlers: on_success if it
# passed, else on_failure; then on_exit. Each gets SWIM_OUTCOME (pass, fail,
# stopped, interrupted, timeout), SWIM_STOP_REASON, SWIM_FAILED_STEP and
# SWIM_FAILED_STAGE, and runs soft: nothing in it stops it, and its steps
# share one budget (Handler-Timeout:, default 5m) that starts now.
_swim_handlers() {
  if [ -n "${SWIM_CHECK_ONLY:-}" ]; then
    return # a read-only check changed nothing: nothing to roll back or clean up
  fi
  if [ -n "$_swim_stop_reason" ]; then
    case "$_swim_stop_reason" in
      "interrupted by operator"*) _swim_outcome=interrupted ;;
      timeout:*) _swim_outcome=timeout ;;
      *) _swim_outcome=stopped ;;
    esac
  elif [ "$1" -eq 130 ] || [ "$1" -eq 143 ] || [ "$1" -eq 129 ]; then
    _swim_outcome=interrupted
  elif [ "$1" -ne 0 ] || [ "$_swim_fail" -gt 0 ]; then
    _swim_outcome=fail
  else
    _swim_outcome=pass
  fi
  if [ "$_swim_outcome" = pass ]; then
    _swim_first=on_success
  else
    _swim_first=on_failure
  fi
  for _swim_h in $_swim_first on_exit; do
    declare -F "$_swim_h" >/dev/null 2>&1 || continue
    if [ -z "$_swim_in_handler" ]; then
      _swim_in_handler=1
      SWIM_OUTCOME=$_swim_outcome
      SWIM_STOP_REASON=$_swim_stop_reason
      SWIM_FAILED_STEP=$_swim_first_failed
      SWIM_FAILED_STAGE=$_swim_first_failed_stage
      # The handlers' own deadline replaces the round's; steps in them start
      # even after a stop request (SWIM_IN_HANDLER).
      SWIM_DEADLINE=$(( $(date +%s) + ${SWIM_HANDLER_TIMEOUT:-300} ))
      SWIM_TIMEOUT="Handler-Timeout ${SWIM_HANDLER_TIMEOUT:-300}s"
      SWIM_IN_HANDLER=1
      export SWIM_OUTCOME SWIM_STOP_REASON SWIM_FAILED_STEP SWIM_FAILED_STAGE SWIM_DEADLINE SWIM_TIMEOUT SWIM_IN_HANDLER
      trap '' USR2 # a stop request now only skips the handlers' remaining steps (see run)
    fi
    "$SWIM_BIN" _handler "$SWIM_LANE" "$_swim_h"
    "$_swim_h"
  done
  _swim_in_handler=
  unset SWIM_IN_HANDLER
}

# _swim_after_step CODE — count a failure; stop the round if the round's
# Timeout: ran out. swim step exits 124 for any time limit, and leaves
# $STEP_LOG.round-timeout when it was the round's (not the step's own).
_swim_after_step() {
  _swim_last=$1
  if [ -n "$_swim_in_handler" ]; then
    # Nothing stops a handler; its budget running out skips the rest.
    if [ "$_swim_last" -eq 124 ] && [ -e "$STEP_LOG.round-timeout" ]; then
      rm -f "$STEP_LOG.round-timeout"
      _swim_handler_abort="the handlers' budget ($SWIM_TIMEOUT) ran out"
    fi
    return "$_swim_last"
  fi
  if [ "$_swim_last" -eq 86 ]; then
    # swim step refused to start: the lane is stopping.
    _swim_operator_stop "$_swim_prev_label"
  fi
  _swim_prev_label=$_swim_label
  if [ "$_swim_last" -ne 0 ]; then
    _swim_fail=$((_swim_fail + 1))
    if [ -z "$_swim_first_failed" ]; then
      _swim_first_failed=$_swim_label
      _swim_first_failed_stage=${_swim_stage:-setup}
    fi
  fi
  if [ "$_swim_last" -eq 87 ]; then
    stop "blocked command: $_swim_label"
  fi
  if [ "$_swim_last" -eq 124 ] && [ -e "$STEP_LOG.round-timeout" ]; then
    rm -f "$STEP_LOG.round-timeout"
    stop "timeout: the round's Timeout ($SWIM_TIMEOUT) ran out"
  fi
  # Fail: hard: the first failed step ends the round (a gate stops it with
  # its own reason; run --soft opts one step out).
  if [ "$_swim_last" -ne 0 ] && [ "${SWIM_FAIL_MODE:-soft}" = hard ] && [ -z "$_swim_soft" ] && [ -z "$_swim_no_hard" ]; then
    stop "hard failure: $_swim_label"
  fi
  return "$_swim_last"
}

# _swim_operator_stop [LABEL] — end the round at a step boundary because
# the operator asked (exit 130: interrupted). LABEL is the last step that
# ran (default: the current one, which has just finished when the trap runs).
# A stop request is the operator's first Ctrl-C or swim interrupt N. Bash
# runs the trap once the current command returns, so a running step
# finishes first; a waiting confirm is cancelled.
_swim_operator_stop() {
  trap '' USR2 # ignore repeats (resetting would let a second one kill bash)
  if [ -n "$_swim_done" ]; then
    return
  fi
  _swim_after=${1-$_swim_label}
  if [ -n "$_swim_after" ]; then
    _swim_stop_reason="interrupted by operator (after: $_swim_after)"
  else
    _swim_stop_reason="interrupted by operator (before the first step)"
  fi
  "$SWIM_BIN" _mark "$SWIM_LANE" STOP "$_swim_stop_reason"
  exit 130
}

# stage NAME — start a stage: snapshot, check, change or verify (in that
# order). Steps before the first stage belong to setup. Out of order, twice,
# or a change stage without a snapshot and check before it records WARN.
stage() {
  if [ -n "${SWIM_CHECK_ONLY:-}" ]; then
    case "$1" in
      change|verify) _swim_checked "$1" ;;
    esac
  fi
  if ! "$SWIM_BIN" _stage "$SWIM_LANE" "$1"; then
    stop "bad stage: ${1:-<none>} (stages are snapshot, check, change, verify)"
  fi
  _swim_stage=$1
  _swim_reached="$_swim_reached $1"
}

# _swim_checked STAGE — a read-only check (swim plan --check) ends where
# change would begin: record CHECKED and end the round, passing.
_swim_checked() {
  "$SWIM_BIN" _mark "$SWIM_LANE" CHECKED "stopped before stage $1" "swim plan --check runs only snapshot and check"
  exit 0
}

# reached STAGE — true if the round started STAGE or a later one. A
# failure before the change stage changed nothing, so an on_failure handler
# usually starts with: reached change || return 0
reached() {
  _swim_later=
  for _swim_s in snapshot check change verify; do
    if [ "$_swim_s" = "$1" ]; then
      _swim_later=1
    fi
    if [ -n "$_swim_later" ]; then
      case " $_swim_reached " in
        *" $_swim_s "*) return 0 ;;
      esac
    fi
  done
  return 1
}

# _swim_run_step MODE [options] "<label>" cmd [args...] — shared by run and
# snapshot (MODE is "" or --snapshot). Options, before the label:
#   --timeout D        stop the step after D (30s, 5m); 0 turns off Step-Timeout:
#   --retry N          try up to N more times on a non-zero exit
#   --backoff D        wait D before the first retry, doubling (default 2s)
#   --retry-on CODES   retry only on these exit codes (e.g. 1,255)
#   --no-retry-timeout don't retry an attempt the step limit stopped
#   --soft             in a Fail: hard round, a failure doesn't end it
_swim_run_step() {
  _swim_mode=$1
  shift
  _swim_o=
  _swim_soft=
  while [ $# -gt 0 ]; do
    case "$1" in
      --soft) _swim_soft=1; shift ;;
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
  if [ -n "$_swim_in_handler" ] && [ -n "$_swim_handler_abort" ]; then
    "$SWIM_BIN" _mark "$SWIM_LANE" SKIP "$_swim_label" "not run: $_swim_handler_abort"
    _swim_last=1
    return 1
  fi
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
  _swim_no_hard=1
  _swim_run_step "" "$@"
  _swim_no_hard=
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

# evidence [options] "<label>" cmd [args...] — a check stage step whose
# output shows what the change will do (terraform plan, a diff): saved and
# hashed as the round's evidence, so an approval can cover the plan, not
# just the script (swim approve shows it and signs its hash; a guard counts
# an approval only while the evidence still matches). Print stable output:
# cut timestamps and refresh noise. A failure stops the round, like a gate.
evidence() {
  _swim_no_hard=1
  _swim_run_step --evidence "$@"
  _swim_no_hard=
  if [ "$_swim_last" -ne 0 ]; then
    stop "evidence failed: $_swim_label"
  fi
  return 0
}

# last_failed — true if the previous run/gate/snapshot failed.
last_failed() {
  [ "$_swim_last" -ne 0 ]
}

# any_failed — true if any step in this round has failed so far.
any_failed() {
  [ "$_swim_fail" -gt 0 ]
}

# guard FLAG "<action — date, reason>" — true if the operator approves:
# FLAG=1 approves up front (CI, scripts). With FLAG unset and an operator
# at the terminal, swim asks: "swim N requires human approval of guard
# FLAG ... the lane says: <action> Y/N [N]"; y approves (recorded with who
# answered). Under swim run the launcher decides every guard: FLAG=1
# approves only if swim run's own environment set it (not the lane), a
# FLAG the script doesn't declare or use is refused, and an Observe: fact
# that changed since approval stops the round as drift. Anything else (n,
# Enter, FLAG set to another value, nobody to ask) records SKIP with a
# dry-run message showing how to approve, and returns false. One flag per
# destructive action.
guard() {
  if [ $# -lt 2 ]; then
    echo 'swim: usage: guard FLAG "<action - date, reason>"' >&2
    return 2
  fi
  case "$1" in
    ''|[0-9]*|*[!A-Za-z0-9_]*) echo "swim: bad guard flag name: $1" >&2; return 2 ;;
  esac
  if [ -n "${SWIM_CHECK_ONLY:-}" ]; then
    # A read-only check never approves anything, whatever the flag says.
    "$SWIM_BIN" _mark "$SWIM_LANE" SKIP "$2" "dry run: swim plan --check never approves; set $1=1 to approve"
    return 1
  fi
  eval "_swim_val=\${$1-unset}"
  if [ "$_swim_val" = "1" ] && _swim_from_rc "$1"; then
    # Files never approve guards: ask as if the flag were unset.
    "$SWIM_BIN" _mark "$SWIM_LANE" WARN "$1=1 from .lane.$SWIM_LANE.rc ignored" "files can't approve guards; set it in the shell or approve"
    _swim_val=unset
  fi
  _swim_flag_set=
  if [ "$_swim_val" = "1" ]; then
    [ -n "$SWIM_LANE" ] || return 0
    # swim _approve says whether the flag counts: under swim run, only if
    # swim run's own environment set it; run directly with bash, it does.
    # Not this script's environment: a lane can change that.
    _swim_flag_set=--flag-set
  fi
  if [ -n "$_swim_flag_set" ] || { [ "$_swim_val" = "unset" ] && [ -n "$SWIM_LANE" ]; }; then
    # Nobody set the flag: ask the operator if one is at the terminal
    # ("requires human approval of guard ... Y/N [N]"); swim _approve
    # records APPROVED, or SKIP as a dry run when declined or no one can be
    # asked (CI, no terminal, approvals: flags).
    "$SWIM_BIN" _approve "$SWIM_LANE" "$1" "$2" $_swim_flag_set
    _swim_rc=$?
    if [ "$_swim_rc" -eq 3 ]; then
      # An Observe: fact changed between approval and now.
      stop "drift: observed facts changed since approval ($1)"
    fi
    return "$_swim_rc"
  fi
  "$SWIM_BIN" _mark "$SWIM_LANE" SKIP "$2" "dry run: set $1=1 to approve"
  return 1
}

# _swim_from_rc FLAG — true if .lane.N.rc set FLAG=1: it names FLAG, and
# FLAG wasn't already 1 before lane_init sourced it. declare -p prints
# `declare -x FLAG="1"` in bash 4+, but a bare `FLAG=1` line in bash 3.2.
_swim_from_rc() {
  [ -n "${_swim_rc_file:-}" ] || return 1
  _swim_nl='
'
  case "$_swim_nl$_swim_pre_rc$_swim_nl" in
    *" $1=\"1\"$_swim_nl"*|*"$_swim_nl$1=1$_swim_nl"*|*"$_swim_nl$1='1'$_swim_nl"*) return 1 ;;
  esac
  grep -q "$1" "$_swim_rc_file" 2>/dev/null
}

# confirm "<question>" [answer] — ask the operator; anything other than the
# exact answer (default: yes), including no input, stops the round.
confirm() {
  if [ -n "${SWIM_CHECK_ONLY:-}" ]; then
    # A question for the operator comes before a change: the check ends here.
    "$SWIM_BIN" _mark "$SWIM_LANE" SKIP "$1" "not asked: swim plan --check"
    _swim_checked "confirm"
  fi
  _swim_want=${2:-yes}
  printf '%s [type %s to continue]: ' "$1" "$_swim_want" >&2
  _swim_ans=
  # Read in 1s slices so a stop request (Ctrl-C) cancels the wait on any
  # bash (3.2's read isn't interrupted by the USR2 trap). A failed read
  # that returns at once is end of input, not a timeout.
  while :; do
    _swim_t=$SECONDS
    if read -r -t 1 _swim_ans; then
      break
    fi
    _swim_ans=
    if [ "$SECONDS" -eq "$_swim_t" ]; then
      break
    fi
    if [ -z "$_swim_in_handler" ] && [ -e "$SWIM_ROOT/.swim/lane$SWIM_LANE.stop" ]; then
      _swim_operator_stop
    fi
  done
  if [ "$_swim_ans" = "$_swim_want" ]; then
    "$SWIM_BIN" _mark "$SWIM_LANE" APPROVED "$1" "operator typed $_swim_want" --confirm
    return 0
  fi
  stop "not confirmed: $1"
}

# msg [--progress N/M] "<text>" — post the lane's status message: where a
# long job is now ("migrating billing", --progress 7/25). It shows in the
# live view, swim status and the hub; the latest wins, msg "" clears it,
# and the log gets it throttled. Never fails the round.
msg() {
  "$SWIM_BIN" msg "$@" || true
}

# input N|JOB NAME — print the path of an output of a round this one
# depends on (After:): lane N's round this run waited on, or job JOB's.
# A small value (output KEY value) prints the value itself. Use it as
# "$(input 2 stats.json)". A failure is recorded as FAIL.
input() {
  "$SWIM_BIN" input "$@"
}

# output KEY VALUE — record a small value (an id, a version) among the
# round's outputs; dependent lanes read it with $(input N KEY).
output() {
  "$SWIM_BIN" output "$@"
}

# drift "<what differs>" — record a DRIFT line (deployed state differs from code).
drift() {
  "$SWIM_BIN" _mark "$SWIM_LANE" DRIFT "$1"
}

# stop "<reason>" — record STOP <reason> and end the round now. In a
# handler it records the STOP (the handler fails) and returns 1 instead:
# nothing stops a rollback halfway.
stop() {
  "$SWIM_BIN" _mark "$SWIM_LANE" STOP "${1:-stopped}"
  if [ -n "$_swim_in_handler" ]; then
    return 1
  fi
  _swim_stop_reason=${1:-stopped}
  exit 1
}

# summary — run the round's outcome handlers, print the round summary,
# append it to the log, record the final state. Exits non-zero if any step
# failed. Put it last.
summary() {
  _swim_end ""
}
