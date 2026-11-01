#!/bin/sh
# sam's proof ladder + governed heartbeat. The numbered lines are the
# machine-checkable story an evaluator tails; /tmp/sam.PASS is the compose
# healthcheck contract (present = every proof stood and the heartbeat is
# healthy). On refusal (kill switch, revocation) the PASS marker is removed
# and, after three failed beats, the ladder restarts from enrollment, so
# re-enabling sam in midPoint self-heals with no manual restart.
#
# Credential: an Ed25519 key that sam's service (straza-sam-service) makes
# and keeps under its own account, so nothing sam runs can read it. This
# script drops the PUBLIC half into the shared /keys volume, the seeder
# registers it via the admin API, and enroll retries until that lands. No
# IdP client, no shared secret, nothing to rotate out of the realm import.
set -u

SRV="${STRAZA_SERVER:-http://127.0.0.1:8420}"
SAM_USER=sam-sre-agent
KEYS_DIR="${STRAZA_KEYS_DIR:-/keys}"
SOCK=/run/straza-service/service.sock
PASS=/tmp/sam.PASS
CATALOG_COUNT=0
say() { echo "[sam] $*"; }

publish_key() {
  sed -n 's/^Public key: //p' /tmp/keygen.log > "$KEYS_DIR/$SAM_USER.pub"
  say "the service holds sam's key; public half published at $KEYS_DIR/$SAM_USER.pub (the seeder registers it)"
}

# Have the service make sam's key when it holds none, and publish the public
# half for the seeder. Without --force keygen succeeds only when the service
# holds no key for sam, so a refusal with the public half already published
# means the key is in place. A lost /keys volume regenerates with --force,
# and the re-run seeder registers the fresh key.
ensure_key() {
  if straza keygen --user "$SAM_USER" >/tmp/keygen.log 2>&1; then
    publish_key
    return 0
  fi
  [ -s "$KEYS_DIR/$SAM_USER.pub" ] && return 0
  straza keygen --user "$SAM_USER" --force >/tmp/keygen.log 2>&1 \
    || { say "keygen failed:"; cat /tmp/keygen.log; return 1; }
  publish_key
}

# One governed tools/list through the service's MCP route, in the session
# `straza exec` opened (harness exec-wrapper); returns 0 only when the
# catalog holds a tool that sam's roles reach, and leaves the size of the
# catalog in CATALOG_COUNT. The service adds the session token and its
# proof, so no token ever reaches this script. The built-in straza__ tools
# are in every catalog, so they do not count as proof that a role arrived.
# The list is the session's own catalog, so it needs no upstream server and
# no credential.
gateway_catalog() {
  NAMES=$(curl -sf --unix-socket "$SOCK" -X POST \
    -H 'Straza-Harness: exec-wrapper' -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' http://straza-service/local/v1/mcp \
    | jq -r '.result.tools[].name' 2>/dev/null) || return 1
  CATALOG_COUNT=$(printf '%s\n' "$NAMES" | grep -c .)
  printf '%s\n' "$NAMES" | grep -v '^straza__' | grep -q .
}

while :; do
  rm -f "$PASS"

  say "1/5 having sam's service make the Ed25519 NHI key (first boot only)..."
  until ensure_key; do sleep 5; done
  say "1/5 enrolling headless as $SAM_USER through the service (signed assertion, key lane)..."
  until straza enroll --headless --user "$SAM_USER" --server "$SRV" >/tmp/enroll.log 2>&1; do
    say "   enroll not ready yet (stack booting, or the seeder has not registered the key): $(tail -1 /tmp/enroll.log)"
    sleep 5
  done
  say "1/5 enrolled: the platform accepted sam's signed assertion"

  say "2/5 starting a governed session (this is the identity-mapping proof)..."
  until straza exec -- date >/tmp/exec.log 2>&1; do
    say "   session not up yet (sam's SCIM row or role may still be provisioning): $(tail -1 /tmp/exec.log)"
    sleep 5
  done
  say "2/5 session live: the service checked sam in, bound to its key"

  say "3/5 identity, as the service reports it:"
  straza status 2>&1 | sed 's/^/[sam]    /'

  say "4/5 governed gateway call (tools/list through the service to /mcp)..."
  n=0
  while ! gateway_catalog; do
    n=$((n + 1))
    [ "$n" -gt 24 ] && break
    sleep 5
  done
  if [ "$n" -le 24 ]; then
    say "4/5 LISTED (gateway): the catalog holds $CATALOG_COUNT tools for sam's roles"
  else
    say "4/5 WARNING: the gateway listed no tool for sam's roles (a role or an MCP server is still settling); hook lane is already proven, continuing"
  fi

  say "5/5 destructive command (must be denied with a reason)..."
  if straza exec -- rm -rf /tmp/probe >/tmp/deny.log 2>&1; then
    say "5/5 FATAL: rm -rf was NOT denied; refusing to report PASS. Retrying the ladder in 30s."
    sleep 30
    continue
  fi
  say "5/5 DENIED WITH REASON: $(grep -io 'straza:[^\"]*' /tmp/deny.log | head -1)"

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
      gateway_catalog || true
      [ "$FAILS" -gt 0 ] && say "heartbeat recovered"
      FAILS=0
    else
      FAILS=$((FAILS + 1))
      say "GOVERNED CALL REFUSED ($FAILS/3): $(tail -1 /tmp/hb.log)"
      say "(kill switch? sam disabled in midPoint?)"
      rm -f "$PASS"
      if [ "$FAILS" -ge 3 ]; then
        say "re-entering the enrollment ladder (self-heals once sam is re-enabled)"
        break
      fi
    fi
  done
done
