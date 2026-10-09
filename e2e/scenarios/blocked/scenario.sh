# blocked: swim never pushes, commits or pulls. One lane tries each built-in
# through a different path: a literal `git push` (refused by the pre-flight
# scan, so the lane never starts), `git commit` built at runtime inside a gate
# (refused by swim step), and `git -C . pull` bare in the script (refused by
# the git shim). Every blocked lane fails, its dependents are skipped, a lane
# that only reads git passes, and the scratch repo's history is unchanged.
DESCRIPTION="git push/commit/pull refused via pre-flight, step and shim; dependents skipped; repo unchanged"
LANES=6

scenario_setup() {
  cp "$SCENARIO_DIR"/lanes/lane.*.sh .
  GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@example.com GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@example.com \
    git commit -q --allow-empty -m base
  git rev-parse HEAD > "$OUT/head.before"
  git status --porcelain | grep -v ' .swim.lock$' > "$OUT/status.before"
}

scenario_check() {
  expect_eq "swim run exit" "$RUN_EXIT" 1
  expect_eq "blocked lanes failed" "$(lanes_with FAIL)" "1 2 3"
  expect_eq "their dependents were skipped" "$(lanes_with SKIP)" "4 5"
  expect_eq "the read-only lane passed" "$(lanes_with PASS)" "6"
  expect_match "lane 1 refused before starting" "$(cat "$OUT/run.out")" 'blocked: lane\.1\.sh:[0-9]+ matches "git push"'
  expect_eq "lane 1 never started" "$("$SWIM" log 1 --raw 2>/dev/null | grep -c '^== ROUND ')" "0"
  expect_match "lane 1 exit code 87" "$("$SWIM" status --yaml | awk '/- lane: 1$/,/- lane: 2$/' | grep 'exit_code:')" 'exit_code: 87'
  expect_match "lane 2 BLOCKED at the step" "$("$SWIM" log 2 --raw)" '^  BLOCKED  commit results \(matched "git commit"; swim never writes to git\)'
  expect_match "lane 3 BLOCKED by the shim" "$("$SWIM" log 3 --raw)" '^  BLOCKED  git pull \(git shim'
  expect_eq "no blocked lane ran its next step" "$(for n in 2 3; do "$SWIM" log $n --raw; done | grep -c 'PASS  after the blocked command')" "0"
  expect_eq "repo history unchanged" "$(git rev-parse HEAD)" "$(cat "$OUT/head.before")"
  # .swim.lock is written by swim run itself (a file meant to be committed), not by git.
  expect_eq "working tree unchanged" "$(git status --porcelain | grep -v ' .swim.lock$')" "$(cat "$OUT/status.before")"
  checks_passed
}
