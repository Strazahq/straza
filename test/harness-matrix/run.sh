#!/usr/bin/env bash
# harness-matrix: live-fire REAL harness binaries against the hook wiring the
# installer actually writes. Key-free by construction where the vendor allows
# it: a fake API key opens a real session whose early hooks fire before the
# auth failure, verified per harness. This file is the shared
# plumbing and lane dispatcher; each harness owns one script under lanes/,
# and HARNESS_MATRIX_LANES runs a subset of them.
#
# ISOLATION, non-negotiable: harnesses only ever run with their HOME/config
# dirs pointed into .work/, and read managed /etc/<dir> files through an
# overlayfs mounted INSIDE an unprivileged user+mount namespace (unshare -rm),
# so the box's real /etc is never written. Where user namespaces are missing,
# the CI-only sudo fallback (HARNESS_MATRIX_ALLOW_SUDO_ETC=1) stages managed
# dirs into the DISPOSABLE runner's real /etc, refusing any dir that exists.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
MATRIX="$ROOT/test/harness-matrix"
WORK="$MATRIX/.work"
TOOLS="$MATRIX/.tools"
POLICY="$ROOT/spec/conformance/tier1/policy.yaml"

case "$ROOT" in *' '*)
  echo "harness-matrix: repo path contains spaces, so hook command strings would not survive harness tokenization; move the checkout" >&2
  exit 1
esac

# shellcheck source=versions.env
. "$MATRIX/versions.env"

# One run at a time: the shared .work is wiped at startup, so a concurrent
# invocation would clobber a live run and destroy its evidence. The lock
# lives beside .work, not in it.
exec 9>"$MATRIX/.work.lock"
if ! flock -n 9; then
  echo "harness-matrix: another run holds $MATRIX/.work.lock; one run at a time (the shared .work is wiped at startup)" >&2
  exit 1
fi

# The overlay workdir's kernel-created internals come back mode 000; reclaim
# before wiping or a second run dies on its own leftovers.
[ -d "$WORK" ] && chmod -R u+rwX "$WORK" 2>/dev/null
rm -rf "$WORK"
mkdir -p "$WORK"/{bin,fakehome,etc-upper,etc-work}
REPORT="$WORK/drift-report.md"

# ---------------------------------------------------------------- reporting
FAILED=0
note() { printf '%s\n' "$1" | tee -a "$REPORT"; }
gate() { # gate <name> <check args...>: runs check, records the row
  local name=$1; shift
  if "$WORK/bin/check" -gate "$name" "$@" 2>"$WORK/$name.err"; then
    note "- $name: PASS"
  else
    note "- $name: **FAIL**"
    sed 's/^/    /' "$WORK/$name.err" | tee -a "$REPORT"
    FAILED=1
  fi
}

# ------------------------------------------------------------------- build
echo "harness-matrix: building straza + lane tools"
( cd "$ROOT" &&
  go build -o "$WORK/bin/straza" ./cmd/straza &&
  go build -o "$WORK/bin/sentinel" ./test/harness-matrix/sentinel &&
  go build -o "$WORK/bin/check" ./test/harness-matrix/check &&
  go build -o "$WORK/bin/render" ./test/harness-matrix/render )

{
  echo "# harness-matrix drift report"
  echo
  echo "straza: \`$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo unknown)\`"
  echo
} > "$REPORT"

# ------------------------------------------------- /etc overlay (live gates)
# MODE=userns: unprivileged user+mount namespace with overlayfs, the default
# and the only mode that runs on a developer box. MODE=sudo: CI-only fallback
# for runners without user namespaces. MODE="": live gates skip, per lane.
MODE=""
if unshare -rm true 2>/dev/null &&
   unshare -rm sh -c "mount -t overlay overlay -o lowerdir=/etc,upperdir=$WORK/etc-upper,workdir=$WORK/etc-work /etc && test -d /etc" 2>/dev/null; then
  MODE=userns
elif [ "${HARNESS_MATRIX_ALLOW_SUDO_ETC:-}" = "1" ] && sudo -n true 2>/dev/null; then
  MODE=sudo
  STAGED_ETC=""
  cleanup_etc() { local b; for b in $STAGED_ETC; do sudo rm -rf "/etc/${b:?}"; done; }
  trap cleanup_etc EXIT
fi

# sudo_stage_etc: mirror $WORK/etc-upper/* into the real /etc (sudo MODE only).
# Refuses any dir that already exists and was not staged by this run.
sudo_stage_etc() {
  local d b
  for d in "$WORK"/etc-upper/*/; do
    [ -d "$d" ] || continue
    b=$(basename "$d")
    case " $STAGED_ETC " in *" $b "*) ;; *)
      if [ -e "/etc/$b" ]; then
        echo "harness-matrix: /etc/$b already exists; refusing the sudo fallback on a box with a real managed config" >&2
        exit 1
      fi
      STAGED_ETC="$STAGED_ETC $b"
    ;; esac
    sudo mkdir -p "/etc/$b"
    sudo cp -r "$d." "/etc/$b/"
  done
}

# overlay_exec <stdout> <stderr> <timeout-s> <VAR=VAL ... command args...>
# Runs the command with $WORK/etc-upper presented over /etc (per MODE). The
# lane stages its managed file(s) into $WORK/etc-upper/<dir>/ first.
overlay_exec() {
  local out=$1 err=$2 tmo=$3; shift 3
  local q
  if [ "$MODE" = userns ]; then
    q=$(printf '%q ' "$@")
    timeout "$tmo" unshare -rm sh -c \
      "mount -t overlay overlay -o lowerdir=/etc,upperdir=$WORK/etc-upper,workdir=$WORK/etc-work /etc && exec env $q" \
      </dev/null >"$out" 2>"$err" || true
  else
    sudo_stage_etc
    timeout "$tmo" env "$@" </dev/null >"$out" 2>"$err" || true
  fi
}

# --------------------------------------------------------------- lane loop
# Default = every lane script present; auto-discovery means adding a lane is
# one new file, no dispatcher edit.
LANES="${HARNESS_MATRIX_LANES:-}"
if [ -z "$LANES" ]; then
  LANES=$(cd "$MATRIX/lanes" && ls -- *.sh | sed 's/\.sh$//' | tr '\n' ' ')
fi

for lane_name in $LANES; do
  if [ ! -f "$MATRIX/lanes/$lane_name.sh" ]; then
    note "- lane $lane_name: **FAIL** (no such lane script)"
    FAILED=1
    continue
  fi
  note ""
  note "## lane: $lane_name"
  # shellcheck disable=SC1090
  . "$MATRIX/lanes/$lane_name.sh"
done

# ------------------------------------------------------------------ verdict
note ""
if [ "$FAILED" = 1 ]; then
  note "Ready-to-file bullet on failure: \`harness-matrix RED: see the gate rows above; pinned-lane red = straza bug, latest-lane red = vendor drift (file it as vendor drift, do not treat it as a straza regression).\`"
fi
echo "harness-matrix: done (failed=$FAILED, report: $REPORT)"
exit $FAILED
