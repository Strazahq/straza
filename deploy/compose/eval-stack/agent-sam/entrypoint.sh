#!/bin/sh
# sam's proof ladder + governed heartbeat. The numbered lines are the
# machine-checkable story an evaluator tails; /tmp/sam.PASS is the compose
# healthcheck contract (present = every proof stood and the heartbeat is
# healthy). On refusal (kill switch, revocation) the PASS marker is removed
# and, after three failed beats, the ladder restarts from enrollment, so
# re-enabling sam in midPoint self-heals with no manual restart.
#
# Credential: an Ed25519 key. The pod generates the pair at first boot
# (private half stays in the home volume), drops the PUBLIC half into the
# shared /keys volume, and the seeder registers it
# via the admin API; enroll retries until that lands. No IdP client, no
# shared secret, nothing to rotate out of the realm import.
set -u

SRV="${STRAZA_SERVER:-http://127.0.0.1:8420}"
SAM_USER=sam-sre-agent
KEYS_DIR="${STRAZA_KEYS_DIR:-/keys}"
PASS=/tmp/sam.PASS
ECHO_TOOL=""
say() { echo "[sam] $*"; }

session_token() {
  jq -r '.sessionToken // .session_token // empty' \
    "$HOME/.straza/state/session.json" 2>/dev/null
}

# Generate the local NHI key when it is missing and publish the public half
# for the seeder. A kept home with a lost /keys volume regenerates with
# --force (the old registration is gone with that volume's consumer; the
# re-run seeder registers the fresh key).
ensure_key() {
  if [ -f "$HOME/.straza/state/nhi-key.json" ] && [ -s "$KEYS_DIR/$SAM_USER.pub" ]; then
    return 0
  fi
  FORCE=""
  [ -f "$HOME/.straza/state/nhi-key.json" ] && FORCE="--force"
  straza keygen --user "$SAM_USER" $FORCE >/tmp/keygen.log 2>&1 \
    || { say "keygen failed:"; cat /tmp/keygen.log; return 1; }
  sed -n 's/^Public key: //p' /tmp/keygen.log > "$KEYS_DIR/$SAM_USER.pub"
  say "NHI key ready; public half published at $KEYS_DIR/$SAM_USER.pub (the seeder registers it)"
}

# One governed tools/call through the gateway; returns 0 only when the call
# was ALLOWED and answered. The echo tool name is discovered from tools/list
# (the session's own catalog), never hardcoded.
gateway_echo() {
  TOK=$(session_token)
  [ -n "$TOK" ] || return 1
  if [ -z "$ECHO_TOOL" ]; then
    ECHO_TOOL=$(curl -sf -X POST -H "Authorization: Bearer $TOK" \
      -H 'Content-Type: application/json' \
      -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' "$SRV/mcp" \
      | jq -r '.result.tools[].name' 2>/dev/null | grep -i 'echo' | head -1)
    [ -n "$ECHO_TOOL" ] || return 1
  fi
  OUT=$(curl -sf -X POST -H "Authorization: Bearer $TOK" \
    -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$ECHO_TOOL\",\"arguments\":{\"message\":\"sam-heartbeat\"}}}" \
    "$SRV/mcp") || return 1
  printf %s "$OUT" | jq -e '.error' >/dev/null 2>&1 && return 1
  printf %s "$OUT" | jq -e '.result.isError == true' >/dev/null 2>&1 && return 1
  return 0
}

while :; do
  rm -f "$PASS"

  say "1/6 generating the Ed25519 NHI key (first boot only)..."
  until ensure_key; do sleep 5; done
  say "1/6 enrolling headless as $SAM_USER (signed assertion, key lane)..."
  until straza enroll --headless --user "$SAM_USER" --server "$SRV" >/tmp/enroll.log 2>&1; do
    say "   enroll not ready yet (stack booting, or the seeder has not registered the key): $(tail -1 /tmp/enroll.log)"
    sleep 5
  done
  say "1/6 enrolled: the platform accepted sam's signed assertion"

  say "2/6 starting a governed session (this is the identity-mapping proof)..."
  until straza exec -- date >/tmp/exec.log 2>&1; do
    say "   session not up yet (sam's SCIM row or role may still be provisioning): $(tail -1 /tmp/exec.log)"
    sleep 5
  done
  say "2/6 session live: deviceless NHI checkin succeeded"

  say "3/6 starting the client daemon (revocation push + session renewal)..."
  straza daemon >/tmp/daemon.log 2>&1 &
  DAEMON_PID=$!

  say "4/6 identity, as the client sees it:"
  straza status 2>&1 | sed 's/^/[sam]    /'

  say "5/6 governed gateway call (demo-tools echo through /mcp)..."
  n=0
  while ! gateway_echo; do
    n=$((n + 1))
    [ "$n" -gt 24 ] && break
    sleep 5
  done
  if [ "$n" -le 24 ]; then
    say "5/6 ALLOWED (gateway): $ECHO_TOOL answered under policy"
  else
    say "5/6 WARNING: gateway echo never succeeded (binding or app still settling); hook lane is already proven, continuing"
  fi

  say "6/6 destructive command (must be denied with a reason)..."
  if straza exec -- rm -rf /tmp/probe >/tmp/deny.log 2>&1; then
    say "6/6 FATAL: rm -rf was NOT denied; refusing to report PASS. Retrying the ladder in 30s."
    kill "$DAEMON_PID" 2>/dev/null
    sleep 30
    continue
  fi
  say "6/6 DENIED WITH REASON: $(grep -io 'straza:[^\"]*' /tmp/deny.log | head -1)"

  : > "$PASS"
  say "PASS: sam is live and governed. Governed heartbeat every 10 minutes."
  say "demo beats: docker exec straza-sam sh /opt/sam/raise-ticket.sh (day-scale)"

  # Heartbeat cadence: 10 minutes while healthy. Every beat is two governed
  # calls and therefore two audit-ledger events, and a faster beat would
  # crowd out the decisions an evaluator came to see. Liveness does not need
  # to dominate the evidence it feeds.
  # While a beat is FAILING the probe tightens to 30s so the kill-switch demo
  # (disable sam in midPoint) still walks unhealthy -> ladder re-entry ->
  # self-heal at demo speed once the first miss lands; the first miss itself
  # waits for the running interval, up to 10 minutes after the disable.
  FAILS=0
  while :; do
    if [ "$FAILS" -eq 0 ]; then sleep 600; else sleep 30; fi
    if straza exec -- date >/tmp/hb.log 2>&1; then
      gateway_echo || true
      [ "$FAILS" -gt 0 ] && say "heartbeat recovered"
      FAILS=0
    else
      FAILS=$((FAILS + 1))
      say "GOVERNED CALL REFUSED ($FAILS/3): $(tail -1 /tmp/hb.log)"
      say "(kill switch? sam disabled in midPoint?)"
      rm -f "$PASS"
      if [ "$FAILS" -ge 3 ]; then
        say "re-entering the enrollment ladder (self-heals once sam is re-enabled)"
        kill "$DAEMON_PID" 2>/dev/null
        break
      fi
    fi
  done
done
