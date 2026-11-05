# swim ops helpers: read-only building blocks for lane scripts.
#
# Copy this file into the repo whose lanes use it (say lib/swim-ops.sh),
# then in a lane:
#
#   # Includes: lib/swim-ops.sh
#   source lib/swim-ops.sh
#   stage check
#   aws_gate 123456789012
#   pr_fetch 482
#
# # Includes: pins the file with the lane, so approving the lane approves
# these helpers as they are; an edit here is an edit to every lane that
# includes it. Every command a helper runs goes through gate or snapshot,
# so the log records it like any step. Nothing here changes anything but
# the repo's own git refs: destructive work stays in the lane, behind its
# own guard (lint doesn't read this file for guards).
#
# Runs on the operator's machine: bash 3.2 and BSD tools (macOS).

# _ops_fail LABEL MESSAGE: a failed gate, for a helper called wrong (the
# round stops, and the log says why).
_ops_fail() {
  gate "$1" bash -c 'echo "$1"; exit 2' _ "$2"
}

# pr_fetch N [REMOTE]: fetch GitHub pull request N's head from REMOTE
# (default origin) into refs/remotes/REMOTE/pr/N, as a gate: the round
# stops if it can't. Snapshots the commit it got, and sets PR_REF to the
# ref for the steps after it (git -C "$SWIM_ROOT" diff main..."$PR_REF").
pr_fetch() {
  _ops_pr=${1:-}
  _ops_remote=${2:-origin}
  case $_ops_pr in
    '' | *[!0-9]*)
      _ops_fail "pr_fetch: a PR number" "pr_fetch needs a pull request number, got '$_ops_pr'"
      return 1
      ;;
  esac
  PR_REF=refs/remotes/$_ops_remote/pr/$_ops_pr
  gate --timeout 2m --retry 2 "fetch PR $_ops_pr from $_ops_remote" \
    git -C "${SWIM_ROOT:-.}" fetch --no-tags "$_ops_remote" "+refs/pull/$_ops_pr/head:$PR_REF"
  snapshot "PR $_ops_pr head" git -C "${SWIM_ROOT:-.}" log -1 --format='%H %an: %s' "$PR_REF"
}

# aws_gate [ACCOUNT]: a gate that an AWS session is active (sts
# get-caller-identity answers, under AWS_PROFILE if set), and with ACCOUNT
# that it is that account. The log shows who the session is.
aws_gate() {
  if [ -z "${1:-}" ]; then
    gate --timeout 30s "AWS session active" aws sts get-caller-identity --output json
    return
  fi
  case $1 in
    *[!0-9]*)
      _ops_fail "aws_gate: an account id" "aws_gate takes a 12-digit account id, got '$1'"
      return 1
      ;;
  esac
  gate --timeout 30s "AWS session is account $1" bash -c 'got=$(aws sts get-caller-identity --query Account --output text) || exit 1; echo "account $got (profile ${AWS_PROFILE:-default})"; [ "$got" = "$1" ] || { echo "want account $1"; exit 1; }' _ "$1"
}
