#!/bin/sh
# check-release-binary.sh compares a darwin straza that goreleaser just built
# with the unsigned binary the release workflow's macOS job built and then
# signed into the pkg. The goreleaser straza build calls it as a post hook
# once per target, and it does nothing for a target that is not darwin.
#
#   check-release-binary.sh <os> <arch> <path>
#
# STRAZA_MACOS_PKG_DIR names the folder that holds the macOS job's
# straza-darwin-arm64 and straza-darwin-amd64. The release workflow sets it
# for a vX.Y.Z tag, so a darwin binary that differs stops the build before
# any draft release exists. When it is unset, as in a local build or the MSI
# rig, the script says so in one line and the build goes on.
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: check-release-binary.sh <os> <arch> <path>" >&2
	exit 2
fi
os=$1
arch=$2
path=$3

[ "$os" = darwin ] || exit 0

dir=${STRAZA_MACOS_PKG_DIR:-}
if [ -z "$dir" ]; then
	echo "check-release-binary.sh: the darwin/$arch straza was not compared with the macOS package's binary, because the release workflow did not set STRAZA_MACOS_PKG_DIR."
	exit 0
fi
want=$dir/straza-darwin-$arch
if [ ! -f "$want" ]; then
	echo "check-release-binary.sh: $want does not exist, so the darwin/$arch straza at $path cannot be compared with the binary in the macOS package, and the build stops before any draft release exists. Check that the macOS job uploaded straza-darwin-$arch and that the goreleaser job downloaded it into $dir, then run the release again." >&2
	exit 1
fi
if ! cmp -s "$path" "$want"; then
	echo "check-release-binary.sh: the darwin/$arch straza at $path differs from $want, the binary the macOS job signed into the package, so the build stops before any draft release exists. Check that both jobs stamped the same version, from the pushed tag, and built the same commit from a checkout with fetch-depth 0, with the Go toolchain go.mod names and the same flags, then run the release again." >&2
	exit 1
fi
echo "check-release-binary.sh: the darwin/$arch straza at $path is byte for byte $want, the binary the macOS job signed into the package."
