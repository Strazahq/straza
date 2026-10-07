#!/usr/bin/env sh
# verify-release: prove a release's bytes came from this source tree, with no
# key and no CI, so it survives any signing outage.
# Usage: `make verify-release` (it cds to the repo root); override the baked
# version strings with `VERSION=1.2.3 COMMIT=abc1234 make verify-release`. Fill
# dist/ from a published release (archives plus checksums.txt) or goreleaser.
# Checks (a) dist/checksums.txt against the artifacts on disk, the file cosign
# signs and so the hinge of the whole chain, and (b) every release binary in
# dist/, rebuilt from current source and sha256-compared. Exits 1 on a mismatch
# or when dist/ holds no release binary, 0 only when all reproduce bit-for-bit.
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

# --- (a) the signed checksum file vs the artifacts -------------------------

if [ -f "$DIST/checksums.txt" ]; then
  note "== checksums.txt vs artifacts on disk =="
  if (cd "$DIST" && sha256sum -c checksums.txt --ignore-missing); then
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

# goreleaser lays binaries out as dist/<build-id>_<goos>_<goarch>[_variant]/<binary>.
for bin in $(find "$DIST" -type f \
  \( -name strazad -o -name strazad.exe \
  -o -name strazactl -o -name strazactl.exe \
  -o -name straza -o -name straza.exe \) | sort); do

  info=$(go version -m "$bin" 2>/dev/null) || {
    bad "$bin: not a Go binary (no embedded build info)"
    continue
  }
  case $info in
  *"$MODULE"*) ;;
  *)
    bad "$bin: build info does not name $MODULE; not one of ours"
    continue
    ;;
  esac

  found=$((found + 1))
  # First line is "<path>: go1.2.3 [(<distributor> ...)]"; keep the whole
  # toolchain string, distributor suffix included: a Red Hat/Debian-patched
  # go1.2.3 does not produce the same bytes as an upstream go1.2.3.
  built_go=$(printf '%s\n' "$info" | sed -n '1s/^.*: //p')
  if [ "$built_go" != "$local_go" ]; then
    bad "$bin: built with $built_go, this machine has $local_go."
    note "     Reproducibility needs the SAME toolchain (distributor included);"
    note "     install $built_go and re-run rather than trusting a mismatch."
    continue
  fi

  main_pkg=$(printf '%s\n' "$info" | awk '$1=="path"{print $2; exit}')
  setting() { printf '%s\n' "$info" | awk -v k="$1" '$1=="build" && index($2,k"=")==1 {sub(/^[^=]*=/,"",$2); print $2; exit}'; }
  goos=$(setting GOOS)
  goarch=$(setting GOARCH)
  goamd64=$(setting GOAMD64)
  goarm64=$(setting GOARM64)
  goarm=$(setting GOARM)
  cgo=$(setting CGO_ENABLED)

  out="$work/$(printf '%s' "$bin" | tr '/' '_')"
  # Replay the recorded environment exactly; anything unset stays unset so the
  # toolchain default applies (which is what produced the original too).
  if ! env CGO_ENABLED="${cgo:-0}" \
    GOOS="$goos" GOARCH="$goarch" \
    ${goamd64:+GOAMD64="$goamd64"} \
    ${goarm64:+GOARM64="$goarm64"} \
    ${goarm:+GOARM="$goarm"} \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" "$main_pkg" 2>"$work/err"; then
    bad "$bin: rebuild failed"
    sed 's/^/     /' "$work/err" >&2
    continue
  fi

  want=$(sha256sum "$bin" | awk '{print $1}')
  got=$(sha256sum "$out" | awk '{print $1}')
  checked=$((checked + 1))
  if [ "$want" = "$got" ]; then
    note "MATCH     $goos/$goarch  $bin"
  else
    bad "$bin: REBUILD DIFFERS"
    note "     shipped:  $want"
    note "     rebuilt:  $got"
    note "     Check, in order: toolchain distributor, VERSION/COMMIT ldflags"
    note "     (VERSION=... COMMIT=... make verify-release), and whether the"
    note "     working tree is exactly the tagged commit (git status)."
  fi
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
