#!/usr/bin/env bash
# Agent-e2e lane launcher. Fresh-boots a standalone strazad +
# ollama, parses the one-time bootstrap admin password from the first-boot log,
# and hands it to the runner, which does everything else (see runner/driver.py).
#
#   ./run.sh                              # full fresh run
#   STRAZA_E2E_MODEL=qwen2.5:0.5b ./run.sh  # smaller model (tight RAM)
#   STRAZA_E2E_FRESH=0 ./run.sh           # reuse the running stack + password
#   STRAZA_E2E_INTERACTIVE=1 ./run.sh     # governed CHAT instead of episodes:
#                                         # you type, the model plans tools,
#                                         # Straza decides every call live
#                                         # (approvals via :8421/console)
set -euo pipefail
cd "$(dirname "$0")"

DC="docker compose"
FRESH="${STRAZA_E2E_FRESH:-1}"

if [ "$FRESH" = "1" ]; then
  # Fresh = an empty Straza store, so the first boot prints a new admin password. The
  # ollama model cache volume deliberately survives (~1 GB per re-pull).
  $DC down --remove-orphans 2>/dev/null || true
  docker volume rm -f straza-agent-e2e_straza-data >/dev/null 2>&1 || true
  rm -f .admin-password
fi
$DC up -d --build strazad ollama

# The bootstrap password is printed exactly once, on the first boot,
# and a container recreate (image rebuild) wipes the log buffer, so persist
# what we parse into a git-ignored state file for non-fresh reruns.
PWFILE=".admin-password"
PW="${STRAZA_ADMIN_PASSWORD:-}"
if [ -z "$PW" ]; then
  for _ in $(seq 1 60); do
    LOGS="$($DC logs strazad 2>&1 || true)"
    # slog text handler: password=xxx ; JSON handler: "password":"xxx"
    PW="$(printf '%s' "$LOGS" | grep -oE '"password":"[^"]+"' | head -1 | cut -d'"' -f4 || true)"
    [ -z "$PW" ] && PW="$(printf '%s' "$LOGS" | grep -oE 'password=[^ ]+' | head -1 | cut -d= -f2 || true)"
    if [ -n "$PW" ]; then
      (umask 077 && printf '%s' "$PW" > "$PWFILE")
      break
    fi
    # No print in the logs: an already-bootstrapped volume, so use the saved one.
    if [ -f "$PWFILE" ]; then
      PW="$(cat "$PWFILE")"
      break
    fi
    sleep 1
  done
fi
if [ -z "$PW" ]; then
  echo "run.sh: no bootstrap password (not in strazad logs, no $PWFILE)." >&2
  echo "  Run fresh (STRAZA_E2E_FRESH=1, the default) or pass STRAZA_ADMIN_PASSWORD." >&2
  exit 1
fi

STRAZA_ADMIN_PASSWORD="$PW" $DC run --build --rm runner "$@"
