# clone: clone a repository and check out exactly one commit. bash 3.2.

# clone URL SHA DIR - clone URL into DIR and check out SHA, a full commit
# id: a branch or a tag could move between approval and run, so they're
# refused. DIR must not exist, or be empty. HEAD is checked against SHA
# before it returns.
clone() {
  local url sha dir head
  if [ $# -ne 3 ]; then
    echo "clone: usage: mod clone URL SHA DIR (SHA: a full commit id)" >&2
    return 2
  fi
  url=$1
  sha=$2
  dir=$3
  case "$sha" in
    *[!0-9a-f]*|'')
      echo "clone: $sha isn't a full commit id (40 or 64 lower-case hex digits; a branch or tag can move)" >&2
      return 2 ;;
  esac
  if [ ${#sha} -ne 40 ] && [ ${#sha} -ne 64 ]; then
    echo "clone: $sha isn't a full commit id (40 or 64 hex digits, not an abbreviation)" >&2
    return 2
  fi
  if [ -e "$dir" ] && [ -n "$(ls -A "$dir" 2>/dev/null)" ]; then
    echo "clone: $dir is there and not empty" >&2
    return 1
  fi
  echo "clone: $url at $sha into $dir"
  git init -q "$dir" || return 1
  git -C "$dir" remote add origin "$url" || return 1
  if ! git -C "$dir" fetch -q --depth 1 origin "$sha" 2>/dev/null; then
    echo "clone: the remote won't send $sha by itself; fetching its branches and tags"
    git -C "$dir" fetch -q origin '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*' || return 1
  fi
  if ! git -C "$dir" -c advice.detachedHead=false checkout -q --detach "$sha^{commit}" 2>/dev/null; then
    echo "clone: $sha isn't a commit in $url" >&2
    return 1
  fi
  head=$(git -C "$dir" rev-parse HEAD) || return 1
  if [ "$head" != "$sha" ]; then
    echo "clone: HEAD is $head, not $sha" >&2
    return 1
  fi
  echo "clone: HEAD is $head"
}
