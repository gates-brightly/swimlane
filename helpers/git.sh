# swim helpers, module git: the repo's git state. Building blocks for
# lanes that work on branches and worktrees.
#
# Copy into the repo whose lanes use it (say lib/swim/git.sh); in a lane:
#
#   # Includes: lib/swim/git.sh
#   source lib/swim/git.sh
#   stage check
#   git_fetch_gate origin main
#   git_worktree .worktrees/bdk-1328 bdk-1328
#   git_authors_gate "Zach Gates <zach@brightly.com>" origin/main..bdk-1328
#
# Every function is git_<name>. # Includes: pins the file with the lane, so
# approving the lane approves it as it is. The gates run their checks
# through swim _helper (the check is swim's own code, as gate is); the log
# shows the call and what the check found. They change only the repo's own git state (refs,
# a new worktree); pushing, resetting and deleting stay in the lane, behind
# its own guard (lint doesn't read this file). Paths are relative to the
# repo ($SWIM_ROOT). bash 3.2 and BSD tools (macOS).

# _git_fail LABEL MESSAGE: a failed gate, for a function called wrong.
_git_fail() {
  gate "$1" bash -c 'echo "$1"; exit 2' _ "$2"
}

# _git_path P: P, under $SWIM_ROOT unless it's absolute.
_git_path() {
  case $1 in
    /*) echo "$1" ;;
    *) echo "${SWIM_ROOT:-.}/$1" ;;
  esac
}

# git_fetch_gate REMOTE REF...: a gate that fetches REFs (branches, tags,
# refspecs) from REMOTE, with retries.
git_fetch_gate() {
  if [ $# -lt 2 ]; then
    _git_fail "git_fetch_gate: a remote and refs" "git_fetch_gate needs a remote and what to fetch: git_fetch_gate origin main"
    return 1
  fi
  _git_remote=$1
  shift
  gate --helper "git_fetch_gate $_git_remote $*" --timeout 2m --retry 2 "fetch $* from $_git_remote" git -C "${SWIM_ROOT:-.}" fetch --no-tags "$_git_remote" "$@"
}

# git_worktree PATH BRANCH [BASE]: a gate that PATH is a worktree on a new
# branch BRANCH from BASE (default origin/main). Rerunnable: a worktree
# already at PATH on BRANCH passes; one on another branch, or a BRANCH that
# already exists elsewhere, stops the round. Sets WT to the worktree's path.
git_worktree() {
  WT=$(_git_path "${1:-}")
  gate --helper "git_worktree${*:+ $*}" --timeout 2m "worktree ${1:-?} on ${2:-?} (from ${3:-origin/main})" "${SWIM_BIN:-swim}" _helper git_worktree "$@"
}

# git_pushed_gate BRANCH [REMOTE]: a gate that REMOTE (default origin) has
# BRANCH at the local branch's commit: what was tested is what was pushed.
git_pushed_gate() {
  gate --helper "git_pushed_gate${*:+ $*}" --timeout 1m "${1:-?} is on ${2:-origin} at its local head" "${SWIM_BIN:-swim}" _helper git_pushed_gate "$@"
}

# git_authors_gate "NAME <EMAIL>" RANGE [PATH]: a gate that every commit in
# RANGE (base..tip) was authored and committed by that identity, in the
# repo or the worktree at PATH. The log lists any that weren't.
git_authors_gate() {
  gate --helper "git_authors_gate${*:+ $*}" "commits in ${2:-?} are by ${1:-?}" "${SWIM_BIN:-swim}" _helper git_authors_gate "$@"
}
