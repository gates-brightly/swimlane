#!/usr/bin/env bash
# Print a release's notes: its section of CHANGELOG.md, the
# "## 0.<date>.<index> (v0.<date>.<index>, <date>)" heading's body, for
# GoReleaser's --release-notes (.github/workflows/release.yml).
#
#   scripts/release-notes.sh v0.20261102.1
#
# Fails if CHANGELOG.md has no section for the tag (scripts/release.sh
# makes sure it does before tagging). Bash 3.2 compatible (macOS).
set -eu

tag=${1:-}
case "$tag" in v0.*) ;; *) echo "usage: scripts/release-notes.sh v0.<YYYYMMDD>.<index>" >&2; exit 2 ;; esac
cd "$(dirname "$0")/.."
notes=$(awk -v want="${tag#v}" '
	/^## / { if (in_section) exit; if ($2 == want) { in_section = 1; next } }
	in_section { print }
' CHANGELOG.md)
[ -n "$(printf '%s' "$notes" | tr -d '[:space:]')" ] || { echo "release-notes: CHANGELOG.md has no ## ${tag#v} section" >&2; exit 1; }
printf '%s\n' "$notes"
