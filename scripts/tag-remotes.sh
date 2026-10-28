# Release tags' remotes, sourced by next-tag.sh, release.sh and mirror-tags.sh.
#
# TAG_REMOTE (default origin) is GitHub, and its tags are authoritative:
# what's released is what it has. MIRROR_REMOTE (default fj) is Forgejo, a
# mirror that gets each tag after; set MIRROR_REMOTE= to skip it.
# Bash 3.2 compatible (macOS).

TAG_REMOTE=${TAG_REMOTE-origin}
MIRROR_REMOTE=${MIRROR_REMOTE-fj}

# check_tag_remote refuses a TAG_REMOTE that isn't GitHub: tagging the
# mirror as if it were the authority would fork the release history.
check_tag_remote() {
  url=$(git remote get-url "$TAG_REMOTE" 2>/dev/null) || {
    echo "release tags go to $TAG_REMOTE (GitHub), but there's no remote $TAG_REMOTE: git remote add $TAG_REMOTE https://github.com/gates-brightly/swimlane.git" >&2
    return 1
  }
  case "$url" in
    *github.com[:/]*) ;;
    *)
      echo "release tags go to $TAG_REMOTE, which must be GitHub (its tags are authoritative), but it's $url." >&2
      echo "With GitHub as origin and Forgejo as fj: git remote rename origin fj && git remote rename <github-remote> origin" >&2
      return 1 ;;
  esac
}

# authoritative_tags lists TAG_REMOTE's tags (names only, no ^{} peels).
authoritative_tags() {
  git ls-remote --tags --refs "$TAG_REMOTE" 'v*' | sed 's|.*refs/tags/||'
}

# mirror_tag pushes tag $1 to MIRROR_REMOTE. A failure warns: the release
# stands on TAG_REMOTE, and just mirror-tags catches the mirror up.
mirror_tag() {
  [ -n "$MIRROR_REMOTE" ] || return 0
  if ! git remote get-url "$MIRROR_REMOTE" >/dev/null 2>&1; then
    echo "warning: no remote $MIRROR_REMOTE (Forgejo); $1 isn't mirrored. Add it, then: just mirror-tags" >&2
    return 0
  fi
  if git push -q "$MIRROR_REMOTE" "refs/tags/$1"; then
    echo "mirrored $1 to $MIRROR_REMOTE"
  else
    echo "warning: pushing $1 to $MIRROR_REMOTE failed; the release stands on $TAG_REMOTE. Retry: just mirror-tags" >&2
  fi
}
