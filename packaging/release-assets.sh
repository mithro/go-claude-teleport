#!/bin/sh
# The GitHub Release's assets for one build (mithro/apt-repo-action
# docs/packaging.md, "GitHub Releases"): the .debs of every debs-<suite>-<arch>
# artifact as <suite>_<file>, the extra files given, and SHA256SUMS over them.
#
# Each file is named here the way GitHub will store it, so SHA256SUMS lists the
# names people download. GitHub "renames asset filenames that have special
# characters, non-alphanumeric characters, and leading or trailing periods"
# (REST API docs, "Upload a release asset"); in practice ~ becomes . (an asset
# uploaded as trixie_claude-teleport_0.24.post5~deb13_amd64.deb is stored
# as trixie_claude-teleport_0.24.post5.deb13_amd64.deb), while + . _ and -
# are kept. So every other character becomes a dot.
#
# Usage: release-assets.sh <artifacts-dir> <out-dir> [extra-file...]
set -eu
artifacts=$1
out=$2
shift 2
mkdir -p "$out"

# asset <file> <name>: copy <file> into <out-dir> under <name>, made safe.
asset() {
	name=$(printf '%s' "$2" | sed 's/[^A-Za-z0-9._+-]/./g')
	if [ -e "$out/$name" ]; then
		echo "release-assets.sh: two assets would be named $name" >&2
		exit 1
	fi
	cp "$1" "$out/$name"
}

for dir in "$artifacts"/debs-*; do
	name=${dir##*/debs-}
	suite=${name%-*} # debs-<suite>-<arch>; a suite may contain a dash
	for deb in "$dir"/*.deb; do
		asset "$deb" "${suite}_${deb##*/}"
	done
done
for f in "$@"; do
	asset "$f" "${f##*/}"
done
cd "$out"
sha256sum -- * > SHA256SUMS
