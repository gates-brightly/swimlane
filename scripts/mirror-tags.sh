#!/usr/bin/env bash
# Mirror origin's release tags to fj:  scripts/mirror-tags.sh  (just mirror-tags)
#   DRY=1 scripts/mirror-tags.sh     print what it would push, change nothing
#
# origin (GitHub) is authoritative: every release tag it has that fj
# (Forgejo) lacks is fetched from origin and pushed to fj. A tag fj has
# that names something else is reported, never overwritten (that's a
# decision: delete it on fj, then run this again). Tags only fj has are
# reported too. See scripts/tag-remotes.sh for the remote names.
# Bash 3.2 compatible (macOS).
set -u

cd "$(dirname "$0")/.." || exit 1
. ./scripts/tag-remotes.sh

die() { echo "mirror-tags: $*" >&2; exit 1; }

check_tag_remote || exit 1
[ -n "$MIRROR_REMOTE" ] || die "MIRROR_REMOTE is empty: nothing to mirror to"
git remote get-url "$MIRROR_REMOTE" >/dev/null 2>&1 || die "no remote $MIRROR_REMOTE (Forgejo): git remote add $MIRROR_REMOTE <url>"

tmp=${TMPDIR:-/tmp}/mirror-tags.$$
trap 'rm -f "$tmp".*' EXIT
git ls-remote --tags --refs "$TAG_REMOTE" 'v0.*' | sed 's|refs/tags/||' | sort -k2 >"$tmp.src" || die "couldn't list $TAG_REMOTE's tags"
git ls-remote --tags --refs "$MIRROR_REMOTE" 'v0.*' | sed 's|refs/tags/||' | sort -k2 >"$tmp.dst" || die "couldn't list $MIRROR_REMOTE's tags"

missing=""
while read -r sha tag; do
  have=$(awk -v t="$tag" '$2 == t {print $1}' "$tmp.dst")
  if [ -z "$have" ]; then
    missing="$missing $tag"
  elif [ "$have" != "$sha" ]; then
    echo "warning: $MIRROR_REMOTE's $tag differs from $TAG_REMOTE's; left alone. To replace it: git push $MIRROR_REMOTE :refs/tags/$tag, then just mirror-tags" >&2
  fi
done <"$tmp.src"
while read -r _ tag; do
  awk -v t="$tag" '$2 == t {f=1} END {exit !f}' "$tmp.src" ||
    echo "warning: $MIRROR_REMOTE has $tag, which $TAG_REMOTE doesn't; not a release. Delete it: git push $MIRROR_REMOTE :refs/tags/$tag" >&2
done <"$tmp.dst"

if [ -z "$missing" ]; then
  echo "$MIRROR_REMOTE has every release tag $TAG_REMOTE has"
  exit 0
fi
if [ -n "${DRY:-}" ]; then
  echo "would mirror to $MIRROR_REMOTE:$missing"
  exit 0
fi
for tag in $missing; do
  # From origin, as origin has it (a local tag of that name may differ).
  git fetch -q "$TAG_REMOTE" "+refs/tags/$tag:refs/tags/$tag" || die "couldn't fetch $tag from $TAG_REMOTE"
  mirror_tag "$tag"
done
