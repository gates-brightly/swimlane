#!/usr/bin/env bash
# Tag the current commit with the next release tag and push that tag:
#   scripts/next-tag.sh            (just next-tag)
#   DRY=1 scripts/next-tag.sh      (DRY=1 just next-tag: print the tag, change nothing)
#
# Remotes: origin is GitHub, and its tags are authoritative: the next index
# comes from origin's tags (git ls-remote), not local ones, and the tag is
# pushed there first. fj (Forgejo) is a mirror: the tag is pushed there
# after, and a failed mirror push only warns (just mirror-tags retries).
# TAG_REMOTE and MIRROR_REMOTE override the names (MIRROR_REMOTE= skips it).
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
. ./scripts/tag-remotes.sh

die() { echo "next-tag: $*" >&2; exit 1; }

check_tag_remote || exit 1
published=$(authoritative_tags) || die "couldn't list $TAG_REMOTE's tags (git ls-remote --tags $TAG_REMOTE)"

today=$(date +%Y%m%d)
top=0
for t in $published; do
  case "$t" in
    v0.$today.*)
      i=${t#v0.$today.}
      case "$i" in ''|*[!0-9]*) ;; *) [ "$i" -gt "$top" ] && top=$i ;; esac ;;
  esac
done
for t in $(git tag -l 'v*'); do
  rest=${t#v0.}; a=${rest%%.*}; b=${rest#*.}
  case "$t:$a:$b" in
    v0.*:*[!0-9]*:*|v0.*:*:*[!0-9]*|v0.*::*|v0.*:*:) ;;   # not two numbers after v0.
    v0.*) continue ;;                                      # v0.<date>.<i> or the older v0.<b>.<date>
  esac
  echo "warning: tag $t isn't a release tag (v0.<YYYYMMDD>.<index>); git push --tags would publish it. Delete it: git tag -d $t; git push $TAG_REMOTE :refs/tags/$t" >&2
done

tag="v0.$today.$((top + 1))"
head=$(git rev-parse --short HEAD) || exit 1
for t in $(git tag --points-at HEAD 'v0.*'); do
  case "$t" in v0.[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9].*)
    echo "next-tag: $head is already released as $t" >&2; exit 1 ;;
  esac
done
if [ -n "${DRY:-}" ]; then
  echo "$tag  (would tag $head, push it to $TAG_REMOTE${MIRROR_REMOTE:+, then $MIRROR_REMOTE}: just next-tag)"
  exit 0
fi

[ -z "$(git status --porcelain --untracked-files=no)" ] || die "the working tree has changes; commit or stash them first (git status)"   # untracked files aren't in the tag
git fetch -q "$TAG_REMOTE" main || die "couldn't fetch $TAG_REMOTE main"
git merge-base --is-ancestor HEAD "$TAG_REMOTE/main" || die "$head isn't on $TAG_REMOTE's main yet; push it first (git push $TAG_REMOTE main)"
# A local tag of that name that origin doesn't have is a leftover (an
# earlier push that failed, or a tag made by hand): origin decides.
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  die "a local tag $tag exists that $TAG_REMOTE doesn't have; delete it (git tag -d $tag) and run again"
fi

git tag -a "$tag" -m "swim 0.$today.$((top + 1))" -m "Install: go install github.com/gates-brightly/swimlane/cmd/swim@$tag" || die "git tag failed"
if ! git push "$TAG_REMOTE" "refs/tags/$tag"; then
  git tag -d "$tag" >/dev/null
  die "push to $TAG_REMOTE failed; the local tag $tag was removed"
fi
echo "tagged $head as $tag and pushed it to $TAG_REMOTE"
mirror_tag "$tag"
echo "install: go install github.com/gates-brightly/swimlane/cmd/swim@$tag"
