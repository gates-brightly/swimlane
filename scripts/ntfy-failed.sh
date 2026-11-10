#!/usr/bin/env bash
# ntfy-failed.sh WHAT: tell ntfy a Forgejo workflow failed (.forgejo/workflows
# run it with `if: failure()`). WHAT names the workflow ("CI", "release").
#
# NTFY_URL is the topic's URL (https://ntfy.sh/<topic>, or your own
# server's); unset, nothing is sent. NTFY_TOKEN, if set, sends it as that
# token's user (a private topic). RUN_URL is the run's page, for the
# notification's click and text. GITHUB_REF_NAME and GITHUB_EVENT_NAME are
# the runner's.
set -euo pipefail

what=${1:?usage: ntfy-failed.sh WHAT}
if [ -z "${NTFY_URL:-}" ]; then
  echo "NTFY_URL isn't set: no notification"
  exit 0
fi
auth=()
if [ -n "${NTFY_TOKEN:-}" ]; then
  auth=(-H "Authorization: Bearer $NTFY_TOKEN")
fi
curl -fsS --retry 3 -o /dev/null ${auth[@]+"${auth[@]}"} \
  -H "Title: swimlane $what failed on ${GITHUB_REF_NAME:-?}" \
  -H "Priority: high" \
  -H "Tags: x,swimlane" \
  -H "Click: ${RUN_URL:-}" \
  -d "$(git log -1 --format='%h %s' 2>/dev/null || echo '?') (${GITHUB_EVENT_NAME:-?}): ${RUN_URL:-}" \
  "$NTFY_URL"
echo "sent to ntfy"
