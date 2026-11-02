#!/bin/sh
# container-build.sh runs inside the pinned alpine container that build-msi.sh
# starts. It installs the pinned msitools, builds the MSI with wixl, runs the
# msibuild post-processing, asserts the tables and copies the result to /out.
# It is not run by hand. /in is the staged sources, read-only; /out receives
# the MSI.
#
#   container-build.sh <msi-product-version> <output-file-name>
set -eu
VER=$1
NAME=$2

# Pin the package too, so a later alpine point release that bumps msitools
# fails the build loudly instead of changing the output silently.
apk add --no-cache msitools=0.106-r0 >/tmp/apk.log 2>&1 || {
	cat /tmp/apk.log
	echo "container-build.sh: apk add msitools=0.106-r0 failed. The pinned version is gone from the pinned image's repository." >&2
	exit 1
}
ver=$(wixl --version)
[ "$ver" = "0.106" ] || {
	echo "container-build.sh: wixl is $ver, not 0.106." >&2
	exit 1
}

M=/tmp/build.msi
wixl -a x64 -D ProductVersion="$VER" -o "$M" /in/straza.wxs 2>/tmp/wixl.log || {
	grep -vi 'platform' /tmp/wixl.log >&2
	echo "container-build.sh: wixl failed." >&2
	exit 1
}
# The same staged straza.exe wixl packed goes into the Binary table, so the
# two copies are one set of bytes; assert.sh checks their sha256 match.
sh /in/postprocess.sh "$M" /in/straza.exe
sh /in/assert.sh "$M" "$VER"
cp "$M" "/out/$NAME"
# The container runs as root for apk, so hand the MSI back to the caller.
chown "$HOST_UID:$HOST_GID" "/out/$NAME"
echo "container-build.sh: built /out/$NAME"
