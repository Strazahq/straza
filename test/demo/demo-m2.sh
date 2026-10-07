#!/usr/bin/env bash
# make demo-m2: reproduces the M2 milestone demo with the real binaries.
# a central policy blocks `rm -rf` inside a (simulated) Claude Code session,
# with a reason; the decision is audited; a role-bound knowledge pack appears
# in the session context (the M2 milestone contract).
#
# The full end-to-end flow is also asserted as a Go test
# (internal/agentguard/e2e_test.go, TestM2Demo); this script is the
# human-runnable version driving the built binaries against a live strazad.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$ROOT/bin"
DATA="$(mktemp -d)"
export STRAZA_HOME="$DATA/straza"
trap 'kill "${STRAZAD_PID:-0}" 2>/dev/null || true; rm -rf "$DATA"' EXIT

echo "== building binaries =="
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN/strazad" ./cmd/strazad \
  && CGO_ENABLED=0 go build -o "$BIN/strazactl" ./cmd/strazactl \
  && CGO_ENABLED=0 go build -o "$BIN/straza" ./cmd/straza )

echo "== starting strazad (standalone) =="
STRAZA_DATA_DIR="$DATA/strazad" "$BIN/strazad" serve --profile standalone --listen 127.0.0.1:8420 \
  >"$DATA/strazad.log" 2>&1 &
STRAZAD_PID=$!
until curl -sf http://127.0.0.1:8420/healthz >/dev/null 2>&1; do sleep 0.2; done
ADMIN_PW="$(grep -o '"password":"[^"]*"' "$DATA/strazad.log" | head -1 | cut -d'"' -f4)"
echo "bootstrap admin password: $ADMIN_PW"

cat <<'NOTE'

This script sketches the operator steps; the automated, assertion-backed
version is `go test ./internal/agentguard -run TestM2Demo`. Run that for the
authoritative demo. Manual steps from here:

  1. strazactl login --server http://127.0.0.1:8420   (device flow as admin)
  2. strazactl policy apply -f spec/policyset/examples/valid-finance.yaml
     strazactl policy activate finance-tools
  3. straza enroll --server http://127.0.0.1:8420
  4. straza install           (writes Claude Code hooks)
  5. In Claude Code: a `rm -rf` Bash call is blocked with the policy reason;
     the role-bound knowledge pack appears in the session context.
  6. strazactl audit verify       (the block is in the tamper-evident chain)

NOTE
echo "strazad healthy on :8420 (pid $STRAZAD_PID). See notes above."
