# swim helpers, module gh: GitHub. Read-only building blocks for lanes.
#
# Copy into the repo whose lanes use it (say lib/swim/gh.sh); in a lane:
#
#   # Includes: lib/swim/gh.sh
#   source lib/swim/gh.sh
#   stage check
#   gh_pr_fetch 482
#
# Every function is gh_<name>. # Includes: pins the file with the lane, so
# approving the lane approves it as it is. Each command runs through gate
# or snapshot, so the log records it. Nothing here changes anything but the
# repo's own git refs; destructive work stays in the lane, behind its own
# guard (lint doesn't read this file). bash 3.2 and BSD tools (macOS).

# _gh_fail LABEL MESSAGE: a failed gate, for a function called wrong.
_gh_fail() {
  gate "$1" bash -c 'echo "$1"; exit 2' _ "$2"
}

# gh_pr_fetch N [REMOTE]: a gate that fetches pull request N's head from
# REMOTE (default origin) into refs/remotes/REMOTE/pr/N, with retries.
# Snapshots the commit it got and sets PR_REF to that ref for the steps
# after it (git -C "$SWIM_ROOT" diff main..."$PR_REF").
gh_pr_fetch() {
  _gh_pr=${1:-}
  _gh_remote=${2:-origin}
  case $_gh_pr in
    '' | *[!0-9]*)
      _gh_fail "gh_pr_fetch: a PR number" "gh_pr_fetch needs a pull request number, got '$_gh_pr'"
      return 1
      ;;
  esac
  PR_REF=refs/remotes/$_gh_remote/pr/$_gh_pr
  gate --timeout 2m --retry 2 "fetch PR $_gh_pr from $_gh_remote" \
    git -C "${SWIM_ROOT:-.}" fetch --no-tags "$_gh_remote" "+refs/pull/$_gh_pr/head:$PR_REF"
  snapshot "PR $_gh_pr head" git -C "${SWIM_ROOT:-.}" log -1 --format='%H %an: %s' "$PR_REF"
}

# gh_actions: true on a GitHub Actions runner (GITHUB_ACTIONS=true), for
# branching: if gh_actions; then ...; fi. It runs nothing and logs
# nothing; gh_actions_gate logs which runner it is.
gh_actions() {
  [ "${GITHUB_ACTIONS:-}" = true ]
}

# gh_actions_gate: a gate that this is a GitHub Actions runner. The log
# shows the runner, repository, run and commit.
gh_actions_gate() {
  gate "GitHub Actions runner" bash -c '[ "${GITHUB_ACTIONS:-}" = true ] || { echo "not a GitHub Actions runner (GITHUB_ACTIONS is not true)"; exit 1; }; echo "runner ${RUNNER_NAME:-?} (${RUNNER_OS:-?}): ${GITHUB_REPOSITORY:-?} run ${GITHUB_RUN_ID:-?} at ${GITHUB_SHA:-?}"'
}
