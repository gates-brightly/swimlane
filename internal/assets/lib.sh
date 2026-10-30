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
_swim_soft=
_swim_no_hard=
_swim_stage=
_swim_reached=
_swim_stop_reason=
_swim_first_failed=
_swim_first_failed_stage=
SWIM_LANE=

# lane_init N — claim lane N: cd to its workspace (its staged copy under
# swim run), point STEP_LOG at .swim/logs/agentN.log, write the round marker and mark
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
  # swim run runs a lane in its staged copy ($SWIM_WORKSPACE), so relative
  # paths read only what its manifest pinned; # Workspace: live, or a lane
  # run directly with bash, runs in the repo.
  cd "${SWIM_WORKSPACE:-$SWIM_ROOT}" || exit 1
  STEP_LOG="$SWIM_ROOT/.swim/logs/agent$SWIM_LANE.log"
  SWIM_PID=$$
  export SWIM_LANE SWIM_ROOT STEP_LOG SWIM_PID
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
  SWIM_TIMEOUT=${_swim_start#* }
  SWIM_RUN_LANES=${SWIM_RUN_LANES:-$SWIM_LANE}
  # Where the round's outputs go (# Outputs:), fresh for each round.
  SWIM_OUT="$SWIM_ROOT/.swim/out/$SWIM_JOB/$SWIM_RUN"
  export SWIM_OUT SWIM_JOB SWIM_DEADLINE SWIM_RUN SWIM_RUN_LANES SWIM_STEP_TIMEOUT SWIM_TIMEOUT SWIM_FAIL_MODE
  trap '_swim_on_exit' EXIT
  trap '_swim_on_signal 130' INT
  trap '_swim_on_signal 143' TERM
  trap '_swim_on_err' ERR
}

# _swim_on_err — a command outside any step failed (a typo, a command that
# isn't there, a cd that didn't happen): nothing else records it, so say
# so in the log as a WARN. It changes nothing in the round. A function that
# returns non-zero (run, gate, guard, ...) recorded its own result:
# bash names its return, which is skipped. Commands in an if, while, && or
# || never get here.
_swim_on_err() {
  _swim_err=$?
  case "$BASH_COMMAND" in
    return|return\ *) return ;;
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
          stop "source $1: not pinned with the lane (add # Includes: $_swim_src)"
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
  exit "$1"
}

# Runs on any exit: a round that ends without `summary` (Ctrl-C, gate,
# stop, a typo) still gets a summary and a final state.
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
# Timeout: ran out. swim step exits 124 for any time limit, and leaves
# $STEP_LOG.round-timeout when it was the round's (not the step's own).
_swim_after_step() {
  _swim_last=$1
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
  if ! "$SWIM_BIN" _stage "$SWIM_LANE" "$1"; then
    stop "bad stage: ${1:-<none>} (stages are snapshot, check, change, verify)"
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

# _swim_run_step MODE [options] "<label>" cmd [args...] — shared by run and
# snapshot (MODE is "" or --snapshot). Options, before the label:
#   --timeout D        stop the step after D (30s, 5m); 0 turns off Step-Timeout:
#   --retry N          try up to N more times on a non-zero exit
#   --backoff D        wait D before the first retry, doubling (default 2s)
#   --retry-on CODES   retry only on these exit codes (e.g. 1,255)
#   --no-retry-timeout don't retry an attempt the step limit stopped
#   --soft             in a Fail: hard round, a failure doesn't end it
#   --expect-fail[=CODES]  the step should fail: it passes on a non-zero
#                      exit (one of CODES, e.g. =1,2), and fails on exit 0
#                      or another code; logged "failed as expected"
#   --checkpoint[=AGE] once it passed in this round, a later run of the round
#                      skips it (KEPT), unless its command changed or the pass
#                      is older than AGE (4h)
#   --key K            the step's name across rounds, logged (no spaces)
#   --target T         what it talks to (orders-api), logged; the lane's
#                      # Targets: by default
_swim_run_step() {
  _swim_mode=$1
  shift
  _swim_o=
  _swim_soft=
  while [ $# -gt 0 ]; do
    case "$1" in
      --soft) _swim_soft=1; shift ;;
      --timeout|--retry|--backoff|--retry-on|--key|--target)
        if [ $# -lt 2 ]; then
          echo "swim: $1 needs a value" >&2
          return 2
        fi
        _swim_o="$_swim_o $1 $2"
        shift 2 ;;
      --no-retry-timeout|--expect-fail|--expect-fail=*|--checkpoint|--checkpoint=*) _swim_o="$_swim_o $1"; shift ;;
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

# decide NAME "<question>" "<choice 1>" "<choice 2>" ... — ask the
# operator to pick one of 2 to 9 numbered choices, and set DECISION to the
# number picked (1-based); branch on it with case. NAME is declared in the
# header (# Decisions: NAME  what it decides). swim asks, not the script:
# NAME=N from whoever started swim run (or a queued batch's --decide)
# answers ahead; otherwise the operator at the prompt. Unanswered (no one
# to ask, a value that isn't a choice), the round ends here, SKIP, as a dry
# run: nothing on any branch runs. Call it on its own line: in $(...) or a
# pipe it couldn't end the round, so it refuses there.
decide() {
  if [ $# -lt 4 ]; then
    echo 'swim: usage: decide NAME "<question>" "<choice 1>" "<choice 2>" ...' >&2
    return 2
  fi
  case "$1" in
    ''|[0-9]*|*[!A-Za-z0-9_]*) echo "swim: bad decision name: $1" >&2; return 2 ;;
  esac
  if [ "${BASH_SUBSHELL:-0}" -gt 0 ]; then
    echo "swim: decide $1: call it on its own line, then read \$DECISION (in \$(...) or a pipe, an unanswered decision couldn't end the round)" >&2
    "$SWIM_BIN" _mark "$SWIM_LANE" STOP "decide $1 called in a subshell"
    kill -TERM "$SWIM_PID" 2>/dev/null
    exit 1
  fi
  _swim_name=$1 _swim_q=$2
  shift 2
  DECISION=
  # In an && list, a failure is neither the ERR trap's nor lost: $? keeps it.
  DECISION=$("$SWIM_BIN" _decide "$SWIM_LANE" "$_swim_name" "$_swim_q" -- "$@") && return 0
  _swim_rc=$?
  DECISION=
  if [ "$_swim_rc" -eq 3 ]; then
    # Unanswered: the round ends here, SKIP (nothing failed).
    _swim_end ""
    exit 0
  fi
  stop "decide $_swim_name: swim _decide failed (exit $_swim_rc)"
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
  stop "not confirmed: $1"
}

# msg [--progress N/M] "<text>" — post the lane's status message: where a
# long job is now ("migrating billing", --progress 7/25). It shows in the
# live view and swim status; the latest wins, msg "" clears it,
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

# cache restore|save TREE [--with STRING]... — an installed tree's snapshot
# (# Cache: snap:TREE @files), keyed by those files, the OS, the
# architecture and the --with strings. restore returns 1 on a miss (or when
# TREE is there already), so the script installs instead:
#   cache restore web/node_modules || { run "npm ci" npm ci --prefix web && cache save web/node_modules; }
cache() {
  "$SWIM_BIN" cache "$@"
}

# drift "<what differs>" — record a DRIFT line (deployed state differs from code).
drift() {
  "$SWIM_BIN" _mark "$SWIM_LANE" DRIFT "$1"
}

# stop "<reason>" — record STOP <reason> and end the round now.
stop() {
  "$SWIM_BIN" _mark "$SWIM_LANE" STOP "${1:-stopped}"
  _swim_stop_reason=${1:-stopped}
  exit 1
}

# summary — print the round summary,
# append it to the log, record the final state. Exits non-zero if any step
# failed. Put it last.
summary() {
  _swim_end ""
}
