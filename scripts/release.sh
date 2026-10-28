#!/usr/bin/env bash
# Tag a swim release: scripts/release.sh v0.<YYYYMMDD>.<index>  (just release v0...)
#
# Refuses unless everything agrees:
#   - V is v0.<YYYYMMDD>.<index>, the date is today, the tag doesn't exist,
#     the index is the day's next (DRY=1 just next-tag prints it), and no
#     release tag is newer
#   - CHANGELOG.md's newest revision is 0.<date>.<index> (v0.<date>.<index>, <date>),
#     and ## Unreleased above it is empty
#   - the working tree is clean, and the changelog and version tests pass
# Then it creates an annotated tag on HEAD. It never pushes: that's yours to do
# (origin, which is GitHub and authoritative, then just mirror-tags for fj).
# "Exists" and "the day's next index" are judged by origin's tags.
# Compatibility isn't in the version: a change to lane scripts, logs or state
# bumps its syntax (internal/syntax), which .swim.lock pins.
# Bash 3.2 compatible (macOS).
set -u

die() { echo "release: $*" >&2; exit 1; }

V=${1:-}
[ -n "$V" ] && [ "$V" != latest ] || die "usage: just release v0.<YYYYMMDD>.<index>  (DRY=1 just next-tag prints it)"
case "$V" in v0.*) ;; *) die "$V: release tags are v0.<YYYYMMDD>.<index>" ;; esac
rest=${V#v0.}
D=${rest%%.*}
I=${rest#*.}
case "$D" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;; *) die "$V: <YYYYMMDD> must be 8 digits" ;; esac
case "$I" in ''|0*|*[!0-9]*) die "$V: <index> must be 1, 2, ..." ;; esac

cd "$(dirname "$0")/.." || exit 1
. ./scripts/tag-remotes.sh
check_tag_remote || exit 1
published=$(authoritative_tags) || die "couldn't list $TAG_REMOTE's tags (git ls-remote --tags $TAG_REMOTE)"

iso=$(echo "$D" | sed 's/^\(....\)\(..\)\(..\)$/\1-\2-\3/')
today=$(date +%Y%m%d)
today_utc=$(date -u +%Y%m%d)
[ "$D" = "$today" ] || [ "$D" = "$today_utc" ] || die "$V is dated $D, but today is $today (UTC $today_utc)"

for t in $published; do
  [ "$t" = "$V" ] && die "$TAG_REMOTE already has $V; the next one: DRY=1 just next-tag"
done
git rev-parse -q --verify "refs/tags/$V" >/dev/null && die "a local tag $V exists that $TAG_REMOTE doesn't have; delete it (git tag -d $V) and run again"
top=0
for t in $published; do
  case "$t" in v0.*) ;; *) continue ;; esac
  tr=${t#v0.}; td=${tr%%.*}; ti=${tr#*.}
  case "$td" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;; *) continue ;; esac   # an older v0.<b>.<date> tag
  case "$ti" in ''|*[!0-9]*) continue ;; esac
  [ "$td" -gt "$D" ] && die "tag $t is newer than $V; releases only move forward"
  [ "$td" = "$D" ] && [ "$ti" -gt "$top" ] && top=$ti
done
[ "$I" -eq $((top + 1)) ] || die "$V: today's next index is $((top + 1)) (DRY=1 just next-tag)"

first=$(awk '/^## /{ if ($2 == "Unreleased") { u = 1; next } print; exit } u && NF { print "UNRELEASED-NOT-EMPTY"; exit }' CHANGELOG.md)
[ "$first" != UNRELEASED-NOT-EMPTY ] || die "CHANGELOG.md: ## Unreleased still has content. Rename it to ## 0.$D.$I ($V, $iso) and add an empty ## Unreleased above"
case "$first" in
  "## 0.$D.$I ($V, $iso)") ;;
  *) die "CHANGELOG.md's newest revision is '${first#\#\# }', not '0.$D.$I ($V, $iso)'" ;;
esac

[ -z "$(git status --porcelain --untracked-files=no)" ] || die "the working tree isn't clean; commit the release first (git status)"   # untracked files aren't in the tag

go test ./internal/changelog ./internal/version >/dev/null || die "changelog/version tests fail: go test ./internal/changelog ./internal/version"

git tag -a "$V" -m "swim 0.$D.$I" -m "Install: go install github.com/gates-brightly/swimlane/cmd/swim@$V" || die "git tag failed"
echo "tagged $V at $(git rev-parse --short HEAD) (swim 0.$D.$I)"
echo "check it, then publish:  git push $TAG_REMOTE $V${MIRROR_REMOTE:+ && just mirror-tags}"
