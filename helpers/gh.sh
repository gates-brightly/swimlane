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

# Functions that talk to GitHub use $REPO (owner/name) when set, else the
# repo origin points at (gh_repo sets it).

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

# gh_auth_gate [LOGIN]: a gate that gh is signed in, and with LOGIN, as
# that user. The log shows who.
gh_auth_gate() {
  if [ -z "${1:-}" ]; then
    gate --timeout 30s "gh is signed in" gh auth status
    return
  fi
  gate --timeout 30s "gh is signed in as $1" bash -c 'got=$(gh api user --jq .login) || exit 1; echo "signed in as $got"; [ "$got" = "$1" ] || { echo "want $1"; exit 1; }' _ "$1"
}

# gh_repo [REMOTE]: set REPO to the GitHub owner/name REMOTE (default
# origin) points at, for gh --repo "$REPO". It runs no step; a remote that
# isn't GitHub's stops the round.
gh_repo() {
  _gh_r=${1:-origin}
  _gh_url=$(git -C "${SWIM_ROOT:-.}" remote get-url "$_gh_r" 2>/dev/null)
  REPO=
  case $_gh_url in
    *github.com[:/]*/*)
      REPO=${_gh_url#*github.com[:/]}
      REPO=${REPO%.git}
      REPO=${REPO%/}
      ;;
  esac
  case $REPO in
    */*/* | /* | */ | '') REPO= ;;
  esac
  if [ -z "$REPO" ]; then
    _gh_fail "gh_repo: $_gh_r is a GitHub repo" "remote $_gh_r ($_gh_url) isn't a GitHub repo"
    return 1
  fi
}

# _gh_need_repo: REPO, from origin if unset.
_gh_need_repo() {
  [ -n "${REPO:-}" ] || gh_repo
}

# gh_pr_state_gate N STATE...: a gate that pull request N is in one of the
# states (OPEN, MERGED, CLOSED, or DRAFT for an open draft). The log shows
# its state, head commit and review decision.
gh_pr_state_gate() {
  _gh_pr=${1:-}
  case $_gh_pr in
    '' | *[!0-9]*)
      _gh_fail "gh_pr_state_gate: a PR number" "gh_pr_state_gate needs a pull request number, got '$_gh_pr'"
      return 1
      ;;
  esac
  shift
  if [ $# -eq 0 ]; then
    _gh_fail "gh_pr_state_gate: a state" "gh_pr_state_gate $_gh_pr needs the states it may be in (OPEN, MERGED, CLOSED, DRAFT)"
    return 1
  fi
  _gh_need_repo || return 1
  _gh_jq='[(if .isDraft then "DRAFT" else .state end), .headRefOid, (.reviewDecision // "")] | join(" ")'
  gate --timeout 1m "PR $_gh_pr is $*" bash -c 'pr=$1 repo=$2 jq=$3; shift 3; got=$(gh pr view "$pr" --repo "$repo" --json state,isDraft,headRefOid,reviewDecision --jq "$jq") || exit 1; echo "PR $pr in $repo: $got"; for want in "$@"; do [ "${got%% *}" = "$want" ] && exit 0; done; echo "want $*"; exit 1' _ "$_gh_pr" "$REPO" "$_gh_jq" "$@"
}

# gh_pr_head_gate N SHA: a gate that pull request N's head is still SHA
# (what was reviewed or tested is what the PR holds).
gh_pr_head_gate() {
  case ${1:-} in
    '' | *[!0-9]*)
      _gh_fail "gh_pr_head_gate: a PR number" "gh_pr_head_gate needs a pull request number, got '${1:-}'"
      return 1
      ;;
  esac
  if [ -z "${2:-}" ]; then
    _gh_fail "gh_pr_head_gate: a commit" "gh_pr_head_gate $1 needs the commit it should be at"
    return 1
  fi
  _gh_need_repo || return 1
  gate --timeout 1m "PR $1 is at $2" bash -c 'got=$(gh pr view "$1" --repo "$2" --json headRefOid --jq .headRefOid) || exit 1; echo "PR $1 head: $got"; case $got in "$3"*) exit 0 ;; esac; echo "want $3"; exit 1' _ "$1" "$REPO" "$2"
}
