#!/bin/sh
# Checks packaging/release-assets.sh on made-up artifacts shaped like a real
# build's, since the release job itself only runs on the default branch.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT
a=$work/artifacts
mkdir -p "$a/debs-trixie-amd64" "$a/debs-raspbian-trixie-armhf" "$a/debs-sid-amd64" "$a/bin"
echo trixie > "$a/debs-trixie-amd64/pkg_1.0.post5~deb13_amd64.deb"
echo raspbian > "$a/debs-raspbian-trixie-armhf/pkg_1.0.post5~deb13_armhf.deb"
echo sid > "$a/debs-sid-amd64/pkg_1.0.post5_amd64.deb"
echo bin > "$a/bin/claude-teleport-linux-amd64"

sh "$here/release-assets.sh" "$a" "$work/release" "$a/bin/claude-teleport-linux-amd64"

cd "$work/release"
ls -1
want="SHA256SUMS
claude-teleport-linux-amd64
raspbian-trixie_pkg_1.0.post5.deb13_armhf.deb
sid_pkg_1.0.post5_amd64.deb
trixie_pkg_1.0.post5.deb13_amd64.deb"
got=$(ls -1 | LC_ALL=C sort)
if [ "$got" != "$want" ]; then
	echo "release-assets-test: wrong asset names" >&2
	exit 1
fi
# What someone who downloads the release runs: every name in SHA256SUMS is
# an asset, and its sum matches.
sha256sum -c SHA256SUMS
if grep -q '~' SHA256SUMS; then
	echo "release-assets-test: SHA256SUMS names a file GitHub would rename" >&2
	exit 1
fi
echo "release-assets-test: ok"
