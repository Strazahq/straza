#!/usr/bin/env sh
# verify-release: prove a release's bytes came from this source tree, with no
# key and no CI, so it survives any signing outage.
# Usage: `make verify-release` (it cds to the repo root); override the baked
# version strings with `VERSION=1.2.3 COMMIT=abc1234 make verify-release`. Fill
# dist/ from a published release (archives plus checksums.txt) or goreleaser.
# Checks (a) dist/checksums.txt against the artifacts on disk, the file cosign
# signs and so the hinge of the whole chain, (b) every release binary in dist/,
# rebuilt from current source and sha256-compared, and (c) on a Mac, the straza
# in each macOS package, compared slice by slice once its code signature is
# removed. Exits 1 on a mismatch or when dist/ holds no release binary, 0 only
# when all reproduce bit-for-bit, that signature aside.

# Rebuilds replay each binary's embedded build info (GOOS, GOARCH, GOAMD64 or
# GOARM64, CGO_ENABLED, toolchain) rather than this box's defaults, and
# -trimpath strips ldflags, so version/commit come from dist/metadata.json. A
# mismatch usually means a different Go toolchain patch level or distributor (a
# distro-patched build bakes in a different DefaultGODEBUG), or a dirty tree.
set -eu

cd "$(git rev-parse --show-toplevel)"

DIST=${DIST:-dist}
MODULE=github.com/strazahq/straza
VERSION_PKG=$MODULE/internal/version

fail=0
note() { printf '%s\n' "$*"; }
bad() { printf 'FAIL: %s\n' "$*" >&2; fail=1; }

if [ ! -d "$DIST" ]; then
  note "verify-release: no $DIST/ directory."
  note "Populate it from a published release (archives + checksums.txt) or"
  note "build one locally; see the 'GETTING A dist/' block in this script."
  exit 1
fi

# sum is the SHA-256 tool: sha256sum, or shasum on a Mac without sha256sum,
# which older macOS versions lack.
if command -v sha256sum >/dev/null 2>&1; then
  sum=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  sum="shasum -a 256"
else
  note "verify-release: neither sha256sum nor shasum is on PATH, so the release files cannot be hashed."
  note "Install GNU coreutils, which has sha256sum, or Perl, which has shasum, and run again."
  exit 1
fi

# --- (a) the signed checksum file vs the artifacts -------------------------

if [ -f "$DIST/checksums.txt" ]; then
  note "== checksums.txt vs artifacts on disk =="
  if (cd "$DIST" && $sum -c checksums.txt --ignore-missing); then
    note "checksums.txt: OK"
  else
    bad "checksums.txt does not match the artifacts in $DIST/"
  fi
  if [ -f "$DIST/checksums.txt.sig" ] && [ -f "$DIST/checksums.txt.pem" ]; then
    note "(signature present; verify the identity with:"
    note "   cosign verify-blob --signature $DIST/checksums.txt.sig \\"
    note "     --certificate $DIST/checksums.txt.pem \\"
    note "     --certificate-identity-regexp '^https://github\\.com/Strazahq/straza/\\.github/workflows/release\\.yml@refs/tags/v[0-9]+\\.[0-9]+\\.[0-9]+\$' \\"
    note "     --certificate-oidc-issuer https://token.actions.githubusercontent.com \\"
    note "     $DIST/checksums.txt )"
  else
    note "(no cosign signature in $DIST/; reproduction below is then the ONLY evidence)"
  fi
  note ""
else
  note "note: no $DIST/checksums.txt; skipping the checksum-file check."
  note ""
fi

# --- version/commit the release baked in -----------------------------------

meta="$DIST/metadata.json"
if [ -z "${VERSION:-}" ] && [ -f "$meta" ]; then
  VERSION=$(tr ',' '\n' < "$meta" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}
LDFLAGS=${LDFLAGS:-"-s -w -X $VERSION_PKG.Version=$VERSION -X $VERSION_PKG.Commit=$COMMIT"}

note "== rebuilding release binaries =="
note "version: $VERSION   commit: $COMMIT"
note "ldflags: $LDFLAGS"
note ""

# --- (b) rebuild every shipped binary and compare --------------------------

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

local_go=$(go env GOVERSION)
found=0
checked=0
pkgs=0

setting() { printf '%s\n' "$info" | awk -v k="$1" '$1=="build" && index($2,k"=")==1 {sub(/^[^=]*=/,"",$2); print $2; exit}'; }

# sha256 FILE prints the SHA-256 of FILE for the reader. cmp decides a match,
# so a missing hash tool cannot fake one.
sha256() { $sum "$1" | awk '{print $1}'; }

# replay BIN OUT NAME rebuilds the Go binary BIN from this tree into OUT and
# calls it NAME in FAIL lines. It returns 0 when OUT was built, 1 when BIN is
# not one of ours, and 2 when it is ours but was built with another toolchain
# or does not rebuild. It leaves BIN's GOOS and GOARCH in goos and goarch.
replay() {
  info=$(go version -m "$1" 2>/dev/null) || {
    bad "$3: not a Go binary (no embedded build info)"
    return 1
  }
  case $info in
  *"$MODULE"*) ;;
  *)
    bad "$3: build info does not name $MODULE; not one of ours"
    return 1
    ;;
  esac

  # First line is "<path>: go1.2.3 [(<distributor> ...)]"; keep the whole
  # toolchain string, distributor suffix included: a Red Hat/Debian-patched
  # go1.2.3 does not produce the same bytes as an upstream go1.2.3.
  built_go=$(printf '%s\n' "$info" | sed -n '1s/^.*: //p')
  if [ "$built_go" != "$local_go" ]; then
    bad "$3: built with $built_go, this machine has $local_go."
    note "     Reproducibility needs the SAME toolchain (distributor included);"
    note "     install $built_go and re-run rather than trusting a mismatch."
    return 2
  fi

  main_pkg=$(printf '%s\n' "$info" | awk '$1=="path"{print $2; exit}')
  goos=$(setting GOOS)
  goarch=$(setting GOARCH)
  goamd64=$(setting GOAMD64)
  goarm64=$(setting GOARM64)
  goarm=$(setting GOARM)
  cgo=$(setting CGO_ENABLED)

  # Replay the recorded environment exactly; anything unset stays unset so the
  # toolchain default applies (which is what produced the original too).
  if ! env CGO_ENABLED="${cgo:-0}" \
    GOOS="$goos" GOARCH="$goarch" \
    ${goamd64:+GOAMD64="$goamd64"} \
    ${goarm64:+GOARM64="$goarm64"} \
    ${goarm:+GOARM="$goarm"} \
    go build -trimpath -ldflags "$LDFLAGS" -o "$2" "$main_pkg" 2>"$work/err"; then
    bad "$3: rebuild failed"
    sed 's/^/     /' "$work/err" >&2
    return 2
  fi
}

# goreleaser lays binaries out as dist/<build-id>_<goos>_<goarch>[_variant]/<binary>.
for bin in $(find "$DIST" -type f \
  \( -name strazad -o -name strazad.exe \
  -o -name strazactl -o -name strazactl.exe \
  -o -name straza -o -name straza.exe \) | sort); do

  out="$work/$(printf '%s' "$bin" | tr '/' '_')"
  rc=0
  replay "$bin" "$out" "$bin" || rc=$?
  [ "$rc" -eq 1 ] || found=$((found + 1))
  [ "$rc" -eq 0 ] || continue

  checked=$((checked + 1))
  if cmp -s "$bin" "$out"; then
    note "MATCH     $goos/$goarch  $bin"
  else
    bad "$bin: REBUILD DIFFERS"
    note "     shipped:  $(sha256 "$bin")"
    note "     rebuilt:  $(sha256 "$out")"
    note "     Check, in order: toolchain distributor, VERSION/COMMIT ldflags"
    note "     (VERSION=... COMMIT=... make verify-release), and whether the"
    note "     working tree is exactly the tagged commit (git status)."
  fi
done

# --- (c) the straza in each macOS package ----------------------------------

# A package's straza is universal and signed, so no rebuild has its bytes. On
# a Mac, lipo splits it, each slice is replayed as above, and tools/machocmp
# proves the binary is those rebuilds plus a code signature. Elsewhere
# pkgutil cannot expand the package, so only (a) covers it.
for pkg in "$DIST"/*_darwin_universal.pkg; do
  [ -f "$pkg" ] || continue
  name=${pkg##*/}
  if [ "$(uname -s)" != Darwin ]; then
    if (cd "$DIST" && awk -v f="$name" '$2 == f' checksums.txt | $sum -c --status) 2>/dev/null; then
      note "NOTE      $pkg matches checksums.txt. Its straza is compared with this tree only on a Mac, where pkgutil can expand the package."
    else
      note "NOTE      $pkg has no matching line in checksums.txt, so nothing here vouches for it. Its straza is compared with this tree only on a Mac, where pkgutil can expand the package."
    fi
    continue
  fi
  exp="$work/$name"
  if ! pkgutil --expand-full "$pkg" "$exp" >"$work/err" 2>&1; then
    bad "$pkg: pkgutil cannot expand it, so its straza was not compared. Download the package again; pkgutil said:"
    sed 's/^/     /' "$work/err" >&2
    continue
  fi
  bin=$(find "$exp" -type f -path '*/Payload/opt/straza/bin/straza')
  if [ "$(printf '%s\n' "$bin" | grep -c /)" -ne 1 ]; then
    bad "$pkg: its payload does not hold exactly one opt/straza/bin/straza, so it is not a Straza release package. Download it again from the release."
    continue
  fi
  found=$((found + 1))
  rc=0
  for arch in arm64 x86_64; do
    if lipo "$bin" -thin "$arch" -output "$exp.$arch" 2>"$work/err"; then
      replay "$exp.$arch" "$exp.$arch.go" "$pkg ($arch slice)" || rc=$?
    else
      bad "$pkg: lipo cannot take the $arch slice out of its straza, so it is not the universal binary a release ships. Download the package again; lipo said:"
      sed 's/^/     /' "$work/err" >&2
      rc=2
    fi
  done
  [ "$rc" -eq 0 ] || continue
  checked=$((checked + 1))
  if go run ./tools/machocmp "$bin" "$exp.arm64.go" "$exp.x86_64.go" >"$work/cmp" 2>&1; then
    pkgs=$((pkgs + 1))
    note "MATCH     darwin/universal  $pkg"
  else
    bad "$pkg: its straza is not this tree's build plus a code signature."
    note "     Check, in order: toolchain distributor, VERSION/COMMIT ldflags, and"
    note "     whether the working tree is exactly the tagged commit (git status)."
  fi
  sed 's/^/     /' "$work/cmp"
  note "     pkgutil --check-signature:"
  pkgutil --check-signature "$pkg" 2>&1 | sed 's/^/     /'
done

note ""
if [ "$found" -eq 0 ]; then
  note "verify-release: found no release binaries under $DIST/; nothing reproduced."
  note "checksums alone prove only internal consistency, not origin."
  exit 1
fi
if [ "$fail" -ne 0 ]; then
  note "verify-release: FAILED ($found binaries found, $checked rebuilt and compared)."
  exit 1
fi
note "verify-release: OK. $checked binaries reproduce bit-for-bit from this tree."
if [ "$pkgs" -gt 0 ]; then
  note "That count includes the straza in each macOS package ($pkgs), which matches once its code signature is removed."
fi

# Key-based cosign fallback, for when the keyless path is unavailable (keyless
# stays the default in .goreleaser.yaml). Never automated: it mints a long-lived
# private key whose compromise forges releases, so a person runs these by hand
# on a trusted machine, with the password chosen by hand too.
#   1. cosign generate-key-pair -> straza-release.key (back it up offline, never
#      commit it, never place it on a build VM) and .pub (publish it to pin).
#   2. cosign sign-blob --key straza-release.key \
#        --output-signature dist/checksums.txt.sig dist/checksums.txt
#      Add --tlog-upload=false only when Rekor is unreachable, note it in the
#      release notes, and tell verifiers to pass --insecure-ignore-tlog: a
#      signature with no transparency-log entry cannot be audited afterwards.
#   3. Verifiers run: cosign verify-blob --key straza-release.pub --signature
#      checksums.txt.sig checksums.txt, then sha256sum -c checksums.txt
#      --ignore-missing. Publish that beside the release. Images use the same
#      pair; rotation publishes a new key and names the cutover version.
