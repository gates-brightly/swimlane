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
# approving the lane approves it as it is. Each command runs through gate,
# so the log records it. They change only the repo's own git state (refs,
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
  gate --timeout 2m --retry 2 "fetch $* from $_git_remote" git -C "${SWIM_ROOT:-.}" fetch --no-tags "$_git_remote" "$@"
}

# git_worktree PATH BRANCH [BASE]: a gate that PATH is a worktree on a new
# branch BRANCH from BASE (default origin/main). Rerunnable: a worktree
# already at PATH on BRANCH passes; one on another branch, or a BRANCH that
# already exists elsewhere, stops the round. Sets WT to the worktree's path.
git_worktree() {
  if [ -z "${1:-}" ] || [ -z "${2:-}" ]; then
    _git_fail "git_worktree: a path and a branch" "git_worktree needs a path and a branch: git_worktree .worktrees/bdk-1328 bdk-1328"
    return 1
  fi
  WT=$(_git_path "$1")
  gate --timeout 2m "worktree $1 on $2 (from ${3:-origin/main})" bash -c 'root=$1 wt=$2 br=$3 base=$4; if [ -e "$wt/.git" ]; then cur=$(git -C "$wt" branch --show-current); [ "$cur" = "$br" ] && { echo "$wt is on $br already"; exit 0; }; echo "$wt is a worktree on ${cur:-a detached HEAD}, not $br"; exit 1; fi; if git -C "$root" show-ref --verify --quiet "refs/heads/$br"; then echo "branch $br exists already (not at $wt): pick another branch or path"; exit 1; fi; git -C "$root" worktree add --no-track -b "$br" "$wt" "$base"' _ "${SWIM_ROOT:-.}" "$WT" "$2" "${3:-origin/main}"
}

# git_pushed_gate BRANCH [REMOTE]: a gate that REMOTE (default origin) has
# BRANCH at the local branch's commit: what was tested is what was pushed.
git_pushed_gate() {
  if [ -z "${1:-}" ]; then
    _git_fail "git_pushed_gate: a branch" "git_pushed_gate needs a branch"
    return 1
  fi
  gate --timeout 1m "$1 is on ${2:-origin} at its local head" bash -c 'here=$(git -C "$1" rev-parse --verify "refs/heads/$2") || exit 1; there=$(git -C "$1" ls-remote "$3" "refs/heads/$2" | cut -f1) || exit 1; echo "local $here, $3 ${there:-(no such branch)}"; [ "$here" = "$there" ]' _ "${SWIM_ROOT:-.}" "$1" "${2:-origin}"
}

# git_authors_gate "NAME <EMAIL>" RANGE [PATH]: a gate that every commit in
# RANGE (base..tip) was authored and committed by that identity, in the
# repo or the worktree at PATH. The log lists any that weren't.
git_authors_gate() {
  if [ -z "${1:-}" ] || [ -z "${2:-}" ]; then
    _git_fail "git_authors_gate: an identity and a range" "git_authors_gate needs an identity and a range: git_authors_gate \"Name <email>\" origin/main..HEAD"
    return 1
  fi
  gate "commits in $2 are by $1" bash -c 'out=$(git -C "$1" log --format="%h %an <%ae> | %cn <%ce>" "$3") || exit 1; [ -n "$out" ] || { echo "no commits in $3"; exit 0; }; bad=$(printf "%s\n" "$out" | grep -v -F -x -e "" | while read -r h rest; do [ "$rest" = "$2 | $2" ] || echo "$h $rest"; done); [ -z "$bad" ] && { echo "$(printf "%s\n" "$out" | wc -l | tr -d " ") commits, all by $2"; exit 0; }; echo "not by $2:"; echo "$bad"; exit 1' _ "$(_git_path "${3:-.}")" "$1" "$2"
}
