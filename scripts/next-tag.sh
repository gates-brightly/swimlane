#!/usr/bin/env bash
# Print the next release tag and the commands to publish it (runs nothing):
#   scripts/next-tag.sh            (make next-tag)
#
# Tags are v0.<version>.<YYYYMMDD>. The next one is dated today; its version
# is the highest of Breaking (internal/version) and every release tag's, plus
# one if a tag is already dated today (one release per version per day).
# Warns when Breaking doesn't match, since a binary built from the tag would
# then report another version ("a mis-tagged release"), and about tags that
# aren't v0.<version>.<YYYYMMDD>. Bash 3.2 compatible (macOS).
set -u

cd "$(dirname "$0")/.." || exit 1

today=$(date +%Y%m%d)
breaking=$(sed -n 's/^const Breaking = \([0-9][0-9]*\)$/\1/p' internal/version/version.go)
[ -n "$breaking" ] || { echo "next-tag: can't find 'const Breaking = N' in internal/version/version.go" >&2; exit 1; }

top=$breaking
taken=
for t in $(git tag -l 'v*'); do
  case "$t" in
    v0.*) ;;
    *) echo "warning: tag $t isn't v0.<version>.<YYYYMMDD>; ignored (delete it: git tag -d $t; git push origin :refs/tags/$t)" >&2; continue ;;
  esac
  rest=${t#v0.}; n=${rest%%.*}; d=${rest#*.}
  case "$n" in ''|*[!0-9]*) echo "warning: tag $t isn't v0.<version>.<YYYYMMDD>; ignored" >&2; continue ;; esac
  case "$d" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;; *) echo "warning: tag $t isn't v0.<version>.<YYYYMMDD>; ignored" >&2; continue ;; esac
  [ "$n" -gt "$top" ] && top=$n
  [ "$d" = "$today" ] && taken=1
done

next=$top
[ -n "$taken" ] && next=$((top + 1))
tag="v0.$next.$today"

if [ "$next" != "$breaking" ]; then
  echo "warning: internal/version has Breaking = $breaking, but this tag is version $next." >&2
  echo "  A binary built from $tag would report $breaking.$today. Set Breaking = $next and commit it first:" >&2
  echo "    sed -i.bak 's/^const Breaking = $breaking\$/const Breaking = $next/' internal/version/version.go && rm internal/version/version.go.bak" >&2
fi

echo "git tag $tag"
echo "git push --tags"
