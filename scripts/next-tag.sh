#!/usr/bin/env bash
# Print the next release tag and the commands to publish it (runs nothing):
#   scripts/next-tag.sh            (make next-tag)
#
# Tags are v0.<YYYYMMDD>.<index>: today's date, and 1 + the highest index
# already tagged today (1 for the day's first release). Older tags
# (v0.<breaking>.<YYYYMMDD>) are fine and ignored; any other v-tag gets a
# warning, since `git push --tags` would publish it too.
# Bash 3.2 compatible (macOS).
set -u

cd "$(dirname "$0")/.." || exit 1

today=$(date +%Y%m%d)
top=0
for t in $(git tag -l 'v*'); do
  case "$t" in
    v0.$today.*)
      i=${t#v0.$today.}
      case "$i" in ''|*[!0-9]*) ;; *) [ "$i" -gt "$top" ] && top=$i; continue ;; esac ;;
  esac
  rest=${t#v0.}; a=${rest%%.*}; b=${rest#*.}
  case "$t:$a:$b" in
    v0.*:*[!0-9]*:*|v0.*:*:*[!0-9]*|v0.*::*|v0.*:*:) ;;   # not two numbers after v0.
    v0.*) continue ;;                                      # v0.<date>.<i> or the older v0.<b>.<date>
  esac
  echo "warning: tag $t isn't a release tag (v0.<YYYYMMDD>.<index>); git push --tags would publish it. Delete it: git tag -d $t; git push origin :refs/tags/$t" >&2
done

tag="v0.$today.$((top + 1))"
echo "git tag $tag"
echo "git push --tags"
