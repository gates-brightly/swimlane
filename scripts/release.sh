#!/usr/bin/env bash
# Tag a swim release: scripts/release.sh v0.<breaking>.<YYYYMMDD>  (make release V=...)
#
# Refuses unless everything agrees, so a tag can't ship code that reports the
# wrong version (as v0.3.20261009 did):
#   - V is v0.<breaking>.<YYYYMMDD>, the date is today, the tag doesn't exist,
#     and no existing release tag is newer
#   - <breaking> is Breaking in internal/version/version.go
#   - CHANGELOG.md's newest revision is <breaking>.<date> (v0.<breaking>.<date>, ...),
#     and ## Unreleased above it is empty
#   - the working tree is clean, and the changelog and version tests pass
# Then it creates an annotated tag on HEAD. It never pushes: that's yours to do.
# Bash 3.2 compatible (macOS).
set -u

die() { echo "release: $*" >&2; exit 1; }

V=${1:-}
[ -n "$V" ] && [ "$V" != latest ] || die "usage: make release V=v0.<breaking>.<YYYYMMDD>"
case "$V" in v0.*) ;; *) die "$V: release tags are v0.<breaking>.<YYYYMMDD>" ;; esac
rest=${V#v0.}
N=${rest%%.*}
D=${rest#*.}
case "$N" in ''|*[!0-9]*) die "$V: <breaking> must be a number" ;; esac
case "$D" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;; *) die "$V: <YYYYMMDD> must be 8 digits" ;; esac

cd "$(dirname "$0")/.." || exit 1

breaking=$(sed -n 's/^const Breaking = \([0-9][0-9]*\)$/\1/p' internal/version/version.go)
iso=$(echo "$D" | sed 's/^\(....\)\(..\)\(..\)$/\1-\2-\3/')
[ -n "$breaking" ] || die "can't find 'const Breaking = N' in internal/version/version.go"
[ "$N" = "$breaking" ] || die "$V says breaking version $N, but internal/version has Breaking = $breaking. Set Breaking = $N (a second release on the same day bumps it), or tag v0.$breaking.$D"

today=$(date +%Y%m%d)
today_utc=$(date -u +%Y%m%d)
[ "$D" = "$today" ] || [ "$D" = "$today_utc" ] || die "$V is dated $D, but today is $today (UTC $today_utc)"

git rev-parse -q --verify "refs/tags/$V" >/dev/null && die "tag $V already exists. One release per breaking version per day: bump Breaking for another release today"
for t in $(git tag -l 'v0.*'); do
  tr=${t#v0.}; tn=${tr%%.*}; td=${tr#*.}
  case "$tn$td" in *[!0-9]*) continue ;; esac
  if [ "$tn" -gt "$N" ] || { [ "$tn" -eq "$N" ] && [ "$td" -gt "$D" ]; }; then
    die "tag $t is newer than $V; releases only move forward"
  fi
done

first=$(awk '/^## /{ if ($2 == "Unreleased") { u = 1; next } print; exit } u && NF { print "UNRELEASED-NOT-EMPTY"; exit }' CHANGELOG.md)
[ "$first" != UNRELEASED-NOT-EMPTY ] || die "CHANGELOG.md: ## Unreleased still has content. Rename it to ## $N.$D ($V, $iso) and add an empty ## Unreleased above"
case "$first" in
  "## $N.$D ($V, "*) ;;
  *) die "CHANGELOG.md's newest revision is '${first#\#\# }', not '$N.$D ($V, <date>)'" ;;
esac

[ -z "$(git status --porcelain)" ] || die "the working tree isn't clean; commit the release first (git status)"

go test ./internal/changelog ./internal/version >/dev/null || die "changelog/version tests fail: go test ./internal/changelog ./internal/version"

git tag -a "$V" -m "swim $N.$D" -m "Install: go install github.com/gates-brightly/swimlane/cmd/swim@$V" || die "git tag failed"
echo "tagged $V at $(git rev-parse --short HEAD) (swim $N.$D)"
echo "check it, then publish:  git push origin $V"
