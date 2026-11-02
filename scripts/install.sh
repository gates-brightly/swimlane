#!/bin/sh
# Install swim from its GitHub release: no Go toolchain, no build. For a
# container's setup, or anyone's:
#
#   curl -fsSL https://github.com/gates-brightly/swimlane/releases/latest/download/install.sh | sh
#
# Environment:
#   SWIM_VERSION      a release tag (v0.20261102.1); default: the latest
#   SWIM_INSTALL_DIR  where swim goes; default: /usr/local/bin if writable,
#                     else ~/.local/bin
#   SWIM_DOWNLOAD_BASE  where the releases are, laid out as GitHub's
#                     (<base>/download/<tag>/<asset>); default GitHub's
#
# It downloads swim_<os>_<arch>.tar.gz for this machine (linux or darwin,
# amd64 or arm64), checks it against the release's checksums.txt, and
# installs the swim binary. POSIX sh; curl or wget.
set -eu

repo=gates-brightly/swimlane
base=${SWIM_DOWNLOAD_BASE:-https://github.com/$repo/releases}

say() { echo "swim install: $*" >&2; }
die() { say "$*"; exit 1; }

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "no swim build for $(uname -s) (Linux and macOS only)" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "no swim build for $(uname -m) (amd64 and arm64 only)" ;;
esac

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
else
	die "needs curl or wget"
fi

# The release: SWIM_VERSION, else the latest. The latest is resolved to its
# tag first (curl), so the archive and checksums.txt come from one release
# even if another is published meanwhile.
tag=${SWIM_VERSION:-}
if [ -z "$tag" ] && command -v curl >/dev/null 2>&1; then
	url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$base/latest") || die "couldn't reach $base/latest"
	tag=${url##*/}
	case $tag in v0.*) ;; *) die "couldn't tell the latest release from $url" ;; esac
fi
if [ -n "$tag" ]; then
	dl=$base/download/$tag
else
	dl=$base/latest/download
fi

asset=swim_${os}_${arch}.tar.gz
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch "$dl/$asset" "$tmp/$asset" || die "couldn't download $dl/$asset"
fetch "$dl/checksums.txt" "$tmp/checksums.txt" || die "couldn't download $dl/checksums.txt"

want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "$asset isn't in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
	got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
else
	die "needs sha256sum or shasum to check the download"
fi
[ "$got" = "$want" ] || die "$asset's checksum is $got, but checksums.txt says $want: not installed"

tar -xzf "$tmp/$asset" -C "$tmp" swim

dir=${SWIM_INSTALL_DIR:-}
if [ -z "$dir" ]; then
	if [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
	else
		dir=$HOME/.local/bin
	fi
fi
mkdir -p "$dir"
# Into place in one step: a swim running from dir keeps its old binary.
cp "$tmp/swim" "$dir/.swim.new"
chmod 755 "$dir/.swim.new"
mv -f "$dir/.swim.new" "$dir/swim"

say "installed $("$dir/swim" --version 2>/dev/null | head -1) at $dir/swim"
case :$PATH: in
*:"$dir":*) ;;
*) say "$dir isn't on PATH: add it (export PATH=\"$dir:\$PATH\")" ;;
esac
