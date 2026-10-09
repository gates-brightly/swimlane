#!/usr/bin/env bash
# e2e scenario runner: each scenario gets a fresh git repo and swim config,
# writes its lane scripts, runs them through the swim binary built from this
# checkout, and checks the outcome. Offline: curl is a fake serving fixtures/.
# Lane scripts do their work with e2etool (e2e/cmd/e2etool), built alongside.
#
#   e2e/run.sh                 run every scenario
#   e2e/run.sh dag99 ...       run the named scenarios
#   e2e/run.sh -l              list scenarios
#   e2e/run.sh -k ...          keep scratch repos (paths printed) for digging in
#
#   SWIM_E2E_BIN=/path/swim    test that binary instead of building one
#   E2E_CURL_DELAY=0.01        seconds per fake curl request
#
# Scenario contract (e2e/scenarios/<name>/scenario.sh, sourced in a subshell
# whose cwd is the scratch repo, after lib/common.sh):
#   DESCRIPTION="..."           one line for -l and the report
#   LANES=N                     lanes to configure (1..99)
#   scenario_setup              write lane.N.sh files into the repo
#   scenario_run                run swim (default: swim_run on lanes 1..LANES)
#   scenario_check              assert on the outcome; non-zero fails the scenario
# Bash 3.2 compatible (macOS).
set -u

E2E=$(cd "$(dirname "$0")" && pwd)
ROOT=$(dirname "$E2E")
keep=0

scenarios() { for d in "$E2E"/scenarios/*/; do [ -f "$d/scenario.sh" ] && basename "$d"; done; }

while [ $# -gt 0 ]; do
  case "$1" in
    -k) keep=1; shift ;;
    -l)
      for s in $(scenarios); do
        printf '  %-16s %s\n' "$s" "$(DESCRIPTION=; . "$E2E/scenarios/$s/scenario.sh" >/dev/null 2>&1; echo "$DESCRIPTION")"
      done
      exit 0 ;;
    -h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "e2e: unknown flag $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
selected=${*:-$(scenarios)}
for s in $selected; do
  [ -f "$E2E/scenarios/$s/scenario.sh" ] || { echo "e2e: no scenario '$s' (e2e/run.sh -l lists them)" >&2; exit 2; }
done

base=$(mktemp -d "${TMPDIR:-/tmp}/swim-e2e.XXXXXX") || exit 1
cleanup() { [ "$keep" = 1 ] || rm -rf "$base"; }
trap cleanup EXIT

mkdir -p "$base/bin"
if [ -n "${SWIM_E2E_BIN:-}" ]; then
  SWIM=$SWIM_E2E_BIN
else
  SWIM=$base/bin/swim
  echo "building swim from $ROOT"
  (cd "$ROOT" && go build -buildvcs=false -o "$SWIM" ./cmd/swim) || { echo "e2e: build failed" >&2; exit 1; }
fi
# The scenarios' lane scripts call e2etool for their work and checks.
(cd "$ROOT" && go build -buildvcs=false -o "$base/bin/e2etool" ./e2e/cmd/e2etool) || { echo "e2e: e2etool build failed" >&2; exit 1; }

report=""
failed=0
for s in $selected; do
  dir=$base/$s
  repo=$dir/repo
  mkdir -p "$repo" "$dir/out" "$dir/xdg"
  echo
  echo "=== $s"
  start=$(date +%s)
  (
    export SWIM E2E SCENARIO_DIR="$E2E/scenarios/$s" OUT="$dir/out" E2E_FIXTURES="$E2E/fixtures"
    export XDG_CONFIG_HOME="$dir/xdg" PATH="$E2E/lib/fakebin:$base/bin:$(dirname "$SWIM"):$PATH"
    export NO_COLOR=1 SWIM_BIN="$SWIM"
    # Hermetic: scenarios set what they need. Inside a swim lane (this repo's
    # CI runs through swim ci) or a CI runner, drop the lane's SWIM_* vars,
    # the CI provider's vars and the lane's git shim on PATH.
    for v in $(env | sed -nE 's/^((SWIM|GITHUB|GITLAB|CI)_[A-Za-z0-9_]*)=.*/\1/p'); do
      [ "$v" = SWIM_BIN ] || unset "$v"
    done
    unset CI STEP_LOG E2E_FAIL
    PATH=$(printf '%s\n' "$PATH" | tr ':' '\n' | grep -v '/\.swim/bin$' | paste -s -d: -)
    export SWIM_BIN="$SWIM"
    cd "$repo" || exit 1
    git init -q . || exit 1
    "$SWIM" init >/dev/null || exit 1
    . "$E2E/lib/common.sh"
    LANES=4
    scenario_run() { swim_run $(seq 1 "$LANES"); }
    . "$SCENARIO_DIR/scenario.sh"
    "$SWIM" config --lanes "$LANES" >/dev/null || exit 1
    scenario_setup || { echo "  setup failed"; exit 1; }
    scenario_run
    if [ -f "$OUT/run.yml" ]; then
      echo "  swim run exit $RUN_EXIT: $(lane_results | awk '{c[$2]++} END {printf "%d pass, %d fail, %d skip", c["PASS"], c["FAIL"], c["SKIP"]}')"
    else
      echo "  swim exit $RUN_EXIT"
    fi
    scenario_check
  )
  rc=$?
  secs=$(( $(date +%s) - start ))
  if [ $rc -eq 0 ]; then result=PASS; else result=FAIL; failed=$((failed + 1)); fi
  [ -f "$dir/out/info" ] && cat "$dir/out/info"
  [ "$keep" = 1 ] && echo "  kept: $repo"
  report="$report$(printf '  %-16s %-4s %4ss' "$s" "$result" "$secs")
"
done

echo
echo "e2e summary"
printf '%s' "$report"
[ "$keep" = 1 ] && echo "scratch repos kept under $base"
exit $(( failed > 0 ))
