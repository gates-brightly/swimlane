#!/usr/bin/env bash
# Tag a swim release: scripts/release.sh v0.<YYYYMMDD>.<index>  (make release V=...)
#
# Refuses unless everything agrees:
#   - V is v0.<YYYYMMDD>.<index>, the date is today, the tag doesn't exist,
#     the index is the day's next (scripts/next-tag.sh prints it), and no
#     release tag is newer
#   - CHANGELOG.md's newest revision is 0.<date>.<index> (v0.<date>.<index>, <date>),
#     and ## Unreleased above it is empty
#   - the working tree is clean, and the changelog and version tests pass
# Then it creates an annotated tag on HEAD. It never pushes: that's yours to do.
# Compatibility isn't in the version: a change to lane scripts, logs or state
# bumps its syntax (internal/syntax), which .swim.lock pins.
# Bash 3.2 compatible (macOS).
set -u

die() { echo "release: $*" >&2; exit 1; }

V=${1:-}
[ -n "$V" ] && [ "$V" != latest ] || die "usage: make release V=v0.<YYYYMMDD>.<index>  (make next-tag prints it)"
case "$V" in v0.*) ;; *) die "$V: release tags are v0.<YYYYMMDD>.<index>" ;; esac
rest=${V#v0.}
D=${rest%%.*}
I=${rest#*.}
case "$D" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;; *) die "$V: <YYYYMMDD> must be 8 digits" ;; esac
case "$I" in ''|0*|*[!0-9]*) die "$V: <index> must be 1, 2, ..." ;; esac

cd "$(dirname "$0")/.." || exit 1

iso=$(echo "$D" | sed 's/^\(....\)\(..\)\(..\)$/\1-\2-\3/')
today=$(date +%Y%m%d)
today_utc=$(date -u +%Y%m%d)
[ "$D" = "$today" ] || [ "$D" = "$today_utc" ] || die "$V is dated $D, but today is $today (UTC $today_utc)"

git rev-parse -q --verify "refs/tags/$V" >/dev/null && die "tag $V already exists; the next one: make next-tag"
top=0
for t in $(git tag -l 'v0.*'); do
  tr=${t#v0.}; td=${tr%%.*}; ti=${tr#*.}
  case "$td" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;; *) continue ;; esac   # an older v0.<b>.<date> tag
  case "$ti" in ''|*[!0-9]*) continue ;; esac
  [ "$td" -gt "$D" ] && die "tag $t is newer than $V; releases only move forward"
  [ "$td" = "$D" ] && [ "$ti" -gt "$top" ] && top=$ti
done
[ "$I" -eq $((top + 1)) ] || die "$V: today's next index is $((top + 1)) (make next-tag)"

first=$(awk '/^## /{ if ($2 == "Unreleased") { u = 1; next } print; exit } u && NF { print "UNRELEASED-NOT-EMPTY"; exit }' CHANGELOG.md)
[ "$first" != UNRELEASED-NOT-EMPTY ] || die "CHANGELOG.md: ## Unreleased still has content. Rename it to ## 0.$D.$I ($V, $iso) and add an empty ## Unreleased above"
case "$first" in
  "## 0.$D.$I ($V, $iso)") ;;
  *) die "CHANGELOG.md's newest revision is '${first#\#\# }', not '0.$D.$I ($V, $iso)'" ;;
esac

[ -z "$(git status --porcelain)" ] || die "the working tree isn't clean; commit the release first (git status)"

go test ./internal/changelog ./internal/version >/dev/null || die "changelog/version tests fail: go test ./internal/changelog ./internal/version"

git tag -a "$V" -m "swim 0.$D.$I" -m "Install: go install github.com/gates-brightly/swimlane/cmd/swim@$V" || die "git tag failed"
echo "tagged $V at $(git rev-parse --short HEAD) (swim 0.$D.$I)"
echo "check it, then publish:  git push origin $V"
