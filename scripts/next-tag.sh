#!/usr/bin/env bash
# Tag the current commit with the next release tag and push that tag:
#   scripts/next-tag.sh            (make next-tag)
#   DRY=1 scripts/next-tag.sh      (make next-tag DRY=1: print the tag, change nothing)
#
# It refuses a working tree with changes, and a commit origin's main
# doesn't have yet (push it first): the tag names what's already
# published. It pushes only the new tag, never other local tags.
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
head=$(git rev-parse --short HEAD) || exit 1
for t in $(git tag --points-at HEAD 'v0.*'); do
  case "$t" in v0.[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9].*)
    echo "next-tag: $head is already released as $t" >&2; exit 1 ;;
  esac
done
if [ -n "${DRY:-}" ]; then
  echo "$tag  (would tag $head and push it: make next-tag)"
  exit 0
fi

die() { echo "next-tag: $*" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || die "the working tree has changes; commit or stash them first (git status)"
git fetch -q origin main || die "couldn't fetch origin main"
git merge-base --is-ancestor HEAD origin/main || die "$head isn't on origin's main yet; push it first (git push origin main)"
git rev-parse -q --verify "refs/tags/$tag" >/dev/null && die "tag $tag already exists"

git tag -a "$tag" -m "swim 0.$today.$((top + 1))" -m "Install: go install github.com/gates-brightly/swimlane/cmd/swim@$tag" || die "git tag failed"
if ! git push origin "refs/tags/$tag"; then
  git tag -d "$tag" >/dev/null
  die "push failed; the local tag $tag was removed"
fi
echo "tagged $head as $tag and pushed it"
echo "install: go install github.com/gates-brightly/swimlane/cmd/swim@$tag"
