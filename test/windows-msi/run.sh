#!/usr/bin/env bash
# run.sh builds the Straza Windows MSI the way a release does and checks it,
# without Windows. It assembles the public release tree, makes it a fresh
# clean git repository with one tag, and runs goreleaser there, so the straza
# build's post hook builds the MSI from exactly the files the release ships.
# A file the hook calls but tools/release-manifest.txt does not ship is absent
# in that tree, so the hook fails here instead of at tag time. deploy/windows/
# assert.sh then checks the MSI's tables in the pinned msitools container.
#
#   test/windows-msi/run.sh [--keep] [--include-untracked] [TAG]
#
# TAG is the MSI's version, v1.2.3 by default. --keep leaves the assembled
# tree. --include-untracked also assembles uncommitted files; the default
# ships only git-tracked files, as a release does. It needs docker and
# goreleaser.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)
disk_limit=${MSI_RIG_DISK_LIMIT:-88}

keep=0
untracked=0
tag=v1.2.3
for arg in "$@"; do
	case "$arg" in
	--keep) keep=1 ;;
	--include-untracked) untracked=1 ;;
	v[0-9]*) tag=$arg ;;
	*)
		echo "run.sh: unknown argument $arg" >&2
		exit 2
		;;
	esac
done
ver=${tag#v}

npass=0
nfail=0
result() { # ok id text
	if [ "$1" = 1 ]; then
		npass=$((npass + 1))
		printf 'PASS %-22s %s\n' "$2" "$3"
	else
		nfail=$((nfail + 1))
		printf 'FAIL %-22s %s\n' "$2" "$3"
	fi
}

disk_pct() { df -P "$repo" | awk 'NR==2 { gsub("%","",$5); print $5 }'; }
if [ "$(disk_pct)" -ge "$disk_limit" ]; then
	echo "run.sh: the disk is $(disk_pct)% full, at or over the ${disk_limit}% limit, so the run stops before it builds. Free space, then run it again." >&2
	exit 1
fi

tree="$repo/build/release-tree-msi"
rm -rf "$tree"
echo ".. assembling the public tree into $tree"
assemble_args="-src $repo -out $tree"
# -git=false copies the allowlisted paths even when uncommitted, for a check
# before the unit lands. It still ships only what the manifest lists.
[ "$untracked" = 1 ] && assemble_args="$assemble_args -git=false"
# The MSI hook needs none of gitleaks, which the push gate runs anyway, so a
# fast local run can skip its slow scan of the assembled tree.
[ "${MSI_RIG_SKIP_GITLEAKS:-0}" = 1 ] && assemble_args="$assemble_args -gitleaks=false"
# shellcheck disable=SC2086
if ! go run ./tools/releaseassemble $assemble_args >/dev/null; then
	echo "run.sh: releaseassemble failed; commit the unit's files, or pass --include-untracked for a pre-commit check" >&2
	exit 1
fi

result "$(test -f "$tree/deploy/windows/build-msi.sh" && echo 1 || echo 0)" manifest-ships-build "the manifest ships deploy/windows/build-msi.sh"
result "$(test -f "$tree/deploy/windows/straza.wxs" && echo 1 || echo 0)" manifest-ships-wxs "the manifest ships deploy/windows/straza.wxs"
result "$(test -f "$tree/cmd/straza/installer_windows.go" && echo 1 || echo 0)" manifest-ships-installer "the manifest ships the installer commands the custom actions call"

# A fresh, clean, tagged git repository, so goreleaser builds a release
# version (vX.Y.Z gives X.Y.Z) and never refuses a dirty tree. Only what the
# manifest copied is here, so the MSI hook sees exactly the shipped files.
(
	cd "$tree" || exit 1
	git init -q
	git config user.email rig@straza.local
	git config user.name rig
	git config commit.gpgSign false
	git config tag.gpgSign false
	# goreleaser reads a remote URL even for a local build; nothing is fetched.
	git remote add origin https://github.com/strazahq/straza.git
	git add -A
	git commit -qm "release tree" >/dev/null
	git tag "$tag"
) || {
	echo "run.sh: could not make the assembled tree a git repository" >&2
	exit 1
}

echo ".. building straza for windows amd64 and the MSI with goreleaser in the assembled tree"
build_log=$(mktemp)
(
	cd "$tree" || exit 1
	# The before hook runs `go run ./tools/notices`, a host tool that a
	# windows GOOS would cross-compile and fail to run, so generate the
	# notices the MSI carries here and skip the hook. --single-target with
	# the windows environment builds only the one target the MSI needs, so
	# the run is fast and needs no Windows. The straza build's post hook then
	# builds and asserts the MSI from exactly these shipped files.
	go run ./tools/notices -release -version "$ver" -o build/notices/THIRD_PARTY_NOTICES.txt || exit 1
	GORELEASER_CURRENT_TAG="$tag" GOOS=windows GOARCH=amd64 \
		goreleaser build --single-target --skip=before --id straza --clean
) >"$build_log" 2>&1
grc=$?
if [ "$grc" -ne 0 ]; then
	result 0 goreleaser-build "goreleaser build failed; the tail of its log follows"
	tail -n 25 "$build_log"
else
	result 1 goreleaser-build "goreleaser built straza for every target and ran the MSI hook"
fi

msi="$tree/dist/straza_${ver}_windows_amd64.msi"
if [ -f "$msi" ]; then
	result 1 msi-present "the hook wrote $(basename "$msi"), the archive naming"
	echo ".. asserting the MSI tables"
	if docker run --rm -v "$tree/dist":/d:ro -v "$repo/deploy/windows":/w:ro \
		alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b \
		sh -c "apk add --no-cache msitools=0.106-r0 >/dev/null 2>&1 && sh /w/assert.sh /d/$(basename "$msi") $ver"; then
		result 1 msi-tables "every sequenced action, service table, custom action and upgrade row matches"
	else
		result 0 msi-tables "the MSI tables drifted from the layout deploy/windows/assert.sh expects"
	fi
else
	result 0 msi-present "the hook did not write $(basename "$msi"); only windows amd64 builds one"
fi
rm -f "$build_log"

if [ "$keep" = 1 ]; then
	echo ".. kept the assembled tree at $tree"
else
	rm -rf "$tree"
fi

echo ""
echo "windows-msi rig: $npass passed, $nfail failed"
[ "$nfail" -eq 0 ]
