#!/bin/sh
# Demo beat: sam raises its day-scale deploy gate (agent-guardrails
# deploy-ticket, class: ticket). Run it, decide as a human, run it again.
set -u
echo "[sam] attempting the gated deploy as an autonomous agent..."
if straza exec -- ./deploy prod; then
  echo "[sam] deploy RAN: a live grant was consumed (single-use; the next run raises a fresh ticket)."
else
  echo "[sam] the deploy did not run; the reason above names the ticket."
  echo "[sam] sam is agencyMode=autonomous, so it can NEVER approve itself (engine clamp)."
  echo "[sam] the ticket routes to sam's SPONSOR (alice, on her enrolled phone) and to"
  echo "[sam] the sec-approvers deciders (alice or ivan in the console under Approvals,"
  echo "[sam] or http://localhost:8420/approvals/), whoever answers first."
  echo "[sam] then re-run this script; the matching call consumes the grant exactly once."
fi
