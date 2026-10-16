# notify: post a message to a Slack incoming webhook or a generic JSON
# webhook. bash 3.2.

# notify [--strict] CHANNEL TEXT - post {"channel": CHANNEL, "text": TEXT}
# to the webhook in the secret SLACK_WEBHOOK_URL (never an argument: it's a
# credential). CHANNEL - leaves the channel to the webhook. A missed
# notification shouldn't fail a deploy, so it fails soft: it says why and
# passes, unless --strict.
notify() {
  local strict url body tmp channel
  strict=
  if [ "${1:-}" = --strict ]; then
    strict=1
    shift
  fi
  if [ $# -ne 2 ]; then
    echo 'notify: usage: mod notify [--strict] CHANNEL TEXT (CHANNEL: "#name" or -)' >&2
    return 2
  fi
  channel=$1
  if ! url=$("${SWIM_BIN:-swim}" secret get SLACK_WEBHOOK_URL) || [ -z "$url" ]; then
    _notify_missed "$strict" "no webhook: the secret SLACK_WEBHOOK_URL isn't available to this lane"
    return $?
  fi
  body="{\"text\": \"$(_notify_json "$2")\"}"
  if [ "$channel" != - ]; then
    body="{\"channel\": \"$(_notify_json "$channel")\", \"text\": \"$(_notify_json "$2")\"}"
  fi
  tmp=$(mktemp "${TMPDIR:-/tmp}/swim-notify.XXXXXX") || return 1
  printf '%s' "$body" >"$tmp"
  # The URL goes to curl on its standard input, never in its arguments,
  # which ps shows.
  url=$(_notify_json "$url")
  if printf 'url = "%s"\n' "$url" | curl -sS -f -m 20 -K - -H 'Content-Type: application/json' --data-binary "@$tmp" -o /dev/null; then
    rm -f "$tmp"
    echo "notify: sent to $channel"
    return 0
  fi
  rm -f "$tmp"
  _notify_missed "$strict" "the webhook didn't take it"
}

# _notify_json S - S escaped for a JSON string. The characters come from
# variables, quoted in the pattern and the replacement: bash 3.2 and 5.2
# (which unescapes an unquoted replacement) agree on that form.
_notify_json() {
  local s bs q nl cr tab
  s=$1
  bs='\'
  q='"'
  nl=$'\n'
  cr=$'\r'
  tab=$'\t'
  s=${s//"$bs"/"$bs$bs"}
  s=${s//"$q"/"$bs$q"}
  s=${s//"$nl"/"${bs}n"}
  s=${s//"$cr"/"${bs}r"}
  s=${s//"$tab"/"${bs}t"}
  printf '%s' "$s"
}

# _notify_missed STRICT REASON - a notification that didn't go out.
_notify_missed() {
  if [ -n "$1" ]; then
    echo "notify: not sent: $2" >&2
    return 1
  fi
  echo "notify: not sent: $2 (soft: the round goes on; --strict fails the step)"
  return 0
}
