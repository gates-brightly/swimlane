# ci-github: dag99-cascade under `swim ci` as GitHub Actions. Each injected
# failure gets an ::error annotation, the lanes skipped below them get one
# notice between them, the job summary lists every lane, and the JUnit
# report has a suite for each of the 99 lanes.
. "$E2E/scenarios/dag99/scenario.sh"
DESCRIPTION="swim ci (GitHub): ::error per failure, one notice for the skips, job summary, JUnit for 99 lanes"
FAIL_LANES="23 38 41"

scenario_run() {
  git add -f lane.*.sh >/dev/null && GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@example.com \
    GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@example.com git commit -q -m lanes
  E2E_FAIL="$FAIL_LANES" GITHUB_ACTIONS=true CI=true GITHUB_STEP_SUMMARY="$OUT/summary.md" \
    "$SWIM" ci --junit "$OUT/swim.xml" >"$OUT/ci.out" 2>&1
  RUN_EXIT=$?
}

scenario_check() {
  local errs down l want_fail=""
  expect_eq "swim ci exit (failures injected)" "$RUN_EXIT" 1
  # Injected lanes downstream of another injected lane are skipped, not failed.
  down=" $(e2etool descendants $FAIL_LANES) "
  for l in $FAIL_LANES; do case "$down" in *" $l "*) ;; *) want_fail="$want_fail $l" ;; esac; done
  errs=$(grep '^::error title=swim ' "$OUT/ci.out" | sed 's/^::error title=swim \([0-9]*\)%3A.*/\1/' | sort -n | uniq | tr '\n' ' ')
  expect_eq "an ::error for each failed lane" "${errs% }" "${want_fail# }"
  expect_eq "one notice for all the skipped lanes" "$(grep -c '^::notice title=swim%3A [0-9]* lanes skipped::' "$OUT/ci.out")" 1
  expect_match "failed lanes stay open" "$(cat "$OUT/ci.out")" '^==== swim 23 FAIL - '
  expect_match "passed lanes fold" "$(cat "$OUT/ci.out")" '^::group::swim 1 PASS - '
  expect_eq "job summary has every lane" "$(grep -c '^| swim [0-9]* |' "$OUT/summary.md")" 99
  expect_eq "JUnit has a suite per lane" "$(grep -c '<testsuite ' "$OUT/swim.xml")" 99
  expect_match "results tagged with the commit" "$(grep '^    commit:' .swim/status.yml | head -1)" "commit: $(git rev-parse HEAD)"
  checks_passed
}
