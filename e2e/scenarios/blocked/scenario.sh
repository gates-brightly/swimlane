# blocked: swim blocks only what a repo lists in blocked_commands (swim.yml
# here: terraform destroy). A literal match is refused by the pre-flight scan,
# so the lane never starts; a command built at runtime inside a gate is
# refused by swim step. Blocked lanes fail and their dependents are skipped.
# git is a lane's to run: a lane that commits passes and its commits land.
DESCRIPTION="blocked_commands refused via pre-flight and step; dependents skipped; git commits allowed"
LANES=6

scenario_setup() {
  cp "$SCENARIO_DIR"/lanes/lane.*.sh .
  printf 'blocked_commands: ["terraform destroy"]\n' > swim.yml
  GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@example.com GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@example.com \
    git commit -q --allow-empty -m base
  git rev-parse HEAD > "$OUT/head.before"
  git status --porcelain | grep -v ' .swim.lock$' > "$OUT/status.before"
}

scenario_check() {
  expect_eq "swim run exit" "$RUN_EXIT" 1
  expect_eq "blocked lanes failed" "$(lanes_with FAIL)" "1 2"
  expect_eq "their dependents were skipped" "$(lanes_with SKIP)" "4 5"
  expect_eq "the git lanes passed" "$(lanes_with PASS)" "3 6"
  expect_match "lane 1 refused before starting" "$(cat "$OUT/run.yml")" 'blocked: lane\.1\.sh:[0-9]+ matches "terraform destroy"'
  # Its one round is the refusal's: "not started", no step ran.
  expect_eq "lane 1 never started" "$("$SWIM" log 1 --raw 2>/dev/null | grep -c '^== ROUND ')" "1"
  expect_match "lane 1's round says why" "$("$SWIM" log 1 --raw)" '^  STOP  not started: blocked: lane\.1\.sh:[0-9]+ matches "terraform destroy"'
  expect_eq "lane 1 ran no step" "$("$SWIM" log 1 --raw | grep -cE '^  (PASS|FAIL)  ')" "0"
  expect_match "lane 1 exit code 87" "$("$SWIM" status --yaml | awk '/- lane: 1$/,/- lane: 2$/' | grep 'exit_code:')" 'exit_code: 87'
  expect_match "lane 2 BLOCKED at the step" "$("$SWIM" log 2 --raw)" '^  BLOCKED  tear down \(matched "terraform destroy"; blocked_commands\)'
  expect_eq "no blocked lane ran its next step" "$("$SWIM" log 2 --raw | grep -c 'PASS  after the blocked command')" "0"
  expect_eq "lane 3's two commits landed" "$(git rev-list --count "$(cat "$OUT/head.before")..HEAD")" "2"
  # .swim.lock is written by swim run itself (a file meant to be committed), not by git.
  expect_eq "working tree unchanged" "$(git status --porcelain | grep -v ' .swim.lock$')" "$(cat "$OUT/status.before")"
  checks_passed
}
