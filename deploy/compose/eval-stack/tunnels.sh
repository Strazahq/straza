#!/usr/bin/env bash
# SSH tunnels to a remote Straza eval stack
# (https://docs.straza.ai/get-started/enterprise-demo-stack/#demo-defaults-and-the-hardened-shape). Forwards every service port to localhost, then keeps the connection
# open (Ctrl+C to stop):
#   8400 landing page        8420 strazad + console   8443 approver https (phone)
#   8480 Keycloak            8087 midPoint            5601 Kibana (compose.siem.yaml overlay)
#   8477 governed-agents demo chat face (compose.demo.yaml overlay, which builds
#        from a checkout of the straza-agents-demo repository, which is not public)
#
# Usage:  ./tunnels.sh user@host      (or: STRAZA_EVAL_HOST=user@host ./tunnels.sh)
#
# Then open http://localhost:8400 and start from the landing page. Use
# localhost, not 127.0.0.1: Keycloak's sign-in cookie is bound to that name.
set -euo pipefail
HOST="${1:-${STRAZA_EVAL_HOST:-}}"
if [ -z "$HOST" ]; then
  echo "usage: $0 user@host   (or set STRAZA_EVAL_HOST)" >&2
  exit 2
fi
echo "tunneling 8400/8420/8443/8480/8087/5601/8477 -> $HOST (Ctrl+C to stop)"
exec ssh -N \
  -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 \
  -L 8400:localhost:8400 \
  -L 8420:localhost:8420 \
  -L 8443:localhost:8443 \
  -L 8480:localhost:8480 \
  -L 8087:localhost:8087 \
  -L 5601:localhost:5601 \
  -L 8477:localhost:8477 \
  "$HOST"
