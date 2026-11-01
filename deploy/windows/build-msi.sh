#!/bin/sh
# build-msi.sh builds the Straza Windows MSI from a straza.exe. The goreleaser
# straza build calls it as a post hook once per target; it builds the MSI for
# windows amd64 and does nothing for every other target. The test rig and a
# person call it the same way.
#
#   build-msi.sh <os> <arch> <straza.exe> <version> <dist-dir>
#
# It runs wixl and msibuild in the pinned alpine container, so it needs no
# Windows and no WiX on the host, only docker. It writes
# <dist-dir>/straza_<version>_windows_amd64.msi, the archive naming, and
# exits non-zero when wixl, the post-processing or a table assertion fails.
set -eu

# The msitools build image, pinned by digest. alpine 3.24 ships msitools
# 0.106, which container-build.sh pins again at the apk level.
IMAGE="alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b"
DISK_LIMIT=${MSI_DISK_LIMIT:-88}

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)

if [ "$#" -ne 5 ]; then
	echo "usage: build-msi.sh <os> <arch> <straza.exe> <version> <dist-dir>" >&2
	exit 2
fi
os=$1
arch=$2
exe=$3
version=$4
dist=$5

# The MSI is built for amd64 only. Every other target is skipped, so
# the one post hook on the straza build fires the MSI build exactly once.
if [ "$os" != "windows" ] || [ "$arch" != "amd64" ]; then
	echo "build-msi.sh: $os/$arch is not windows/amd64, so no MSI is built."
	exit 0
fi

# A goreleaser snapshot sets STRAZA_SNAPSHOT=true and gets no MSI, so a
# snapshot on a nearly full disk does not stop at the disk check below. A
# tag build and a person running this script build the MSI as before.
if [ "${STRAZA_SNAPSHOT:-}" = "true" ]; then
	echo "build-msi.sh: this is a goreleaser snapshot, so no MSI is built. For a Windows test MSI, run deploy/windows/build-msi.sh by hand."
	exit 0
fi

# Map the release version to an MSI ProductVersion. A tag vX.Y.Z gives X.Y.Z.
# A dev build vX.Y.Z-N-gHASH gives X.Y.Z.N, whose fourth field Windows ignores
# when it compares versions, so AllowSameVersionUpgrades lets it replace the
# tag build. A prerelease such as vX.Y.Z-rc1 gets no MSI.
v=${version#v}
base=${v%%-*}
rest=${v#"$base"}
if ! printf '%s' "$base" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "build-msi.sh: version $version has no X.Y.Z, so no MSI is built." >&2
	exit 0
fi
if [ -z "$rest" ]; then
	msiver=$base
else
	n=$(printf '%s' "$rest" | sed -nE 's/^-([0-9]+)-g[0-9a-fA-F]+$/\1/p')
	if [ -z "$n" ]; then
		echo "build-msi.sh: version $version is a prerelease, so no MSI is built."
		exit 0
	fi
	msiver=$base.$n
fi

if [ ! -f "$exe" ]; then
	echo "build-msi.sh: the binary $exe does not exist, so there is nothing to package." >&2
	exit 1
fi
notices="$repo/build/notices/THIRD_PARTY_NOTICES.txt"
for f in "$repo/LICENSE" "$repo/LICENSE-APACHE" "$repo/NOTICE" "$notices"; do
	if [ ! -f "$f" ]; then
		echo "build-msi.sh: $f is missing. Run the goreleaser before hook that generates the notices first." >&2
		exit 1
	fi
done

pct=$(df -P "$dist" 2>/dev/null | awk 'NR==2 { gsub("%","",$5); print $5 }' || echo 0)
if [ "${pct:-0}" -ge "$DISK_LIMIT" ]; then
	echo "build-msi.sh: the disk holding $dist is ${pct}% full, at or over the ${DISK_LIMIT}% limit, so the build stops. Free space, then run it again." >&2
	exit 1
fi

name="straza_${version}_windows_amd64.msi"
mkdir -p "$dist"
dist_abs=$(cd "$dist" && pwd)

# Stage every source the container reads into one directory it mounts
# read-only, so the container writes only to the output directory.
stage=$(mktemp -d "${TMPDIR:-/tmp}/straza-msi.XXXXXX")
trap 'rm -rf "$stage"' EXIT INT TERM
cp "$here/straza.wxs" "$here/postprocess.sh" "$here/assert.sh" "$here/container-build.sh" "$stage/"
cp "$exe" "$stage/straza.exe"
cp "$repo/LICENSE" "$repo/LICENSE-APACHE" "$repo/NOTICE" "$notices" "$stage/"

echo "build-msi.sh: building $name (ProductVersion $msiver) in $IMAGE"
docker run --rm \
	-e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
	-v "$stage":/in:ro \
	-v "$dist_abs":/out \
	"$IMAGE" sh /in/container-build.sh "$msiver" "$name"

echo "build-msi.sh: wrote $dist_abs/$name"
