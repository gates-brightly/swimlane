# ci-changed: dag99 runs once and passes; then a commit rewrites two lane
# scripts, and `swim ci --changed=<base>` runs exactly those two (their
# parents already passed), with every other lane untouched.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="swim ci --changed: a commit touching lanes 30 and 60 runs only those two"

commit() {
  GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@example.com GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@example.com \
    git commit -q -m "$1"
}

scenario_run() {
  git add -f lane.*.sh >/dev/null && commit lanes
  "$SWIM" all --plain >"$OUT/first.out" 2>&1
  FIRST_EXIT=$?
  BASE=$(git rev-parse HEAD)
  for n in 30 60; do
    printf '\n# touched by ci-changed\n' >>"lane.$n.sh"
  done
  git add -f lane.30.sh lane.60.sh && commit "touch 30 and 60"
  CI=true "$SWIM" ci --changed="$BASE" >"$OUT/ci.out" 2>&1
  RUN_EXIT=$?
}

rounds_of() { "$SWIM" log "$1" --raw 2>/dev/null | grep -c '^== ROUND '; }

scenario_check() {
  local n bad=""
  expect_eq "first run passed" "$FIRST_EXIT" 0
  expect_eq "swim ci exit" "$RUN_EXIT" 0
  expect_match "selected the changed lanes" "$(cat "$OUT/ci.out")" '^selected: --changed: since [0-9a-f]+; changed: lane\.30\.sh, lane\.60\.sh$'
  expect_match "ran lanes 30 and 60" "$(cat "$OUT/ci.out")" '^run r-[0-9TZ-]+[0-9a-f]* · lanes 30, 60$'
  for n in $(seq 1 99); do
    case $n in
      30|60) [ "$(rounds_of $n)" = 2 ] || bad="$bad $n" ;;
      *) [ "$(rounds_of $n)" = 1 ] || bad="$bad $n" ;;
    esac
  done
  expect_eq "only the changed lanes ran again" "${bad# }" ""
  checks_passed
}
