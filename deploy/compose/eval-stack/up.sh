#!/bin/sh
# One-command eval stack with a working phone QR. The container cannot see
# which of the host's addresses your phone can dial, so this launcher picks
# the default-route IPv4 and hands it to strazad as the advertised approver
# URL. Wrong pick on a multi-adapter machine? Override and mint again:
#   STRAZA_APPROVER_TLS_PUBLIC_URL=https://<ip>:8443 ./up.sh
# Internet-reachable host? Do not use this: run the loopback overlay chain
# from https://docs.straza.ai/get-started/enterprise-demo-stack/#demo-defaults-and-the-hardened-shape
# instead.
set -eu
cd "$(dirname "$0")"

if [ -z "${STRAZA_APPROVER_TLS_PUBLIC_URL:-}" ]; then
  ip=""
  if command -v ip >/dev/null 2>&1; then
    ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.*src \([0-9.]*\).*/\1/p' | head -n 1)
  elif command -v route >/dev/null 2>&1; then
    iface=$(route -n get 1.1.1.1 2>/dev/null | sed -n 's/.*interface: \(.*\)/\1/p')
    [ -n "$iface" ] && ip=$(ipconfig getifaddr "$iface" 2>/dev/null || true)
  fi
  if [ -n "$ip" ]; then
    STRAZA_APPROVER_TLS_PUBLIC_URL="https://$ip:8443"
    export STRAZA_APPROVER_TLS_PUBLIC_URL
    echo "Phone approver will advertise $STRAZA_APPROVER_TLS_PUBLIC_URL (default-route interface)."
    echo "For a different address, run STRAZA_APPROVER_TLS_PUBLIC_URL=https://<ip>:8443 ./up.sh"
  else
    echo "Could not detect a LAN address, so a phone QR would carry an address your phone"
    echo "cannot dial. Before you add a phone, set STRAZA_APPROVER_TLS_PUBLIC_URL=https://<ip>:8443"
    echo "and re-run. Starting anyway."
  fi
else
  echo "Phone approver will advertise $STRAZA_APPROVER_TLS_PUBLIC_URL (from your environment)."
fi

docker compose -f compose.yaml up -d --build

echo
echo "Stack starting. Watch the seed: docker compose -p straza-eval logs -f eval-seed"
echo "Start at http://localhost:8400, which lists every address, account and walkthrough."
echo "Console: http://localhost:8420/console/"
echo "Phone: install the Straza approver app, then console -> Approvals -> Approver devices"
echo "  -> Add a phone -> scan the QR."
echo "Demo defaults assume a network you trust. On any other network, use the loopback"
echo "overlay chain: https://docs.straza.ai/get-started/enterprise-demo-stack/#demo-defaults-and-the-hardened-shape"
