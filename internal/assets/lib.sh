# swim lane script library. Load it at the top of a lane script with:
#   _swim_lib=$("${SWIM_BIN:-swim}" _lib) || exit 1; eval "$_swim_lib"
# Must stay compatible with macOS bash 3.2: no associative arrays, no
# mapfile, no negative indexes, no ${var,,}.
export SWIM_BIN
_swim_fail=0
_swim_last=0
_swim_label=
_swim_prev_label=
_swim_done=
_swim_soft=
_swim_no_hard=
_swim_stage=
_swim_reached=
_swim_stop_reason=
_swim_first_failed=
_swim_first_failed_stage=
SWIM_LANE=

# lane_init N — claim lane N: cd to its workspace (its pinned copy under
# swim run), point STEP_LOG at .swim/logs/laneN.log, write the round marker and mark
# the lane running in .swim/status.yml. Exports SWIM_ROUND_ID (the round id from
# the script's Round-ID: line), and SWIM_DEADLINE / SWIM_TIMEOUT from its
# Timeout: line, and SWIM_RUN / SWIM_RUN_LANES (the swim run and the lanes
# in it; a lane run directly with bash is a run of its own). Refuses if lane
# N is already running.
lane_init() {
  case "$1" in
    ''|*[!0-9]*) echo "swim: lane_init needs a lane number, e.g. lane_init 1" >&2; exit 2 ;;
  esac
  SWIM_LANE=$1
  if [ -n "${SWIM_STAGE:-}" ] && [ -n "${SWIM_ROOT:-}" ]; then
    # swim run runs the pinned copy (.swim/runs/<run>/<lane>/): the repo is
    # the one the launcher names, not the script's directory.
    SWIM_ROOT=$(cd "$SWIM_ROOT" && pwd -P) || exit 1
  else
    SWIM_ROOT=$(cd "$(dirname "$0")" && pwd -P) || exit 1
    # A lane from a group file runs as .swim/groups/lane.N.sh: its repo is
    # two directories up.
    case "$SWIM_ROOT" in */.swim/groups) SWIM_ROOT=${SWIM_ROOT%/.swim/groups} ;; esac
  fi
  # swim run runs a lane in its pinned copy ($SWIM_WORKSPACE), so relative
  # paths read only what its manifest pinned; # Workspace: live, or a lane
  # run directly with bash, runs in the repo.
  cd "${SWIM_WORKSPACE:-$SWIM_ROOT}" || exit 1
  STEP_LOG="$SWIM_ROOT/.swim/logs/lane$SWIM_LANE.log"
  SWIM_PID=$$
  export SWIM_LANE SWIM_ROOT STEP_LOG SWIM_PID
  # Before _start marks the lane running: the launcher only signals a lane
  # (SIGUSR2, a stop request) once status shows its pid, so the trap is
  # always in place by then.
  trap '_swim_operator_stop' USR2
  if ! _swim_start=$("$SWIM_BIN" _start "$SWIM_LANE" --pid $$ --script "$0"); then
    _swim_done=1
    exit 1
  fi
  # "<round id> <deadline epoch, 0 for none> <run> <step timeout, 0 for none>
  # <fail mode> <start, Unix ms> <timeout text>"
  SWIM_ROUND_ID=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_DEADLINE=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_RUN=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_STEP_TIMEOUT=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_FAIL_MODE=${_swim_start%% *}
  _swim_start=${_swim_start#* }
  SWIM_STARTED=${_swim_start%% *}
  SWIM_TIMEOUT=${_swim_start#* }
  SWIM_RUN_LANES=${SWIM_RUN_LANES:-$SWIM_LANE}
  export SWIM_ROUND_ID SWIM_DEADLINE SWIM_RUN SWIM_RUN_LANES SWIM_STEP_TIMEOUT SWIM_TIMEOUT SWIM_FAIL_MODE SWIM_STARTED
  # A marker an earlier round's stop left behind (its shell already gone,
  # a TERM racing a Ctrl-C) would turn this round's real interrupt into a
  # failure: only this round's stop may leave one.
  rm -f "$STEP_LOG.stopped"
  trap '_swim_on_exit' EXIT
  trap '_swim_on_signal 130' INT
  trap '_swim_on_signal 143' TERM
  trap '_swim_on_err' ERR
}

# _swim_on_err — a command outside any step failed (a typo, a command that
# isn't there, a cd that didn't happen): nothing else records it, so say
# so in the log as a WARN. It changes nothing in the round. A function that
# returns non-zero (step, gate, guard, ...) recorded its own result:
# bash names its return, which is skipped. Commands in an if, while, && or
# || never get here. Nor does a short-form lane's own script, which the
# prelude sources (runner.ShortPrelude): it returns its last command's
# status, and that command recorded itself.
_swim_on_err() {
  _swim_err=$?
  case "$BASH_COMMAND" in
    return|return\ *|'builtin source "$0"') return ;;
  esac
  if [ -z "$SWIM_LANE" ] || [ -n "$_swim_done" ]; then
    return
  fi
  if [ "$_swim_err" -eq 127 ]; then
    _swim_err_why="exit 127: command not found"
  else
    _swim_err_why="exit $_swim_err"
  fi
  "$SWIM_BIN" _mark "$SWIM_LANE" WARN "outside a step: $BASH_COMMAND" "$_swim_err_why; it isn't a step, so this line is its only record"
}

# source FILE / . FILE — under swim run, a repo file the header lists
# (# Includes:) is read from the run's stage (the bytes swim checked before
# the lane started), not the working tree. A repo file it doesn't list is
# refused: the round stops, saying which # Includes: line to add, since the
# approval doesn't cover it. Files outside the repo, swim's own (.swim/)
# are sourced as usual. A `declare` at the top of a file
# sourced this way is local to this function: use plain assignments there.
source() {
  if [ -n "${SWIM_STAGE:-}" ] && [ $# -gt 0 ]; then
    case $1 in
      /*) _swim_src=${1#"$SWIM_ROOT"/} ;;
      *) _swim_src=$1 ;;
    esac
    case $_swim_src in
      /*|../*|*/../*|.swim/*) ;;
      *)
        if [ -f "$SWIM_STAGE/$_swim_src" ]; then
          shift
          set -- "$SWIM_STAGE/$_swim_src" "$@"
        elif [ -f "$SWIM_ROOT/$_swim_src" ]; then
          if [ -z "$SWIM_LANE" ]; then
            echo "swim: source $1: not pinned with the lane; add # Includes: $_swim_src" >&2
            return 1
          fi
          _swim_stop "source $1: not pinned with the lane (add # Includes: $_swim_src)"
          return 1
        fi
        ;;
    esac
  fi
  builtin source "$@"
}
.() { source "$@"; }

# _swim_on_signal CODE — Ctrl-C (130) or SIGTERM (143): end the round.
_swim_on_signal() {
  if [ -e "$STEP_LOG.stopped" ]; then
    # _swim_stop, from a subshell: the round failed there.
    rm -f "$STEP_LOG.stopped"
    exit 1
  fi
  exit "$1"
}

# Runs on any exit: a round that ends without `summary` (Ctrl-C, a failed
# gate, a typo) still gets a summary and a final state.
_swim_on_exit() {
  _swim_code=$?
  if [ -z "$SWIM_LANE" ]; then
    return
  fi
  if [ -n "$_swim_done" ]; then
    return
  fi
  _swim_end "$_swim_code"
  exit $?
}

# _swim_end [CODE] — end the round: record the summary and final state.
# CODE is how the script exited ("" from summary: the log decides).
_swim_end() {
  _swim_done=1
  _swim_finish "$1"
}

_swim_finish() {
  if [ -z "$1" ] || [ "$1" -eq 0 ]; then
    "$SWIM_BIN" _finish "$SWIM_LANE"
  else
    "$SWIM_BIN" _finish "$SWIM_LANE" --exit "$1"
  fi
}

# _swim_after_step CODE — count a failure; stop the round if the round's
# Timeout: ran out. swim _step exits 124 for any time limit, and leaves
# $STEP_LOG.round-timeout when it was the round's (not the step's own).
_swim_after_step() {
  _swim_last=$1
  if [ "$_swim_last" -eq 86 ]; then
    # swim _step refused to start: the lane is stopping.
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
    _swim_stop "blocked command: $_swim_label"
  fi
  if [ "$_swim_last" -eq 124 ] && [ -e "$STEP_LOG.round-timeout" ]; then
    rm -f "$STEP_LOG.round-timeout"
    _swim_stop "timeout: the round's Timeout ($SWIM_TIMEOUT) ran out"
  fi
  # Fail: hard: the first failed step ends the round (a gate stops it with
  # its own reason; step --soft opts one step out).
  if [ "$_swim_last" -ne 0 ] && [ "${SWIM_FAIL_MODE:-soft}" = hard ] && [ -z "$_swim_soft" ] && [ -z "$_swim_no_hard" ]; then
    _swim_stop "hard failure: $_swim_label"
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
  if ! "$SWIM_BIN" _stage "$SWIM_LANE" "$1"; then
    _swim_stop "bad stage: ${1:-<none>} (stages are snapshot, check, change, verify)"
  fi
  _swim_stage=$1
  _swim_reached="$_swim_reached $1"
}

# reached STAGE — true if the round started STAGE or a later one.
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

# _swim_run_step MODE [options] "<label>" cmd [args...] — shared by step,
# gate and snapshot (MODE is "" or --snapshot). Options, before the label:
# An option's value may follow it or an = (--timeout 5m, --timeout=5m), and
# a -- may come after the label (step "<label>" -- cmd ...), as in other
# languages' swim step.
#   --timeout D        stop the step after D (30s, 5m); 0 turns off Step-Timeout:
#   --retry N          try up to N more times on a non-zero exit
#   --backoff D        wait D before the first retry, doubling (default 2s)
#   --retry-on CODES   retry only on these exit codes (e.g. 1,255)
#   --no-retry-timeout don't retry an attempt the step limit stopped
#   --soft             in a Fail: hard round, a failure doesn't end it
#   --expect-fail[=CODES]  the step should fail: it passes on a non-zero
#                      exit (one of CODES, e.g. =1,2), and fails on exit 0
#                      or another code; logged "failed as expected"
#   --key K            the step's name across rounds, logged (no spaces)
#   --target T         what it talks to (orders-api), logged; the lane's
#                      # Targets: by default
#   --helper CALL      for swim helpers modules: the call this step is
#                      (tool_gate go 1.27), whose first word is the function
#                      running it; the log shows CALL for the command when
#                      that function's file is this swim's module as shipped
# _swim_misuse MSG — a step written wrong can't run, and nothing after it
# may run as if it had passed (a gate that never checked would let the
# change through): count it failed and stop the round, saying why in the
# log.
_swim_misuse() {
  echo "swim: $1" >&2
  _swim_last=2
  _swim_fail=$((_swim_fail + 1))
  if [ -z "$_swim_first_failed" ]; then
    _swim_first_failed="step written wrong"
    _swim_first_failed_stage=${_swim_stage:-setup}
  fi
  _swim_stop "step written wrong: $1"
}

_swim_run_step() {
  _swim_mode=$1
  shift
  _swim_o=
  _swim_soft=
  _swim_helper=
  while [ $# -gt 0 ]; do
    case "$1" in
      --soft) _swim_soft=1; shift ;;
      --helper)
        if [ $# -lt 2 ] || [ -z "$2" ]; then
          _swim_misuse "--helper needs the call: --helper \"tool_gate go 1.27\""
        fi
        _swim_helper=$2
        shift 2 ;;
      --timeout|--retry|--backoff|--retry-on|--key|--target)
        if [ $# -lt 2 ]; then
          _swim_misuse "$1 needs a value"
        fi
        _swim_o="$_swim_o $1 $2"
        shift 2 ;;
      --timeout=*|--retry=*|--backoff=*|--retry-on=*|--key=*|--target=*)
        # --opt=value, as swim _step takes it.
        if [ -z "${1#*=}" ]; then
          _swim_misuse "${1%%=*} needs a value"
        fi
        _swim_o="$_swim_o ${1%%=*} ${1#*=}"
        shift ;;
      --no-retry-timeout|--expect-fail|--expect-fail=*) _swim_o="$_swim_o $1"; shift ;;
      --) shift; break ;;
      --*) _swim_misuse "unknown step option $1 (options: --timeout --retry --backoff --retry-on --no-retry-timeout --soft --expect-fail --key --target)" ;;
      *) break ;;
    esac
  done
  if [ $# -lt 2 ]; then
    if [ $# -eq 1 ]; then
      _swim_misuse "step \"$1\" has a label but no command: step|gate|snapshot [options] \"<label>\" cmd [args...]"
    fi
    _swim_misuse 'a step with no label or command: step|gate|snapshot [options] "<label>" cmd [args...]'
  fi
  _swim_label=$1
  shift
  # step "<label>" -- cmd, the form swim step takes from other languages:
  # the -- only ends the label.
  if [ "$1" = "--" ]; then
    shift
    if [ $# -eq 0 ]; then
      _swim_misuse "step \"$_swim_label\" has a label but no command after --"
    fi
  fi
  if [ -n "$_swim_helper" ]; then
    # The file that defines the helper's function, as it was sourced: swim
    # _step reads its stamp. The function is the call's first word; a lane
    # that defines its own names itself, and its command is shown in full.
    _swim_hfile=
    _swim_i=1
    while [ "$_swim_i" -lt "${#FUNCNAME[@]}" ]; do
      if [ "${FUNCNAME[$_swim_i]}" = "${_swim_helper%% *}" ]; then
        _swim_hfile=${BASH_SOURCE[$_swim_i]}
        break
      fi
      _swim_i=$((_swim_i + 1))
    done
    "$SWIM_BIN" _step $_swim_mode $_swim_o --helper "$_swim_helper" --helper-file "$_swim_hfile" --label "$_swim_label" -- "$@"
    _swim_after_step $?
    return
  fi
  # $_swim_o is unquoted on purpose: option values (durations, codes) have no spaces.
  "$SWIM_BIN" _step $_swim_mode $_swim_o --label "$_swim_label" -- "$@"
  _swim_after_step $?
}

# step [options] "<label>" cmd [args...] — run one step through `swim _step`,
# recording PASS/FAIL <label>. A failure does not stop the round; use gate
# for that. For pipes or redirects, wrap them: step "<label>" bash -c 'a | b'.
# Options (--timeout, --retry, --backoff, --retry-on, --no-retry-timeout):
# see _swim_run_step. Only retry read-only or idempotent steps.
step() {
  _swim_run_step "" "$@"
}

# gate [options] "<label>" cmd [args...] — like step, but a failure (after
# the last attempt) stops the round here, before any later (destructive)
# step. Fail-closed.
gate() {
  _swim_no_hard=1
  _swim_run_step "" "$@"
  _swim_no_hard=
  if [ "$_swim_last" -ne 0 ]; then
    _swim_stop "gate failed: $_swim_label"
  fi
  return 0
}

# snapshot [options] "<label>" cmd [args...] — run a read-only command and
# also save its output (the last attempt's) to
# .swim/snapshots/laneN-<time>-<label>.txt for comparison.
snapshot() {
  _swim_run_step --snapshot "$@"
}

# last_failed — true if the previous step/gate/snapshot failed.
last_failed() {
  [ "$_swim_last" -ne 0 ]
}

# any_failed — true if any step in this round has failed so far.
any_failed() {
  [ "$_swim_fail" -gt 0 ]
}

# guard FLAG — true if the operator approves what the header says FLAG
# does and why (# Guards: FLAG  <does>, then #   why: <why>). An older
# form, guard FLAG "<action>", gives the text in the call.
# FLAG=1 approves up front (CI, scripts). With FLAG unset and an operator
# at the terminal, swim asks: "swim N requires human approval of guard
# FLAG ... the lane says: <action> Y/N [N]"; y approves (recorded with who
# answered). Under swim run the launcher decides every guard: FLAG=1
# approves only if swim run's own environment set it (not the lane), a
# FLAG the script doesn't declare or use is refused. Anything else (n,
# Enter, FLAG set to another value, nobody to ask) records SKIP with a
# dry-run message showing how to approve, and returns false. One flag per
# destructive action.
guard() {
  if [ $# -lt 1 ]; then
    echo 'swim: usage: guard FLAG (the header says what it does: # Guards: FLAG  <does>)' >&2
    return 2
  fi
  case "$1" in
    ''|[0-9]*|*[!A-Za-z0-9_]*) echo "swim: bad guard flag name: $1" >&2; return 2 ;;
  esac
  if [ $# -lt 2 ] || [ -z "$2" ]; then
    # What the step does is the header's: # Guards: FLAG  <does>.
    set -- "$1" "$("$SWIM_BIN" _guard_text "$SWIM_LANE" "$1" 2>/dev/null || printf '%s' "$1")"
  fi
  eval "_swim_val=\${$1-unset}"
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
    # asked (CI, no terminal, --no-prompt).
    "$SWIM_BIN" _approve "$SWIM_LANE" "$1" "$2" $_swim_flag_set
    return
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
    if [ -e "$SWIM_ROOT/.swim/lane$SWIM_LANE.stop" ]; then
      _swim_operator_stop
    fi
  done
  if [ "$_swim_ans" = "$_swim_want" ]; then
    "$SWIM_BIN" _mark "$SWIM_LANE" APPROVED "$1" "operator typed $_swim_want" --confirm
    return 0
  fi
  _swim_stop "not confirmed: $1"
}

# _swim_stop "<reason>" — record STOP <reason> and end the round now (a
# failed gate, a refused step, an unconfirmed confirm).
_swim_stop() {
  "$SWIM_BIN" _mark "$SWIM_LANE" STOP "${1:-stopped}"
  _swim_stop_reason=${1:-stopped}
  if [ "${BASH_SUBSHELL:-0}" -gt 0 ]; then
    # In $(...) or a pipe, exit would end only this subshell, and the
    # round would go on past a failed gate. End the round itself: the
    # marker makes its TERM handler end it failed, not interrupted.
    : > "$STEP_LOG.stopped"
    kill -TERM "$SWIM_PID" 2>/dev/null
  fi
  exit 1
}

# summary — print the round summary,
# append it to the log, record the final state. Exits non-zero if any step
# failed. Put it last.
summary() {
  _swim_end ""
}
