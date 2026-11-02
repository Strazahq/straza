#!/bin/sh
# build-pkg.sh builds the Straza package for macOS, a product archive with
# one universal straza binary, with Apple's lipo, pkgbuild and productbuild.
#
#   build-pkg.sh <version> <dist-dir> [<darwin-arm64 straza> <darwin-amd64 straza>]
#
# Without the two binaries it builds them from this tree with the go.mod
# toolchain and the release flags. It writes
# <dist-dir>/straza_<version>_darwin_universal.pkg. Signing is optional:
# STRAZA_SIGN_APP names a Developer ID Application identity for the binary,
# STRAZA_SIGN_INSTALLER a Developer ID Installer identity for the package,
# and STRAZA_NOTARY_PROFILE a notarytool keychain profile that notarizes
# and staples it, which needs both identities.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
identifier=ai.straza.straza

if [ "$(uname -s)" != Darwin ]; then
	echo "build-pkg.sh builds the macOS package with Apple's lipo, pkgbuild and productbuild, which run only on macOS, so it built nothing on this $(uname -s) machine. Run it on a Mac with the Command Line Tools installed." >&2
	exit 1
fi
if [ "$#" -ne 2 ] && [ "$#" -ne 4 ]; then
	echo "usage: build-pkg.sh <version> <dist-dir> [<darwin-arm64 straza> <darwin-amd64 straza>]" >&2
	exit 2
fi
version=${1#v}
dist=$2

# The package version decides upgrade and downgrade. A tag vX.Y.Z gives
# X.Y.Z, and a build between tags, vX.Y.Z-N-gHASH from git describe, gives
# X.Y.Z.N, so it sorts above the tag it follows. A prerelease tag such as
# vX.Y.Z-rc.1, which only the release workflow's hand-started rehearsal
# packages, gives X.Y.Z, so the release pkg installs over it.
base=${version%%-*}
rest=${version#"$base"}
pkgver=
if printf '%s' "$base" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	if [ -z "$rest" ]; then
		pkgver=$base
	elif printf '%s' "$rest" | grep -qE '^-(alpha|beta|rc)\.[0-9]+$'; then
		pkgver=$base
	else
		n=$(printf '%s' "$rest" | sed -nE 's/^-([0-9]+)-g[0-9a-fA-F]+(-dirty)?$/\1/p')
		[ -z "$n" ] || pkgver=$base.$n
	fi
fi
if [ -z "$pkgver" ]; then
	echo "build-pkg.sh needs a version X.Y.Z, X.Y.Z-rc.N or X.Y.Z-N-gHASH from git describe, and $1 is none of them, so it built nothing. Run it again with a version such as 1.4.0." >&2
	exit 1
fi

app=${STRAZA_SIGN_APP:-}
installer=${STRAZA_SIGN_INSTALLER:-}
notary=${STRAZA_NOTARY_PROFILE:-}
# STRAZA_SIGN_KEYCHAIN names a keychain file that holds both identities.
# codesign and productbuild then take the identity from it alone, and the
# certificate checks read it. STRAZA_NOTARY_KEYCHAIN names the keychain file
# that holds the notarytool profile. Without them, the search list serves.
sign_keychain=${STRAZA_SIGN_KEYCHAIN:-}
notary_keychain=${STRAZA_NOTARY_KEYCHAIN:-}
if [ -n "$notary" ] && { [ -z "$app" ] || [ -z "$installer" ]; }; then
	echo "build-pkg.sh: STRAZA_NOTARY_PROFILE is set, but Apple notarizes only a package signed with a Developer ID Installer identity around a binary signed with a Developer ID Application identity, so it built nothing. Set STRAZA_SIGN_APP and STRAZA_SIGN_INSTALLER too, or unset STRAZA_NOTARY_PROFILE for an unnotarized test package." >&2
	exit 1
fi

for f in "$repo/LICENSE" "$repo/LICENSE-APACHE" "$repo/NOTICE"; do
	if [ ! -f "$f" ]; then
		echo "build-pkg.sh: $f is missing, and the package must carry the licenses, so it built nothing. Run it from a complete Straza source tree." >&2
		exit 1
	fi
done
notices=$repo/build/notices/THIRD_PARTY_NOTICES.txt
if [ ! -f "$notices" ]; then
	echo "build-pkg.sh: $notices is missing, and the package must carry the notices of every module the binary links, so it built nothing. Generate it in $repo with \`go run ./tools/notices -release -version $version -o build/notices/THIRD_PARTY_NOTICES.txt\`, then run build-pkg.sh again." >&2
	exit 1
fi
if ! xcrun --find lipo >/dev/null 2>&1; then
	echo "build-pkg.sh needs lipo, which comes with the Command Line Tools, so it built nothing. Install them with \`xcode-select --install\`, then run build-pkg.sh again." >&2
	exit 1
fi
if [ -n "$app" ] && ! command -v go >/dev/null 2>&1; then
	echo "build-pkg.sh proves with tools/machocmp, a Go program, that codesign changed nothing in the binary but its signature, and go is not on PATH, so it built nothing. Install Go, then run build-pkg.sh again." >&2
	exit 1
fi
plutil -lint "$here/ai.straza.service.plist"

work=$(mktemp -d "${TMPDIR:-/tmp}/straza-pkg.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM

# check_certificate finds the one certificate of an identity, by SHA-1 for 40
# hex digits and by exact common name otherwise, in STRAZA_SIGN_KEYCHAIN or
# the search list. It must come from Apple's Developer ID G2 authority,
# because macOS stops installing packages signed under the older one on
# 2027-02-01. OpenSSL prints names as "CN=x, OU=y" or "CN = x, OU = y" and
# LibreSSL as "/CN=x/OU=y", and every form is matched.
check_certificate() { # variable identity
	var=$1
	id=$2
	where="the keychain search list"
	[ -z "$sign_keychain" ] || where="the keychain $sign_keychain"
	certs=$work/certs-$var
	mkdir "$certs"
	security find-certificate -a -p ${sign_keychain:+"$sign_keychain"} >"$certs/all.pem" || true
	awk -v d="$certs" '/-----BEGIN CERTIFICATE-----/ { n++; f = sprintf("%s/%05d.pem", d, n) }
		f != "" { print > f }
		/-----END CERTIFICATE-----/ { close(f); f = "" }' "$certs/all.pem"
	byhash=$(printf '%s' "$id" | grep -E '^[0-9A-Fa-f]{40}$' | tr 'a-f' 'A-F')
	seen=
	found=
	for pem in "$certs"/[0-9]*.pem; do
		[ -f "$pem" ] || continue
		sha=$(openssl x509 -noout -fingerprint -sha1 -in "$pem" 2>/dev/null | sed 's/.*=//; s/://g' | tr 'a-f' 'A-F')
		[ -n "$sha" ] || continue
		if [ -n "$byhash" ]; then
			[ "$sha" = "$byhash" ] || continue
		else
			subject=$(openssl x509 -noout -subject -in "$pem" 2>/dev/null) || continue
			case "$subject," in
			*"CN = $id,"* | *"CN=$id,"*) ;;
			*) case "$subject/" in *"/CN=$id/"*) ;; *) continue ;; esac ;;
			esac
		fi
		case "$seen " in *" $sha "*) continue ;; esac
		seen="$seen $sha"
		found=$pem
	done
	count=$(printf '%s' "$seen" | wc -w | tr -d ' ')
	if [ "$count" = 0 ]; then
		echo "build-pkg.sh: no certificate in $where matches $id, the identity $var names, so it built nothing. Set $var to the identity's exact common name or its 40-hex SHA-1, as \`security find-identity -v -p basic\` lists it, then run build-pkg.sh again." >&2
		exit 1
	fi
	if [ "$count" != 1 ]; then
		echo "build-pkg.sh: $count certificates in $where match $id, the identity $var names, so it cannot tell which one signs and built nothing. Delete the old or unwanted certificate from $where, then run build-pkg.sh again." >&2
		exit 1
	fi
	issuer=$(openssl x509 -noout -issuer -in "$found" | sed -E 's/^issuer= ?//')
	if ! printf '%s' "$issuer" | grep -qE 'OU ?= ?G2([,/]|$)'; then
		echo "build-pkg.sh: the certificate of $id, the identity $var names, was issued by $issuer, not by Apple's Developer ID Certification Authority G2, and macOS stops installing packages signed under the older authority on 2027-02-01, so it built nothing. Make a new Developer ID certificate in your Apple Developer account with the G2 Sub-CA profile type, then run build-pkg.sh again with it." >&2
		exit 1
	fi
	end=$(openssl x509 -noout -enddate -in "$found" | sed 's/^notAfter=//')
	if ! openssl x509 -noout -checkend 0 -in "$found" >/dev/null; then
		echo "build-pkg.sh: the certificate of $id, the identity $var names, ended on $end, and codesign and productbuild sign nothing with an expired certificate, so it built nothing. Make a new Developer ID certificate in your Apple Developer account with the G2 Sub-CA profile type, export it with its private key as a .p12, and put that .p12 where this build takes its identities from, for a release the APPLE_DEVELOPER_ID_P12 secret of the macos-signing environment, then run build-pkg.sh again." >&2
		exit 1
	fi
	if ! openssl x509 -noout -checkend 2592000 -in "$found" >/dev/null; then
		prefix=
		[ "${GITHUB_ACTIONS:-}" != true ] || prefix=::warning::
		echo "${prefix}build-pkg.sh: the certificate of $id, the identity $var names, ends on $end, within 30 days, and nothing can be signed with it after that. Renew it now: make a new Developer ID certificate in your Apple Developer account with the G2 Sub-CA profile type, export it with its private key as a .p12, and put that .p12 where this build takes its identities from, for a release the APPLE_DEVELOPER_ID_P12 secret of the macos-signing environment." >&2
	fi
}
if [ -n "$app" ]; then
	check_certificate STRAZA_SIGN_APP "$app"
fi
if [ -n "$installer" ]; then
	check_certificate STRAZA_SIGN_INSTALLER "$installer"
fi

# Built from the tree, each binary gets the release's flags and the exact
# toolchain go.mod names, as the release archive's binary does.
if [ "$#" -eq 4 ]; then
	arm64=$3
	amd64=$4
	for b in "$arm64" "$amd64"; do
		if [ ! -f "$b" ]; then
			echo "build-pkg.sh: the binary $b does not exist, so there is nothing to package. Pass a darwin arm64 straza and a darwin amd64 straza, or neither to build them from this tree." >&2
			exit 1
		fi
	done
	# machocmp runs from the repository root, so it gets absolute paths.
	arm64=$(cd "$(dirname "$arm64")" && pwd)/$(basename "$arm64")
	amd64=$(cd "$(dirname "$amd64")" && pwd)/$(basename "$amd64")
else
	toolchain=$(sed -n 's/^toolchain //p' "$repo/go.mod")
	commit=$(git -C "$repo" rev-parse --short HEAD 2>/dev/null || echo none)
	ldflags="-s -w -X github.com/strazahq/straza/internal/version.Version=$version -X github.com/strazahq/straza/internal/version.Commit=$commit"
	for arch in arm64 amd64; do
		mkdir "$work/$arch"
		(cd "$repo" && env CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" ${toolchain:+GOTOOLCHAIN="$toolchain"} \
			go build -trimpath -ldflags "$ldflags" -o "$work/$arch/straza" ./cmd/straza)
	done
	arm64=$work/arm64/straza
	amd64=$work/amd64/straza
fi

# The payload, with every file root:wheel through pkgbuild's recommended
# ownership. What the service's account owns, postinstall makes, and the
# PATH links too, so the Installer never touches an existing /usr/local/bin.
# The newsyslog rule goes under private/etc, because /etc is a link to it.
root=$work/root
mkdir -p "$root/opt/straza/bin" "$root/opt/straza/share/doc" \
	"$root/Library/LaunchDaemons" "$root/private/etc/newsyslog.d"
bin=$root/opt/straza/bin/straza
xcrun lipo -create -output "$bin" "$arm64" "$amd64"
archs=$(xcrun lipo -archs "$bin")
case "$archs" in
"x86_64 arm64" | "arm64 x86_64") ;;
*)
	echo "build-pkg.sh made a straza binary for $archs, and the package needs one for exactly arm64 and x86_64, so it built nothing. Pass a darwin arm64 straza and a darwin amd64 straza." >&2
	exit 1
	;;
esac
if [ -n "$app" ]; then
	codesign -f --timestamp -o runtime -i "$identifier" ${sign_keychain:+--keychain "$sign_keychain"} -s "$app" "$bin"
	codesign --verify --strict --verbose=2 "$bin"
	# The signed binary must be the two inputs plus Apple's signature and
	# nothing else, which tools/machocmp proves slice by slice.
	if ! (cd "$repo" && go run ./tools/machocmp "$bin" "$arm64" "$amd64"); then
		echo "build-pkg.sh: tools/machocmp did not prove that the signed $bin is $arm64 and $amd64 plus a code signature, so it built nothing. Read machocmp's sentences above, then run build-pkg.sh again." >&2
		exit 1
	fi
fi
cp "$here/ai.straza.service.plist" "$root/Library/LaunchDaemons/"
cp "$here/ai.straza.service.conf" "$root/private/etc/newsyslog.d/"
cp "$repo/LICENSE" "$repo/LICENSE-APACHE" "$repo/NOTICE" "$notices" "$root/opt/straza/share/doc/"
find "$root" -type d -exec chmod 0755 {} +
find "$root" -type f -exec chmod 0644 {} +
chmod 0755 "$bin"

mkdir "$work/scripts" "$work/pkgs"
cp "$here/scripts/preinstall" "$here/scripts/postinstall" "$work/scripts/"
chmod 0755 "$work/scripts/preinstall" "$work/scripts/postinstall"
pkgbuild --root "$root" --scripts "$work/scripts" --identifier "$identifier" \
	--version "$pkgver" --install-location / --ownership recommended "$work/pkgs/straza.pkg"

name=straza_${version}_darwin_universal.pkg
mkdir -p "$dist"
out=$(cd "$dist" && pwd)/$name
if [ -n "$installer" ]; then
	productbuild --distribution "$here/distribution.xml" --package-path "$work/pkgs" \
		--sign "$installer" ${sign_keychain:+--keychain "$sign_keychain"} --timestamp "$out"
	pkgutil --check-signature "$out"
else
	productbuild --distribution "$here/distribution.xml" --package-path "$work/pkgs" "$out"
fi

if [ -n "$notary" ]; then
	result=$(xcrun notarytool submit "$out" --keychain-profile "$notary" ${notary_keychain:+--keychain "$notary_keychain"} --wait --output-format json) || true
	if ! printf '%s' "$result" | grep -Eq '"status" *: *"Accepted"'; then
		echo "build-pkg.sh: Apple did not accept $out for notarization ($result), so it is not stapled. Read why with \`xcrun notarytool log <id> --keychain-profile $notary${notary_keychain:+ --keychain $notary_keychain}\`, using the id above, then fix the package and run build-pkg.sh again." >&2
		exit 1
	fi
	xcrun stapler staple "$out"
	xcrun stapler validate "$out"
fi

if [ -z "$app$installer$notary" ]; then
	echo "build-pkg.sh: STRAZA_SIGN_APP, STRAZA_SIGN_INSTALLER and STRAZA_NOTARY_PROFILE are unset, so the package and its binary are unsigned and not notarized, which suits a test Mac and not a release."
fi
echo "build-pkg.sh: wrote $out (package version $pkgver)"
