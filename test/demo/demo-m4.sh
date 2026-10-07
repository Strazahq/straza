#!/usr/bin/env bash
# make demo-m4: the M4 milestone demo. An IdM provisions a user
# over SCIM into a role-mapped group; the user enrolls and works through the
# gateway; the IdM deactivates the user; the live session loses everything in
# under 5 seconds (the kill switch). Plus: the same PolicySet is enforced
# identically across Claude Code, Codex, and Gemini.
#
# The authoritative, assertion-backed version is `make demo-m4`
# (internal/server/demo_m4_test.go TestM4Demo, kill switch measured ~20 ms;
# and internal/agentguard TestCrossHarnessIdenticalDecisions). This script is
# the human walkthrough against the real binaries + a real Postgres/NATS.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

cat <<'NOTE'
Enterprise topology (docker compose): Postgres + NATS + strazad.

  docker compose -f deploy/compose/docker-compose.yaml up -d
  # set STRAZA_OIDC_ISSUER/CLIENT_ID for your IdP first (enterprise needs one)

Operator steps (as admin):
  1. strazactl scim-token create --name my-idm      # bearer for the connector
  2. Point midPoint/Okta at  https://<strazad>/scim/v2  with that token.
     Map: userName, externalId (= OIDC sub), displayName, emails, active.
     Push a group named  straza-dev  -> implies role "github-dev".

Deploy the governed app, the role it owns and the policy (as admin):
  3. cp spec/app-manifest/examples/valid-github-remote.yaml \
        "$APPS_DIR/github.app.yaml"
     strazactl roles create github-dev --app github --tools "get_*,list_*,create_issue"
     strazactl apps secret set github --role github-dev      # type the token at the hidden prompt
     strazactl policy apply -f policy.yaml ; strazactl policy activate github-dev

Provisioning (from the IdM, shown here as raw SCIM):
  4. POST /scim/v2/Users     {"userName":"grace","externalId":"idm-42","active":true}
     POST /scim/v2/Groups    {"displayName":"straza-dev","members":[{"value":"<uid>"}]}
     -> grace now holds role github-dev by group-derived resolution.

The user works:
  5. grace enrolls (straza enroll) and wires Claude Code to  /mcp .
     /mcp lists github__get_issue etc.; calls succeed; token stays server-side.
     The SAME policy in Codex and Gemini decides identically (K3).

The kill switch:
  6. Deactivate grace in the IdM  ->  SCIM active:false.
     Her next tool call is denied (403 at the gateway) in < 5 s, measured
     ~20 ms in-process; the straza daemon drops local session state on the
     push channel just as fast (D24). Verify:
        strazactl audit tail        # straza.revocation.user + denied calls
NOTE

echo
echo "Run the authoritative demo:  (cd \"$ROOT\" && make demo-m4)"
