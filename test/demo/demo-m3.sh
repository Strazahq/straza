#!/usr/bin/env bash
# make demo-m3: the M3 milestone demo. Drop `github.app.yaml`
# into the watched apps/ directory and publish the draft it becomes, then
# create the server's own role `github-dev` → a user with that role sees
# GitHub tools in Claude Code; a user without the role
# sees nothing; the GitHub token never appears client-side (gateway-side
# injection).
#
# The full flow is asserted as a Go test (internal/server/demo_m3_test.go,
# TestM3Demo, run `make demo-m3`); this script is the human walkthrough
# against the real binaries and, optionally, real GitHub.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$ROOT/bin"
DATA="$(mktemp -d)"
trap 'kill "${STRAZAD_PID:-0}" 2>/dev/null || true; rm -rf "$DATA"' EXIT

echo "== building binaries =="
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN/strazad" ./cmd/strazad \
  && CGO_ENABLED=0 go build -o "$BIN/strazactl" ./cmd/strazactl )

echo "== starting strazad (standalone; watched apps dir: $DATA/strazad/apps) =="
STRAZA_DATA_DIR="$DATA/strazad" "$BIN/strazad" serve --profile standalone --listen 127.0.0.1:8420 \
  >"$DATA/strazad.log" 2>&1 &
STRAZAD_PID=$!
until curl -sf http://127.0.0.1:8420/healthz >/dev/null 2>&1; do sleep 0.2; done
ADMIN_PW="$(grep -o '"password":"[^"]*"' "$DATA/strazad.log" | head -1 | cut -d'"' -f4)"
echo "bootstrap admin password: $ADMIN_PW"

cat <<'NOTE'

Manual steps from here (the automated, assertion-backed version is
`make demo-m3`):

  1. strazactl login --server http://127.0.0.1:8420   # device flow as admin
  2. strazactl users create alice --password '...'
     strazactl users create bob   --password '...'   # no role
  3. cp spec/app-manifest/examples/valid-github-remote.yaml \
        "$STRAZA_DATA_DIR/apps/github.app.yaml"      # THE demo moment
     strazactl drafts list                            # → a draft from the apps directory
     strazactl drafts publish <id>                    # → github installed
     strazactl apps list                              # → github ... running
  4. strazactl roles create github-dev --app github --tools "get_*,list_*,search_*,create_issue"
     strazactl assign github-dev --user alice
     strazactl apps secret set github --role github-dev   # type the token at the hidden prompt
  5. strazactl policy apply -f - <<'EOF' ; strazactl policy activate github-readmostly
     apiVersion: straza.dev/v1beta1
     kind: PolicySet
     metadata: {name: github-readmostly}
     spec:
       match: {roles: [github-dev]}
       rules:
         - id: github-readmostly
           tools: [mcp.call]
           apps: [github]
           toolNames: {allow: ["get_*", "list_*", "search_*", "create_issue"]}
           effect: allow
     EOF
  6. Wire Claude Code (alice's machine) to the gateway:
        claude mcp add --transport http github-via-straza \
          http://127.0.0.1:8420/mcp \
          --header "Authorization: Bearer <alice session token>"
     (straza's checkin mints the session token; `strazactl login` as
     alice prints one too.)
  7. In Claude Code: /mcp → github-via-straza lists get_issue, list_issues,
     create_issue, never delete_repository. Ask it to fetch an issue: the
     call works, and the GitHub token exists only inside strazad.
     Repeat as bob → the server exposes no tools at all.
  8. rm "$STRAZA_DATA_DIR/apps/github.app.yaml", then strazactl drafts publish
     the removal draft it proposes → tools vanish (list_changed).
  9. strazactl audit tail                       # straza.audit.mcp decisions
NOTE
