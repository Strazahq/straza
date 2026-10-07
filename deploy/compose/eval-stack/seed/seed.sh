#!/bin/sh
# Zero-touch eval seeder. Runs once at `compose up` and is idempotent, so a
# re-run converges instead of failing. It authenticates to Keycloak as the
# transient seed-bootstrap actor, falling back on a re-run to alice, who is
# the admin midPoint assigned by then. It then enrolls with strazad, mints the
# admin API token, seeds the Straza-born roles, seeds the IGA layer into
# midPoint, imports the Straza and Keycloak resources, imports the LiveSync
# and recon tasks that materialize the Straza catalog, waits for the imported
# roles, renders the three access packages from one template and imports them
# with the admin and approver packages, creates the demo cast, asserts that
# joe landed in both Straza and Keycloak and that sam landed in Straza, and
# registers the working agents' Ed25519 public keys from /keys. The pods
# generate those keys themselves.
set -u

KC=http://keycloak:8080
WD=http://strazad:8420
MP=http://midpoint:8080/midpoint
MP_AUTH="administrator:${MP_ADMIN_PASSWORD}"
KC_ADMIN_PASSWORD="${KC_ADMIN_PASSWORD:-admin}"
RES_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c01"
DEVELOPER_PKG_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c02" # BR:Straza-developer-access, joe's access package
OPERATOR_PKG_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c50" # BR:Straza-operator-access, sam's access package
ANALYST_PKG_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c51" # BR:Straza-analyst-access, nobody at boot
ACCOUNT_ROLE_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c0b"
ADMIN_PKG_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c0c" # BR:Straza-admin-access
APPROVER_PKG_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c0d" # BR:Straza-approver-access
MCP_ADMIN_PKG_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c0f" # BR:Straza-global-mcp-admin-access
CAROL_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c40" # holder of the demo-tools server admin role
DAVE_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c41" # holder of BR:Straza-global-mcp-admin-access
ALICE_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c4a" # the admin and dev-ops manager
IVAN_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c46" # the approval decider
KC_RES_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c04"
KC_ACCOUNT_ROLE_OID="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c0e" # AR:Keycloak:Account
# The working agents authenticate with an Ed25519 key: keygen happens in
# each pod, the pod drops the PUBLIC half into the shared /keys volume, and
# this seeder registers it via PUT /v1/admin/users/{id}/nhi-key. The
# assertion subject is the username, so no pinned identity provider client
# or externalId exists.
JOE_USER=joe-java-developer-agent
SAM_USER=sam-sre-agent
KEYS_DIR=/keys
say() { echo "[seed] $*"; }
fail() { echo "[seed] FATAL: $*" >&2; exit 1; }
# body_has PATTERN: reads the whole response on stdin, then matches it. A
# grep -q straight on curl closes the pipe at the first match, and midPoint
# then logs the aborted response as an ERROR on every boot.
body_has() { B=$(cat); printf '%s' "$B" | grep -q -- "$1"; }

wait_url() { # $1 url, $2 desc, $3 tries, [$4 basic-auth]
  n=0
  until curl -sfo /dev/null ${4:+-u "$4"} "$1"; do
    n=$((n + 1))
    [ "$n" -gt "$3" ] && fail "$2 not ready after $3 tries"
    sleep 2
  done
  say "$2 ready"
}

import_mp() { # $1 endpoint (resources|roles|users|tasks|archetypes|objectTemplates), $2 file, $3 desc
  HTTP=$(curl -s -o /tmp/mp-import.out -w '%{http_code}' -u "$MP_AUTH" -H 'Content-Type: application/xml' \
    -X POST --data-binary @"$2" "$MP/ws/rest/$1?options=overwrite")
  case "$HTTP" in
    2*) say "$3 imported (HTTP $HTTP)" ;;
    *) head -c 800 /tmp/mp-import.out >&2; echo >&2; fail "$3 import HTTP $HTTP" ;;
  esac
}

patch_mp() { # $1 type/oid, $2 objectModification file, $3 desc
  HTTP=$(curl -s -o /tmp/mp-patch.out -w '%{http_code}' -u "$MP_AUTH" -H 'Content-Type: application/xml' \
    -X PATCH --data-binary @"$2" "$MP/ws/rest/$1")
  case "$HTTP" in
    2*) say "$3 applied (HTTP $HTTP)" ;;
    *) head -c 800 /tmp/mp-patch.out >&2; echo >&2; fail "$3 patch HTTP $HTTP" ;;
  esac
}

test_mp_resource() { # $1 oid, $2 desc
  TEST=$(curl -sf -u "$MP_AUTH" -H 'Content-Type: application/json' -X POST "$MP/ws/rest/resources/$1/test")
  echo "$TEST" | grep -qi '"success"\|>success<\|status.*success' \
    && say "$2 connection test: SUCCESS" \
    || { echo "$TEST" | head -c 600 >&2; fail "$2 connection test did not report success"; }
}

# --- convergent user import: an interrupted earlier run can leave a user
# half-provisioned three ways. A stale repo shadow answers the re-import with
# 409, an orphaned Straza/Keycloak account answers 500 wrapping the
# connector's AlreadyExists, and the focus can be stored linkRef-less. The
# Keycloak resource correlates by username and links pinned accounts, but
# the Straza SCIM resource cannot self-heal these, so without this a re-run
# of `up` could never converge. Instead: drop exactly the stale artifact the
# error names (shadow by its quoted oid, else the user's target accounts,
# which the seed itself created and the retried import recreates fully
# linked) and try again, bounded. Seed-OWNED users only: alice's KC account
# is pinned and hard-guarded from deletion.
KC_TOKEN=""
kc_token() {
  [ -n "$KC_TOKEN" ] && return 0
  KC_TOKEN=$(curl -sf -d "grant_type=password&client_id=admin-cli&username=admin&password=$KC_ADMIN_PASSWORD" \
    "$KC/realms/master/protocol/openid-connect/token" | jq -r '.access_token // empty')
  [ -n "$KC_TOKEN" ]
}
drop_orphan_accounts() { # $1 username
  # alice is seed-owned (midPoint-born) but her KEYCLOAK account is the
  # pinned realm import, and its id is load-bearing: strazad external_id
  # bindings and enrollments hang on it. A Straza orphan may be dropped and
  # reprovisioned, the pinned KC account never: the KC resource correlates
  # by username and links the pinned account instead of erroring (its realm
  # entries carry createdTimestamp because the openstandia connector throws
  # a NullPointerException on a null one).
  # joe's KC account is midPoint-provisioned, not pinned: normal lane.
  if [ "$1" = "alice" ]; then
    SID=$(curl -sf -H "Authorization: Bearer $SCIM_TOKEN" \
      "$WD/scim/v2/Users?filter=userName%20eq%20%22$1%22" | jq -r '.Resources[0].id // empty')
    [ -n "$SID" ] && curl -sf -X DELETE -H "Authorization: Bearer $SCIM_TOKEN" \
      "$WD/scim/v2/Users/$SID" >/dev/null 2>&1 \
      && say "dropped orphaned Straza account for $1 (pinned Keycloak account untouched)"
    return 0
  fi
  SID=$(curl -sf -H "Authorization: Bearer $SCIM_TOKEN" \
    "$WD/scim/v2/Users?filter=userName%20eq%20%22$1%22" | jq -r '.Resources[0].id // empty')
  [ -n "$SID" ] && curl -sf -X DELETE -H "Authorization: Bearer $SCIM_TOKEN" \
    "$WD/scim/v2/Users/$SID" >/dev/null 2>&1 \
    && say "dropped orphaned Straza account for $1"
  if kc_token; then
    KID=$(curl -sf -H "Authorization: Bearer $KC_TOKEN" \
      "$KC/admin/realms/straza/users?username=$1&exact=true" | jq -r '.[0].id // empty')
    [ -n "$KID" ] && curl -sf -X DELETE -H "Authorization: Bearer $KC_TOKEN" \
      "$KC/admin/realms/straza/users/$KID" >/dev/null 2>&1 \
      && say "dropped orphaned Keycloak account for $1"
  fi
  return 0
}
import_user_convergent() { # $1 file, $2 username, $3 desc, $4 hard|soft
  t=0
  while :; do
    t=$((t + 1))
    HTTP=$(curl -s -o /tmp/mp-import.out -w '%{http_code}' -u "$MP_AUTH" -H 'Content-Type: application/xml' \
      -X POST --data-binary @"$1" "$MP/ws/rest/users?options=overwrite")
    case "$HTTP" in
      250) ;; # partial error: a projection failed mid-flight, handled below
      2*) say "$3 imported (HTTP $HTTP)"; return 0 ;;
    esac
    # HTTP 250 = midPoint stored the focus but a projection errored. One
    # cause: the metarole's async member replay recomputes joe concurrently
    # with his import, both race the Keycloak create, one gets
    # AlreadyExists, and midPoint links the conflicting account and aborts
    # the remaining projection wave. The overwrite re-import replays the
    # projector over the now-linked state and converges. Up to 30 re-imports
    # four seconds apart, about two minutes plus the round trips: the same
    # budget the SCIM landing waits below give midPoint, because a cold
    # projector wave on a small host can take that long.
    if [ "$t" -le 30 ] && [ "$HTTP" = "250" ]; then
      say "$3: import answered HTTP 250 (partial error) - re-importing to replay the projector"
      sleep 4
      continue
    fi
    if [ "$t" -le 30 ] && grep -qiE "AlreadyExists|already exists" /tmp/mp-import.out; then
      # Both at once, every retry: dropping only the shadow never converges
      # (discovery re-mints a shadow for the still-existing account on every
      # failed attempt), and account drops alone leave stale repo
      # shadows 409ing the import. Account drops are idempotent lookups after
      # the first pass.
      say "$3: left-over state from an interrupted earlier seed - dropping orphaned account(s)/shadow and retrying"
      drop_orphan_accounts "$2"
      SHOID=$(grep -o 'shadow:[0-9a-f-]\{36\}' /tmp/mp-import.out | head -1 | cut -d: -f2)
      [ -n "$SHOID" ] && curl -sf -u "$MP_AUTH" -X DELETE \
        "$MP/ws/rest/shadows/$SHOID?options=raw" >/dev/null 2>&1
      continue
    fi
    head -c 800 /tmp/mp-import.out >&2; echo >&2
    [ "$4" = "hard" ] && fail "$3 import HTTP $HTTP (did not converge). Reset the stack: docker compose -f deploy/compose/eval-stack/compose.yaml down -v"
    say "WARNING: $3 import HTTP $HTTP - sample user skipped"
    return 1
  done
}

wait_url "$KC/realms/straza/.well-known/openid-configuration" "keycloak" 120

wait_url "$WD/healthz" "strazad" 60

# The seed drives as the TRANSIENT seed-bootstrap actor (oidc.bootstrapAdmin
# mints its admin on an empty store only, while no straza-admin assignment
# exists). On re-runs the
# actor is already demoted and locked, so the lane falls back to alice,
# whose admin is by then the midPoint-granted AR:straza-admin membership.
# Fail-closed: a candidate only wins with a PROVEN admin read.
admin_session() { # $1 username, $2 password -> sets ST on success
  ST=""
  IDT=$(curl -sf -d "grant_type=password&client_id=straza&username=$1&password=$2&scope=openid" \
    "$KC/realms/straza/protocol/openid-connect/token" | jq -r '.id_token // empty')
  [ -n "$IDT" ] || return 1
  DT=$(curl -sf -X POST -H 'Content-Type: application/json' \
    -d "{\"id_token\":\"$IDT\",\"client_kind\":\"human\",\"device\":{\"name\":\"eval-seed\",\"platform\":\"linux\",\"fingerprint\":\"eval-seed-fixed\"}}" \
    "$WD/v1/enroll" | jq -r '.device_token // empty')
  [ -n "$DT" ] || return 1
  ST=$(curl -sf -X POST -H 'Content-Type: application/json' \
    -d "{\"device_token\":\"$DT\",\"harness\":{\"name\":\"strazactl\",\"version\":\"eval-seed\"}}" \
    "$WD/v1/checkin" | jq -r '.session_token // empty')
  [ -n "$ST" ] || return 1
  curl -sfo /dev/null -H "Authorization: Bearer $ST" "$WD/v1/admin/api-tokens" || return 1
  BOOT_USER="$1"
  say "$1 enrolled + checked in (admin session proven)"
}
admin_session seed-bootstrap 'Seed-B00t!26' \
  || admin_session alice alice \
  || fail "no admin session: seed-bootstrap (first boot, oidc.bootstrapAdmin) and alice (re-run, admin granted by midPoint) both failed. Check the strazad and Keycloak logs, then re-run eval-seed."

# Provisioning credentials wear production-style names (midpoint-provisioning
# here, midpoint-pull for the evidence lane below): the token lists are demo
# surfaces (console -> Security -> SCIM/API tokens), so seeded names must read
# like an operator wrote them, and stable names stop re-runs from accreting
# timestamped debris rows. Names are unique server-side and the plaintext is
# shown exactly once, so a stable name means a re-run revokes its own earlier
# mint first; the resource import below always carries this run's fresh
# secret, so the superseded credential dying with it is the correct converge.
revoke_named_token() { # $1 kind (api-tokens), $2 name
  TID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/$1" \
    | jq -r ".[] | select(.name==\"$2\") | .id" 2>/dev/null | head -1)
  [ -n "$TID" ] && curl -sf -X DELETE -H "Authorization: Bearer $ST" \
    "$WD/v1/admin/$1/$TID" >/dev/null 2>&1 \
    && say "$2: token left by an earlier seed run revoked (re-minting)"
  return 0
}

# ONE credential for midPoint: an admin API token whose scope
# carries scim:read and scim:write for the account and group classes over
# /scim/v2 (write does not imply read) and the read grants for the evidence
# classes and the change feed. The seeder
# reads the SCIM plane with the same token below.
revoke_named_token api-tokens midpoint
UNI_TOKEN=$(curl -sf -X POST -H "Authorization: Bearer $ST" -H 'Content-Type: application/json' \
  -d '{"name":"midpoint","scope":"scim:read,scim:write,identity:read,apps:read,changes:read,config:read"}' "$WD/v1/admin/api-tokens" | jq -r '.token // empty')
[ -n "$UNI_TOKEN" ] || fail "admin api-token mint failed"
SCIM_TOKEN="$UNI_TOKEN"
say "admin API token minted for midPoint (midpoint: scim:read,scim:write plus the evidence reads)"

# Straza-born roles: the SCIM surface renders each role as a group and refuses
# lifecycle, so being born HERE is the only way a role exists. midPoint
# assigns the business roles developer, operator and analyst, and imports each
# as a RoleType focus named BR:<role>, midPoint's own code for a role that
# composes other roles, while the wire name stays developer. The application
# roles carry the access rows and each belongs to the one server it reaches,
# so they are created further down, once the servers are installed, and the
# business roles compose them there. sec-approvers (kind approver) carries
# DECIDE authority and nothing else. auditor is a Straza role: read-only
# console oversight through the roleAreas demo config. The straza-enroll-*
# roles are strazad-born.
# A 409 means an earlier run created the role, so the description is PATCHed
# onto the existing row. A seed never renames a role or rewrites a present
# policy, so a family change lands by down -v and a reseed, never a re-run.
seed_role() { # $1 name, $2 kind, $3 description
  BODY=$(jq -n --arg n "$1" --arg k "$2" --arg d "$3" '{name:$n,kind:$k,description:$d}')
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $ST" \
    -H 'Content-Type: application/json' -d "$BODY" "$WD/v1/admin/roles")
  case "$CODE" in
    201) say "role $1 created" ;;
    409)
      RID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/roles" \
        | jq -r ".[] | select(.name==\"$1\") | .id")
      if [ -n "$RID" ] && curl -sf -X PATCH -H "Authorization: Bearer $ST" \
        -H 'Content-Type: application/json' \
        -d "$(jq -n --arg d "$3" '{description:$d}')" \
        "$WD/v1/admin/roles/$RID" >/dev/null; then
        say "role $1 already present, description converged"
      else
        say "WARNING: role $1 present but description refresh failed"
      fi ;;
    *) fail "role $1 create answered HTTP $CODE" ;;
  esac
}
seed_role developer business "The developer role: composes demo-tools-sandbox, every tool of the demo-tools server, views-demo-tools, the MCP Apps example server, and midpoint-self-service, the midPoint reads plus request_role. joe holds it from midPoint."
seed_role operator business "The operator role: composes demo-tools-readers, the read-only tools the demo-tools server's admin defined, and midpoint-operations, every midPoint tool with every write needing approval. sam holds it from midPoint."
seed_role analyst business "The analyst role: composes demo-tools-readers and midpoint-self-service. Nobody holds it at boot; assign it to nina in midPoint and watch her land in Straza."
seed_role sec-approvers approver "The deciders of approval requests for governed agents. Reaches no tools. The identity manager assigns and certifies its holders."
seed_role auditor straza "Read-only oversight: audit, sessions and transcripts. Opens the console, never agent tools."

# --- Demo tool bindings: a fresh boot must demo the role-gated catalog with
# ZERO manual console steps. Unbound apps are invisible by design, so
# without this a fresh stack shows joe an empty /mcp. Idempotent:
# a role or implication already present is skipped. Non-fatal: a missing app
# degrades the demo, never the seed.
ensure_app() { # $1 manifest file name under /seed/apps
  # The two demo servers are installed here, through the admin API, and not
  # from the GitOps apps directory, so the console and strazactl can change
  # them. An install of a name that exists is a change,
  # recorded as such, so a re-run converges every server on its seed file.
  ST=$(curl -sf -H 'Content-Type: application/json' \
    -d "{\"device_token\":\"$DT\",\"harness\":{\"name\":\"strazactl\",\"version\":\"eval-seed\"}}" \
    "$WD/v1/checkin" | jq -r '.session_token // empty')
  [ -n "$ST" ] || { say "WARNING: seed re-checkin failed - install of $1 skipped"; return 0; }
  ANSWER=$(curl -s -X POST -H "Authorization: Bearer $ST" -H 'Content-Type: application/yaml' \
    --data-binary "@/seed/apps/$1" "$WD/v1/admin/apps")
  NAME=$(echo "$ANSWER" | jq -r '.name // empty' 2>/dev/null)
  if [ -n "$NAME" ]; then
    say "app $NAME installed from $1 (status $(echo "$ANSWER" | jq -r '.status'))"
  else
    say "WARNING: install of $1 failed: $(echo "$ANSWER" | jq -r '.error // .' 2>/dev/null | head -c 300)"
  fi
}

seed_server_role() { # $1 role name, $2 owning server, $3 tools JSON array, $4 description
  # One call creates the role owned by its server together with its access
  # row. $3 is that row's tool matcher: ["*"] is every tool and tools added
  # later, which only a global admin may give, and the seed is one. A list
  # narrows the role to its real surface, so the catalog lines up with what
  # policy permits. A 409 means an earlier run made the role, and it stays.
  # Re-checkin first: on a first boot midPoint provisioning takes minutes and
  # alice's 5-minute session token is stale by the time this runs. The pull
  # connector uses a durable API token for the same reason.
  ST=$(curl -sf -H 'Content-Type: application/json' \
    -d "{\"device_token\":\"$DT\",\"harness\":{\"name\":\"strazactl\",\"version\":\"eval-seed\"}}" \
    "$WD/v1/checkin" | jq -r '.session_token // empty')
  [ -n "$ST" ] || { say "WARNING: seed re-checkin failed - server-owned role $1 skipped"; return 0; }
  BODY=$(jq -n --arg n "$1" --arg s "$2" --argjson t "$3" --arg d "$4" \
    '{name:$n,kind:"application",server:$s,tools:$t,description:$d}')
  CODE=$(curl -s -o /tmp/server-role.out -w '%{http_code}' -X POST -H "Authorization: Bearer $ST" \
    -H 'Content-Type: application/json' -d "$BODY" "$WD/v1/admin/roles")
  case "$CODE" in
    201)
      if [ "$(jq -r '.server // empty' /tmp/server-role.out)" = "$2" ]; then
        say "server-owned role $1 created on $2 (tools $3)"
      else
        say "WARNING: role $1 was created but the answer names no owning server, so this strazad predates server-owned roles and the role is global. Delete it and re-seed once the stack runs a build that carries them"
      fi ;;
    409) say "server-owned role $1 already present on $2" ;;
    *) say "WARNING: creating the role $1 on the server $2 answered HTTP $CODE, so the demo lacks the tools that role reaches. Check that $2 installed above, then run the seed again. The answer was: $(head -c 300 /tmp/server-role.out)" ;;
  esac
}

ensure_implication() { # $1 composing role name, $2 implied role name (both Straza-born)
  # Same stale-token hazard as seed_server_role: re-checkin first.
  ST=$(curl -sf -H 'Content-Type: application/json' \
    -d "{\"device_token\":\"$DT\",\"harness\":{\"name\":\"strazactl\",\"version\":\"eval-seed\"}}" \
    "$WD/v1/checkin" | jq -r '.session_token // empty')
  [ -n "$ST" ] || { say "WARNING: seed re-checkin failed - implication $1 -> $2 skipped"; return 0; }
  RID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/roles" \
    | jq -r ".[] | select(.name==\"$1\") | .id")
  TID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/roles" \
    | jq -r ".[] | select(.name==\"$2\") | .id")
  if [ -z "$RID" ] || [ -z "$TID" ]; then
    say "WARNING: implication $1 -> $2 skipped (role missing)"
    return 0
  fi
  if curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/roles/$RID/implications" \
    | jq -e ".[] | select(.implies_id==\"$TID\")" >/dev/null 2>&1; then
    say "implication $1 -> $2 already present"
    return 0
  fi
  curl -sf -X POST -H "Authorization: Bearer $ST" -H 'Content-Type: application/json' \
    -d "{\"implies_role_id\":\"$TID\"}" "$WD/v1/admin/roles/$RID/implications" >/dev/null \
    && say "implication $1 -> $2 created (holders of $1 also hold $2)" \
    || say "WARNING: implication $1 -> $2 failed"
}
wait_url "$MP/ws/rest/self" "midpoint" 180 "$MP_AUTH"

# The MCP tool mirror is retired: midPoint never writes a tool, and Straza
# decides access by role, so one read-only midPoint role per tool only grew
# the repository. A re-seed over an older stack deletes what the mirror left,
# before the extension schema drops strazaApp: the tool LiveSync and recon
# tasks first, so nothing re-creates a tool role, then the tool shadows and
# the MCT: roles, then the mcp-tool archetype, all in raw mode (repository
# only, because the tool class is read-only), then its GUI view. Each kind is
# searched first and only what exists is deleted, so a fresh stack, which
# has none of it, logs nothing here.
retire_raw() { # $1 REST type, $2 filter XML, $3 what
  OIDS=$(curl -sf -u "$MP_AUTH" -H 'Content-Type: application/xml' -H 'Accept: application/json' -X POST \
    --data-binary "<q:query xmlns:q=\"http://prism.evolveum.com/xml/ns/public/query-3\" xmlns:ri=\"http://midpoint.evolveum.com/xml/ns/public/resource/instance-3\"><q:filter>$2</q:filter></q:query>" \
    "$MP/ws/rest/$1/search?options=raw" | jq -r '.object.object[]?.oid' 2>/dev/null)
  N=0
  for oid in $OIDS; do
    curl -sf -u "$MP_AUTH" -X DELETE "$MP/ws/rest/$1/$oid?options=raw" >/dev/null 2>&1 && N=$((N+1))
  done
  [ "$N" -eq 0 ] || say "retired $N $3 deleted (older-seed leftover)"
}
retire_raw tasks "<q:inOid><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c13</q:value><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c18</q:value></q:inOid>" "tool mirror tasks"
# A shadow search on a resource class needs the resource; a fresh stack has
# no resource yet and no tool shadows, so the search runs only when it exists.
if curl -sf -u "$MP_AUTH" -H 'Content-Type: application/xml' -H 'Accept: application/json' -X POST \
     --data-binary "<q:query xmlns:q=\"http://prism.evolveum.com/xml/ns/public/query-3\"><q:filter><q:inOid><q:value>$RES_OID</q:value></q:inOid></q:filter></q:query>" \
     "$MP/ws/rest/resources/search?options=raw" | jq -e '[.object.object[]?] | length > 0' >/dev/null 2>&1; then
  retire_raw shadows "<q:and><q:ref><q:path>resourceRef</q:path><q:value oid=\"$RES_OID\"/></q:ref><q:equal><q:path>objectClass</q:path><q:value>ri:CustomtoolObjectClass</q:value></q:equal></q:and>" "tool shadows"
fi
retire_raw roles "<q:ref><q:path>archetypeRef</q:path><q:value oid=\"b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c25\"/></q:ref>" "MCT: tool roles"
retire_raw archetypes "<q:inOid><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c25</q:value></q:inOid>" "mcp-tool archetype"
VIEW_ID=$(curl -sf -u "$MP_AUTH" -H 'Accept: application/json' "$MP/ws/rest/systemConfigurations/00000000-0000-0000-0000-000000000001" \
  | jq -r '.. | objects | select(.identifier? == "straza-mcp-tools") | ."@id" // empty' 2>/dev/null | head -1)
if [ -n "$VIEW_ID" ]; then
  printf '%s' "<objectModification xmlns=\"http://midpoint.evolveum.com/xml/ns/public/common/api-types-3\" xmlns:c=\"http://midpoint.evolveum.com/xml/ns/public/common/common-3\" xmlns:t=\"http://prism.evolveum.com/xml/ns/public/types-3\"><itemDelta><t:modificationType>delete</t:modificationType><t:path>c:adminGuiConfiguration/c:objectCollectionViews/c:objectCollectionView</t:path><t:value id=\"$VIEW_ID\"/></itemDelta></objectModification>" > /tmp/retire-tools-view.xml
  patch_mp systemConfigurations/00000000-0000-0000-0000-000000000001 /tmp/retire-tools-view.xml "retired MCP tools collection view removal"
fi

# --- IGA layer: archetypes carry the typing through archetype-sourced weak
# typology defaults via induced focusMappings. No user object template and
# no defaultObjectPolicyConfiguration binding are used.
# The extension schema (objects/schema/straza-extension.xsd) is uploaded as a
# midPoint SchemaType object, with no file mount and no restart: midPoint
# registers SchemaType extensions at boot and on every
# change (repo-common SchemaCache). It must land BEFORE any object that
# carries ext: items, hence first in the IGA layer.
SCHEMA_OID=b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c35
{
  printf '%s\n' '<schema xmlns="http://midpoint.evolveum.com/xml/ns/public/common/common-3" oid="'"$SCHEMA_OID"'">' \
    '    <name>straza-extension</name>' \
    '    <description>Straza extension schema (strazaOrigin/strazaPersona/strazaSponsorRef/... on users, lineage on services and roles), uploaded by the eval seeder from objects/schema/straza-extension.xsd.</description>' \
    '    <lifecycleState>active</lifecycleState>' \
    '    <definition>'
  sed -e '1{/^<?xml/d}' /mp/objects/schema/straza-extension.xsd
  printf '%s\n' '    </definition>' '</schema>'
} > /tmp/straza-extension-schema.xml
import_mp schemas /tmp/straza-extension-schema.xml "Straza extension schema (SchemaType, dynamic)"
# Older object templates (...1c30-1c33), replaced by the persona archetypes,
# are deleted best-effort. 404 = already gone, as on every first boot.
retire_raw objectTemplates "<q:inOid><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c30</q:value><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c31</q:value><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c32</q:value><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c33</q:value></q:inOid>" "older object templates"
for f in /mp/objects/templates/*.xml; do
  import_mp objectTemplates "$f" "object template $(basename "$f" .xml)"
done
for f in /mp/objects/archetypes/*.xml; do
  import_mp archetypes "$f" "archetype $(basename "$f" .xml)"
done
# --- Org tree: only CHILDREN are seeded - the roots (Projects/Teams/World)
# and the "Team" archetype are midPoint built-ins (initial objects, fixed
# OIDs on every install). Before users: their org assignments reference these.
for f in /mp/objects/orgs/*.xml; do
  import_mp orgs "$f" "org $(basename "$f" .xml)"
done
# --- Checked-in roles: the MCP proxy grant (rest-3#proxy is NOT in
# superuser's #all - it must be explicit; the mcp-service technical account
# in objects/users/ references it), the Straza role metarole (…1c0a),
# which MUST land before the resource import (the wire-group inbound
# assigns it to every imported role), AR:Straza:Account (…1c0b), the
# owner of the governed-account construction, and AR:Keycloak:Account
# (…1c0e), the owner of the Keycloak login construction (induced by the
# BR packages only when the Keycloak connector is present, human-gated on
# the access packages). The access packages are NOT here: they are rendered
# later from the .xml.tmpl template (conditional Keycloak inducement).
for f in /mp/objects/roles/*.xml; do
  import_mp roles "$f" "role $(basename "$f" .xml)"
done
# The retired user-template binding is cleared (replace with no value), so a
# re-seed over an older stack does not point defaultObjectPolicyConfiguration
# at the deleted template 1c30. A first boot no-ops here.
cat > /tmp/clear-policy.xml <<'EOF'
<objectModification
        xmlns="http://midpoint.evolveum.com/xml/ns/public/common/api-types-3"
        xmlns:c="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
        xmlns:t="http://prism.evolveum.com/xml/ns/public/types-3">
    <itemDelta>
        <t:modificationType>replace</t:modificationType>
        <t:path>c:defaultObjectPolicyConfiguration</t:path>
    </itemDelta>
</objectModification>
EOF
patch_mp systemConfigurations/00000000-0000-0000-0000-000000000001 \
  /tmp/clear-policy.xml "user-template binding cleared (subtype retirement)"
# GUI collection views (MCP servers, BR Roles, one view per Straza
# role kind and AI agents):
# ADD deltas into adminGuiConfiguration/objectCollectionViews, because a
# REPLACE would destroy the ~50 stock views. midPoint skips a flat view that
# is already there, but a view with nested columns answers 500 on a second
# add (4.10.3: "container value with an id that already exists"), so the patch
# is applied once: a run that finds the views in place leaves them alone, and
# a changed views file lands by down -v and a reseed, like every seed change.
if curl -sf -u "$MP_AUTH" "$MP/ws/rest/systemConfigurations/00000000-0000-0000-0000-000000000001" \
  | body_has '<identifier>straza-application-roles</identifier>'; then
  say "Straza collection views already in place, left alone"
else
  patch_mp systemConfigurations/00000000-0000-0000-0000-000000000001 \
    /mp/objects/system/admin-gui-views.patch.xml "Straza collection views (system configuration)"
fi

# Stock "person-view" removal: it is menu noise beside the Straza user
# views. The container id is a build detail, so it is looked up
# by identifier each run; absent means already removed and the lane skips,
# which keeps re-seeds idempotent.
PV_ID=$(curl -sf -u "$MP_AUTH" \
  "$MP/ws/rest/systemConfigurations/00000000-0000-0000-0000-000000000001" \
  | tr -d '\n' | grep -o '<objectCollectionView id="[0-9]*">[^~]\{0,200\}' \
  | grep '<identifier>person-view</identifier>' | grep -o 'id="[0-9]*"' | head -1 | grep -o '[0-9]*') || true
if [ -n "$PV_ID" ]; then
  cat > /tmp/del-person-view.xml <<EOF
<objectModification
        xmlns="http://midpoint.evolveum.com/xml/ns/public/common/api-types-3"
        xmlns:c="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
        xmlns:t="http://prism.evolveum.com/xml/ns/public/types-3">
    <itemDelta>
        <t:modificationType>delete</t:modificationType>
        <t:path>c:adminGuiConfiguration/c:objectCollectionViews/c:objectCollectionView</t:path>
        <t:value id="$PV_ID"/>
    </itemDelta>
</objectModification>
EOF
  patch_mp systemConfigurations/00000000-0000-0000-0000-000000000001 \
    /tmp/del-person-view.xml "stock person-view removed (id $PV_ID)"
else
  say "stock person-view already absent"
fi

# --- Straza resource: ONE resource on the Straza ConnId bundle. SCIM
# account/group CRUD and the read-only evidence entitlements (apps) plus
# the changes feed all ride the one scoped apiToken. The bundle is REQUIRED:
# it IS the identity resource. apiToken grants: identity:read, apps:read,
# changes:read, config:read. It CANNOT read transcripts, which is the whole
# point of the split. Durable tokens on purpose: async recon/import tasks
# outlive their launch delay, and a 5-minute session token would expire
# under them.
curl -sf -u "$MP_AUTH" -H 'Accept: application/xml' "$MP/ws/rest/connectors" | body_has UniversalRestConnector \
  || fail "universal-rest-connector not discovered in midPoint (uni-connector-build restores it from the public release when the jar is missing; it is the identity resource)"
# Pin check: the JAR midPoint loaded (same bind mount) must be the one
# midpoint/connectors/SHA256SUMS names, so a locally built copy cannot
# silently replace the vendored connector.
if [ -f /mp/connectors/SHA256SUMS ]; then
  (cd /mp/connectors && sha256sum -c SHA256SUMS >/dev/null 2>&1) \
    && say "universal-rest-connector jar matches the pinned SHA256SUMS" \
    || fail "universal-rest-connector jar does NOT match midpoint/connectors/SHA256SUMS (stale or local build): restore the vendored jar (git checkout) or re-pin deliberately, then docker compose restart midpoint"
fi
# midpoint-pull: a stable name for this credential, with the same
# stable-name converge as the SCIM token above.
sed -e "s|__UNI_TOKEN__|$UNI_TOKEN|" \
  /mp/objects/resources/straza-resource.xml > /tmp/resource.xml
import_mp resources /tmp/resource.xml "Straza resource"
test_mp_resource "$RES_OID" "Straza resource"
# Shape assertion: the imported resource must carry
# the 4.10-native association types; a stale file cannot silently regress to
# the legacy <association> blocks.
curl -sf -u "$MP_AUTH" -H 'Accept: application/xml' "$MP/ws/rest/resources/$RES_OID" | body_has "associationType" \
  && say "Straza resource carries the native association types (strazaRoleMembership + strazaAppGrant)" \
  || fail "Straza resource has no associationType: the legacy association shape regressed (check objects/resources/straza-resource.xml)"

# --- Keycloak resource (namespace DISCOVERED from the loaded connector, never guessed) ---
KC_NS=$(curl -sf -u "$MP_AUTH" -H 'Accept: application/xml' "$MP/ws/rest/connectors" \
  | grep -oE 'http://[^<"]*bundle/[^<"]*KeycloakConnector' | head -1)
if [ -n "$KC_NS" ]; then
  say "keycloak connector namespace: $KC_NS"
  sed -e "s|__KC_NAMESPACE__|$KC_NS|" -e "s|__KC_ADMIN_PASSWORD__|$KC_ADMIN_PASSWORD|" \
    /mp/objects/resources/keycloak-resource.xml > /tmp/kc-resource.xml
  import_mp resources /tmp/kc-resource.xml "Keycloak resource"
  test_mp_resource "$KC_RES_OID" "Keycloak resource"
  # The login construction is owned by AR:Keycloak:Account (objects/roles/,
  # imported by the roles glob above), the same layering AR:Straza:Account
  # gives the governed account. Business roles induce that role, never an
  # inline construction. The access-package variant gates the login to human
  # subjects: AI agents authenticate on the Ed25519 key lane and must not
  # grow a Keycloak account (null strazaUserType evaluates false,
  # fail-closed).
  KC_INDUCEMENT="<inducement><targetRef oid=\"$KC_ACCOUNT_ROLE_OID\" type=\"RoleType\"/></inducement>"
  KC_INDUCEMENT_HUMAN="<inducement><targetRef oid=\"$KC_ACCOUNT_ROLE_OID\" type=\"RoleType\"/><condition><source><path>\$focus/extension/ext:strazaUserType</path></source><expression><script><code>strazaUserType == 'human'</code></script></expression></condition></inducement>"
else
  say "WARNING: Keycloak connector not found in midPoint - login provisioning skipped"
  KC_INDUCEMENT=""
  KC_INDUCEMENT_HUMAN=""
fi

# --- LiveSync + initial recon on the merged resource. LiveSync: one task per
# synced class (the connector keeps a sync token per class; SyncOp polls
# Straza's GET /v1/admin/changes feed): evidence apps plus the SCIM
# classes (scim-profile revision 12): `role` events
# drive the wire-group class (a role change or a membership change
# refreshes the render) and `user` events drive the account class
# (membership changes emit the affected user ids). Fixed OIDs =>
# options=overwrite keeps re-seeds idempotent.
# Mirrors /mp/objects/tasks/livesync-tasks.xml (the manual GUI-import lane
# for a running stack): same three tasks, same OIDs; KEEP THE TWO IN SYNC.
# The role tasks (GroupObjectClass) carry NO intent (spec "-"): the resource
# has one role object type, so the sweep covers the whole objectclass and
# every role shadow lands in that one type whatever its kind.
for spec in "account account default 1c1b AccountObjectClass" \
            "app entitlement app 1c11 CustomappObjectClass" \
            "role entitlement - 1c1a GroupObjectClass"; do
  set -- $spec
  cat > /tmp/ls-task.xml <<EOF
<task xmlns="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
      xmlns:ri="http://midpoint.evolveum.com/xml/ns/public/resource/instance-3"
      oid="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a$4">
  <name>Straza LiveSync - $1</name>
  <objectRef oid="$RES_OID" type="ResourceType"/>
  <executionState>runnable</executionState>
  <schedule><interval>10</interval></schedule>
  <activity><work><liveSynchronization><resourceObjects>
    <resourceRef oid="$RES_OID"/>
    <kind>$2</kind>$([ "$3" != "-" ] && printf '<intent>%s</intent>' "$3")
    <objectclass>ri:$5</objectclass>
  </resourceObjects></liveSynchronization></work></activity>
</task>
EOF
  import_mp tasks /tmp/ls-task.xml "LiveSync task ($1)"
done
# Retired tasks, delete best-effort so a re-seed over an older stack does
# not keep polling dead classes: LiveSync user 1c10, evidence group 1c14,
# LiveSync evidence-role 1c12 and recon evidence-role 1c17. The wire-group
# class is THE role projection.
retire_raw tasks "<q:inOid><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c10</q:value><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c12</q:value><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c14</q:value><q:value>b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c17</q:value></q:inOid>" "older tasks"

# --- One-shot initial RECONCILIATION: LiveSync starts from "now", so the
# catalog that existed BEFORE this boot (demo-tools/midpoint apps and the
# seeded roles) needs one full pull to materialize as
# focus objects (apps become MCP server services, roles become
# archetyped roles), and the ACCOUNT class needs one pass to link Straza-side strays and
# mirror kind/origin into the focus extension. Reconciliation (not import) on
# purpose: import skips inbound re-evaluation for already-linked unchanged
# shadows, recon repairs such drift too (verified live on 4.10.3), so
# re-seeds re-reconcile the mirror.
# Mirrors /mp/objects/tasks/initial-recon-tasks.xml (manual GUI-import
# lane): same three tasks, same OIDs; KEEP THE TWO IN SYNC. The role recon
# (1c19) materializes each exported role as a RoleType focus (the inbound
# mapping prepends BR: or AR: by kind; the wire name stays raw).
for spec in "account account default 1c15 AccountObjectClass" \
            "app entitlement app 1c16 CustomappObjectClass" \
            "role entitlement - 1c19 GroupObjectClass"; do
  set -- $spec
  cat > /tmp/recon-task.xml <<EOF
<task xmlns="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
      xmlns:ri="http://midpoint.evolveum.com/xml/ns/public/resource/instance-3"
      oid="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a$4">
  <name>Straza initial recon - $1</name>
  <objectRef oid="$RES_OID" type="ResourceType"/>
  <executionState>runnable</executionState>
  <activity><work><reconciliation><resourceObjects>
    <resourceRef oid="$RES_OID"/>
    <kind>$2</kind>$([ "$3" != "-" ] && printf '<intent>%s</intent>' "$3")
    <objectclass>ri:$5</objectclass>
  </resourceObjects></reconciliation></work></activity>
</task>
EOF
  import_mp tasks /tmp/recon-task.xml "initial recon task ($1)"
done

# --- The access packages: one per Straza business role, rendered from
# objects/roles/role-br-straza-access.xml.tmpl, each inducing the
# imported focus BR:<role> plus, for human holders, the Keycloak login. A
# package deliberately does NOT construct the Straza account: the account
# is owned by AR:Straza:Account, which the seed users hold alongside their
# package, so unassigning a package removes the membership and the login
# while the account persists, and losing every role deprovisions the
# account. The imported focus must exist BEFORE its package imports, because
# its OID is injected as a plain role inducement and the Straza role
# metarole's order-2 construction turns holding it into wire-group
# membership, which IS the (user, role) assignment in Straza. Hence one wait
# per package: a package imported before its focus exists would assign
# nothing.
wait_ar_role() { # $1 AR-prefixed role name; sets AR_OID
  Q="<q:query xmlns:q=\"http://prism.evolveum.com/xml/ns/public/query-3\"><q:filter><q:equal><q:path>name</q:path><q:value>$1</q:value></q:equal></q:filter></q:query>"
  AR_OID=""
  n=0
  while :; do
    AR_OID=$(curl -sf -u "$MP_AUTH" -H 'Content-Type: application/xml' \
      -X POST --data-binary "$Q" "$MP/ws/rest/roles/search" \
      | grep -o 'oid="[0-9a-f-]\{36\}"' | head -1 | cut -d'"' -f2)
    [ -n "$AR_OID" ] && break
    n=$((n + 1))
    [ "$n" -gt 90 ] && fail "imported role $1 did not materialize within 180s - check the role recon (1c19) and role LiveSync (1c1a) tasks, the Straza resource, and that the seeder created the Straza-born role above"
    sleep 2
  done
  say "imported role $1 materialized (oid $AR_OID) ✔"
}
# Every imported role wears the archetype of its kind, which the resource
# assigns with an inbound mapping on ri:roleKind. One focus per kind is
# asserted, so a mapping that stops firing when a focus is born fails the
# seed here and never reaches a certifier.
assert_role_archetype() { # $1 AR-prefixed role name, $2 archetype name
  AQ="<q:query xmlns:q=\"http://prism.evolveum.com/xml/ns/public/query-3\"><q:filter><q:equal><q:path>name</q:path><q:value>$2</q:value></q:equal></q:filter></q:query>"
  WANT=$(curl -sf -u "$MP_AUTH" -H 'Content-Type: application/xml' \
    -X POST --data-binary "$AQ" "$MP/ws/rest/archetypes/search" \
    | grep -o 'oid="[0-9a-f-]\{36\}"' | head -1 | cut -d'"' -f2)
  [ -n "$WANT" ] || fail "archetype $2 is not in midPoint: objects/archetypes/archetype-$2.xml did not import"
  RQ="<q:query xmlns:q=\"http://prism.evolveum.com/xml/ns/public/query-3\"><q:filter><q:equal><q:path>name</q:path><q:value>$1</q:value></q:equal></q:filter></q:query>"
  n=0
  while :; do
    if curl -sf -u "$MP_AUTH" -H 'Content-Type: application/xml' \
      -X POST --data-binary "$RQ" "$MP/ws/rest/roles/search" \
      | body_has "archetypeRef[^>]*oid=\"$WANT\""; then
      break
    fi
    n=$((n + 1))
    [ "$n" -gt 30 ] && fail "imported role $1 does not wear the archetype $2 after 60s. Check the inbound mapping straza-wire-role-archetype on ri:roleKind in objects/resources/straza-resource.xml, and that Straza renders roleKind on the group"
    sleep 2
  done
  say "imported role $1 wears the archetype $2 ✔"
}
access_package() { # $1 business role, $2 package oid, $3 requestable, $4 what the role reaches
  say "waiting for the imported role BR:$1 to materialize (role recon)..."
  wait_ar_role "BR:$1"
  sed -e "s|__ROLE__|$1|g" -e "s|__PKG_OID__|$2|" -e "s|__REQUESTABLE__|$3|" \
      -e "s|__ROLE_OID__|$AR_OID|" -e "s|__REACH__|$4|" \
      -e "s|__KC_INDUCEMENT_HUMAN__|$KC_INDUCEMENT_HUMAN|" \
      /mp/objects/roles/role-br-straza-access.xml.tmpl > /tmp/access-package.xml
  import_mp roles /tmp/access-package.xml "role 'BR:Straza-$1-access'"
}
access_package developer "$DEVELOPER_PKG_OID" true "demo-tools-sandbox, every tool of the demo-tools server, views-demo-tools, the MCP Apps example server, and midpoint-self-service, the midPoint reads plus request_role. Requestable, so the self-service walk can ask for it."
access_package operator "$OPERATOR_PKG_OID" false "demo-tools-readers, the read-only tools the demo-tools server's admin defined, and midpoint-operations, every midPoint tool with every write needing approval. Not requestable: operations rights are assigned, never self-requested."
access_package analyst "$ANALYST_PKG_OID" true "demo-tools-readers and midpoint-self-service, the reads on both servers. Nobody holds it at boot: assign it in the GUI to watch an AI agent land in Straza."

# --- BR:Straza-admin-access: the ADMIN access package. alice holds it in her
# seed XML; midPoint materializes the Straza account (AR:Straza:Account) plus
# AR:straza-admin membership, which IS her straza-admin assignment
# (origin=scim), certifiable in any access review. AR:straza-admin exists on
# the wire because Straza roles render there like every other role
# (scim-profile revision 14).
say "waiting for the imported role AR:straza-admin to materialize (role recon)..."
ADMIN_ROLE_QUERY='<q:query xmlns:q="http://prism.evolveum.com/xml/ns/public/query-3"><q:filter><q:equal><q:path>name</q:path><q:value>AR:straza-admin</q:value></q:equal></q:filter></q:query>'
ADMIN_ROLE_OID=""
n=0
while :; do
  ADMIN_ROLE_OID=$(curl -sf -u "$MP_AUTH" -H 'Content-Type: application/xml' \
    -X POST --data-binary "$ADMIN_ROLE_QUERY" "$MP/ws/rest/roles/search" \
    | grep -o 'oid="[0-9a-f-]\{36\}"' | head -1 | cut -d'"' -f2)
  [ -n "$ADMIN_ROLE_OID" ] && break
  n=$((n + 1))
  [ "$n" -gt 90 ] && fail "imported role AR:straza-admin did not materialize within 180s - check the role recon task (1c19) and that this strazad build renders Straza roles on the SCIM wire (scim-profile rev 14)"
  sleep 2
done
say "imported role AR:straza-admin materialized (oid $ADMIN_ROLE_OID) ✔"
cat > /tmp/admin-role.xml <<EOF
<role xmlns="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
      oid="$ADMIN_PKG_OID">
  <name>BR:Straza-admin-access</name>
  <assignment>
    <!-- br-role archetype: the access packages wear their own family -->
    <targetRef oid="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c27" type="ArchetypeType"/>
  </assignment>
  <description>Access package, the ADMIN one: the Straza account plus
  AR:straza-admin membership (the straza-admin assignment, written by this
  identity manager) plus a Keycloak login via AR:Keycloak:Account. Holding
  this role IS being a Straza admin.
  An access review over it certifies who can administer the platform.
  Deliberately NOT requestable by default: admin is assigned, never
  self-requested.</description>
  <requestable>false</requestable>
  <inducement>
    <targetRef oid="$ACCOUNT_ROLE_OID" type="RoleType"/>
  </inducement>
  <inducement>
    <targetRef oid="$ADMIN_ROLE_OID" type="RoleType"/>
  </inducement>
  $KC_INDUCEMENT
</role>
EOF
import_mp roles /tmp/admin-role.xml "role 'BR:Straza-admin-access'"

# --- BR:Straza-approver-access: the DECIDER access package. sec-approvers is
# an approver-kind role, so holding it carries decide authority and nothing
# else (no tools, no packs, no console areas): it is what a policy's
# approve.roles may name besides straza-admin. Same shape as the access
# packages above (imported role + Keycloak login, the account rides
# AR:Straza:Account on the user), so an access review over this ONE package
# answers "who may approve" for the whole eval stack.
# NOT requestable, mirroring the admin package: decide authority is assigned,
# never self-requested (a requestable approver role would let an agent's
# human make themselves a decider).
say "waiting for the imported role AR:sec-approvers to materialize (role recon)..."
APPROVER_ROLE_QUERY='<q:query xmlns:q="http://prism.evolveum.com/xml/ns/public/query-3"><q:filter><q:equal><q:path>name</q:path><q:value>AR:sec-approvers</q:value></q:equal></q:filter></q:query>'
APPROVER_ROLE_OID=""
n=0
while :; do
  APPROVER_ROLE_OID=$(curl -sf -u "$MP_AUTH" -H 'Content-Type: application/xml' \
    -X POST --data-binary "$APPROVER_ROLE_QUERY" "$MP/ws/rest/roles/search" \
    | grep -o 'oid="[0-9a-f-]\{36\}"' | head -1 | cut -d'"' -f2)
  [ -n "$APPROVER_ROLE_OID" ] && break
  n=$((n + 1))
  [ "$n" -gt 90 ] && fail "imported role AR:sec-approvers did not materialize within 180s - check the role recon task (1c19), the Straza resource, and that this strazad build knows the approver role kind"
  sleep 2
done
say "imported role AR:sec-approvers materialized (oid $APPROVER_ROLE_OID) ✔"

# The decider package also carries the self-enrollment roles: a decider needs
# an approval device, and device-enrollment eligibility is IGA-mastered
# like any entitlement. The straza-enroll-* rows are strazad-born at boot
# (reserved namespace, refused at API create) and import over SCIM like
# straza-admin above; the BR below induces both, so holding the decider
# package IS being allowed to enroll a phone and a browser.
say "waiting for the imported roles AR:straza-enroll-browser / AR:straza-enroll-mobile (role recon)..."
wait_ar_role AR:straza-enroll-browser; ENROLL_BROWSER_OID="$AR_OID"
wait_ar_role AR:straza-enroll-mobile; ENROLL_MOBILE_OID="$AR_OID"

# One imported role per kind, each under its own archetype. The application
# kind is checked further down, once its roles exist on their servers.
assert_role_archetype BR:developer straza-business-role
assert_role_archetype AR:sec-approvers straza-approver-role
assert_role_archetype AR:straza-admin straza-role

cat > /tmp/approver-role.xml <<EOF
<role xmlns="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
      oid="$APPROVER_PKG_OID">
  <name>BR:Straza-approver-access</name>
  <assignment>
    <!-- br-role archetype: the access packages wear their own family -->
    <targetRef oid="b6f9f2c8-0d5a-4e77-9a1e-9d3c0e5a1c27" type="ArchetypeType"/>
  </assignment>
  <description>Access package, the DECIDER one: the imported role
  sec-approvers (membership IS the assignment) plus the self-enrollment
  roles straza-enroll-mobile and straza-enroll-browser (a decider needs an
  approval device, and enrollment eligibility is IGA-mastered) plus a
  Keycloak login via AR:Keycloak:Account, so the holder can sign in, enroll
  a phone or browser, and answer approval requests. Decide authority only: approver-kind roles
  carry no tools, no packs, no console areas. The Straza account itself
  rides AR:Straza:Account. Unassign this role and the person stops being a
  decider while the account stays.</description>
  <requestable>false</requestable>
  <inducement>
    <!-- the imported role: holding it writes wire-group membership -->
    <targetRef oid="$APPROVER_ROLE_OID" type="RoleType"/>
  </inducement>
  <inducement>
    <!-- self-enrollment eligibility, phone -->
    <targetRef oid="$ENROLL_MOBILE_OID" type="RoleType"/>
  </inducement>
  <inducement>
    <!-- self-enrollment eligibility, browser -->
    <targetRef oid="$ENROLL_BROWSER_OID" type="RoleType"/>
  </inducement>
  $KC_INDUCEMENT
</role>
EOF
import_mp roles /tmp/approver-role.xml "role 'BR:Straza-approver-access'"

# --- Checked-in demo identities: the working agents joe-java-developer-agent
# (supervised, the developer role + explicit AR:Keycloak:Account, the harness
# identity) and sam-sre-agent (autonomous, the operator role, drives the
# agent-sam pod), the
# unprovisioned agents nina-data-analyst-agent (assign-a-role demo) and
# pam-personal-agent (carol's personal agent), and the sample population
# carol..judy (employees and one external contractor; carol and dave become
# the delegated MCP admins further down). Fixed OIDs
# (...1c05, ...1c40-1c4e) + options=overwrite keep re-seeds idempotent.
# Must run AFTER the access package imports: joe and sam reference their OIDs.
# Humans BEFORE agents: the agent template derives the sponsor username from
# the sponsor reference at import, so the sponsors must exist first.
for f in /mp/objects/users/user-*.xml /mp/objects/users/agent-*.xml; do
  UNAME=$(grep -o '<name>[^<]*</name>' "$f" | head -1 | sed 's/<[^>]*>//g')
  import_user_convergent "$f" "$UNAME" "user $UNAME" soft || true
done

say "waiting for midPoint to provision $JOE_USER into Straza over SCIM..."
n=0
until [ "$(curl -sf -H "Authorization: Bearer $SCIM_TOKEN" \
  "$WD/scim/v2/Users?filter=userName%20eq%20%22$JOE_USER%22" | jq -r '.totalResults // 0')" = "1" ]; do
  n=$((n + 1))
  [ "$n" -gt 60 ] && fail "waited 120s for midPoint to provision $JOE_USER into Straza over SCIM and no user row arrived. Read the '$JOE_USER' import lines above: a partial error (HTTP 250) that never converged means midPoint's projection raced the import and the retries ran out, so run the seed again (docker compose -f deploy/compose/eval-stack/compose.yaml up eval-seed). A clean import and still no row means the Straza resource or its tasks are stuck: open midPoint and check them"
  sleep 2
done
say "$JOE_USER is in Straza (SCIM) ✔ (born in midPoint, landed everywhere)"

# joe just provisioned holding the imported developer role: his wire-group
# membership IS the (joe, developer) assignment row in Straza (revision 12), no
# derivation layer involved. The servers install first, because each
# application role belongs to the one server it reaches and is created on it
# with its access row. demo-tools-sandbox and views-demo-tools take every tool
# (both whole surfaces are sandbox demos), midpoint-self-service names the
# midPoint reads, the self-service pair and decide_work_item (its rule waits
# for the person behind an agent), and midpoint-operations reaches the FULL
# midPoint surface, so every function can be tried and a mutation costs a
# human tap. The row decides existence, the policy decides the gate: a tool
# an access row reaches with no rule runs, and approve-gated tools stay VISIBLE
# in tools/list. The business roles compose the application roles, so every
# holder reaches the tools through the closure and the sets named <role>-access
# govern every path by matching the application role.
ensure_app demo-tools.yaml
ensure_app midpoint.app.yaml
ensure_app views-demo.yaml
seed_server_role demo-tools-sandbox demo-tools '["*"]' \
  "Every tool of the demo-tools server, tools added later included. Composed by the developer role; get-sum is a hold and get-env is a ticket in demo-tools-sandbox-access."
seed_server_role views-demo-tools views-demo '["*"]' \
  "Every tool of the views-demo server, the MCP Apps example whose get-time opens a clock view. Composed by the developer role."
seed_server_role midpoint-self-service midpoint \
  '["whoami","ping","list_requestable_roles","request_role","decide_work_item","search_*","get_*","list_*"]' \
  "The midPoint reads plus the self-service pair whoami, ping, list_requestable_roles and request_role. Composed by the developer and analyst roles; request_role needs approval in midpoint-self-service-access."
seed_server_role midpoint-operations midpoint '["*"]' \
  "Every tool of the midPoint server. Composed by the operator role; every write waits for the person behind the call in midpoint-operations-access."
ensure_implication developer demo-tools-sandbox
ensure_implication developer views-demo-tools
ensure_implication developer midpoint-self-service
ensure_implication operator midpoint-operations
ensure_implication analyst midpoint-self-service

# --- demo-tools-readers, the demo's narrow server-owned role. It belongs to
# the demo-tools server and reaches the tools of that server that only read
# or compute, named one by one. The name is the server's folded name, a
# hyphen and a suffix, which is the convention that makes a server's prefix
# its own. The seeder assigns it to nobody directly: defining the
# entitlement is the server admin's job and handing it out is the identity
# manager's, so the operator and analyst roles compose it below, sam holds
# it from boot through midPoint and nina gets it with the analyst role.
seed_server_role demo-tools-readers demo-tools \
  '["echo","get-annotated-message","get-env","get-sum"]' \
  "Read-only reach into the demo-tools server: the tools that only read or compute, named one by one. Defined by that server's own admin and assigned by the identity manager."
ensure_implication operator demo-tools-readers
ensure_implication analyst demo-tools-readers

# The application kind's archetype check, which the per-kind checks above
# leave to here because its roles exist only now. It waits for the role
# LiveSync to import the role, and runs only when Straza holds the role,
# because a server that failed to install degrades the demo and never fails
# the seed.
if curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/roles" \
  | jq -e '.[] | select(.name=="demo-tools-sandbox")' >/dev/null 2>&1; then
  wait_ar_role AR:demo-tools-sandbox
  assert_role_archetype AR:demo-tools-sandbox straza-application-role
else
  say "WARNING: demo-tools-sandbox is not in Straza, so the check that an application role wears its archetype in midPoint was skipped. Read the demo-tools install and role lines above"
fi

# The decider (ivan, who rides BR:Straza-approver-access instead of a
# business-role package) provisions in parallel with joe. Report his landing
# but never fail the seed on him: joe and sam are the hard assertions, and
# ivan is supporting cast. ivan and alice, who also carries the decider
# package, are the agent-guardrails deciders for the deploy ticket,
# so a WARNING here is the first thing to read if that gate's role half
# resolves empty.
n=0
while [ "$(curl -sf -H "Authorization: Bearer $SCIM_TOKEN" \
  "$WD/scim/v2/Users?filter=userName%20eq%20%22ivan%22" | jq -r '.totalResults // 0')" != "1" ]; do
  n=$((n + 1))
  if [ "$n" -gt 30 ]; then
    say "WARNING: ivan not in Straza after 60s (provisioning may still be in flight)"
    break
  fi
  sleep 2
done
[ "$n" -le 30 ] && say "ivan is in Straza (SCIM) ✔"

# grace is the midPoint-only population: no business role, no account role,
# so a first boot must not land her in Straza. grace is what an employee with
# no governed access at all looks like. carol and dave are not checked,
# because they become the delegated MCP admins below, which gives them a
# Straza account by design.
# WARNING-grade because a re-seed can still find a deactivated row (Straza
# DELETE deactivates, never purges).
for u in grace; do
  if [ "$(curl -sf -H "Authorization: Bearer $SCIM_TOKEN" \
    "$WD/scim/v2/Users?filter=userName%20eq%20%22$u%22" | jq -r '.totalResults // 0')" = "0" ]; then
    say "$u stayed midPoint-only (no Straza row) ✔"
  else
    say "WARNING: $u has a Straza row. That is expected on a re-seed, because rows deactivate and are never purged. On a first boot it means the seed gave her governed access: check her assignments in midPoint."
  fi
done

# --- sam, the working AI agent: HARD assertions, because the agent-sam
# pod depends on this chain. First the SCIM landing, then the Ed25519 key
# registration (the pod generated the pair at boot and dropped the public
# half in /keys; enroll fails until the key is registered, and the pod's
# ladder retries until then).
say "waiting for midPoint to provision $SAM_USER into Straza over SCIM..."
n=0
until [ "$(curl -sf -H "Authorization: Bearer $SCIM_TOKEN" \
  "$WD/scim/v2/Users?filter=userName%20eq%20%22$SAM_USER%22" | jq -r '.totalResults // 0')" = "1" ]; do
  n=$((n + 1))
  [ "$n" -gt 60 ] && fail "waited 120s for midPoint to provision $SAM_USER into Straza over SCIM and no user row arrived. Read the '$SAM_USER' import lines above: a partial error (HTTP 250) that never converged means midPoint's projection raced the import and the retries ran out, so run the seed again (docker compose -f deploy/compose/eval-stack/compose.yaml up eval-seed). A clean import and still no row means the Straza resource or its tasks are stuck: open midPoint and check them"
  sleep 2
done
say "$SAM_USER is in Straza (SCIM) ✔"

users_jq() { jq -r "(. // []) | (if type==\"array\" then . else (.items // []) end) | $1"; }
register_nhi_key() { # $1 username, $2 pubkey file, $3 wait tries -> 0 registered, 1 no pubkey
  n=0
  until [ -s "$2" ]; do
    n=$((n + 1))
    [ "$n" -gt "$3" ] && return 1
    sleep 2
  done
  NHI_ID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/users?q=$1&limit=10" \
    | users_jq ".[] | select(.username==\"$1\") | .id" | head -1)
  [ -n "$NHI_ID" ] || fail "$1 has no Straza user id yet its SCIM row exists - admin API and SCIM disagree"
  PUB=$(cat "$2")
  curl -sf -X PUT -H "Authorization: Bearer $ST" -H 'Content-Type: application/json' \
    -d "{\"public_key\":\"$PUB\"}" "$WD/v1/admin/users/$NHI_ID/nhi-key" >/dev/null \
    || fail "registering $1's public key failed (PUT /v1/admin/users/$NHI_ID/nhi-key) - key lane enroll cannot work without it"
  say "$1's Ed25519 public key registered ✔ (key-lane enroll unblocked)"
}

register_nhi_key "$SAM_USER" "$KEYS_DIR/$SAM_USER.pub" 60 \
  || fail "no public key at $KEYS_DIR/$SAM_USER.pub after 120s - is the agent-sam pod running? (its entrypoint writes the key at boot)"
# The harness (compose.demo.yaml) may not be up yet; its first `up` re-runs
# this one-shot seeder, which then finds the key and registers it.
register_nhi_key "$JOE_USER" "$KEYS_DIR/$JOE_USER.pub" 15 \
  || say "NOTE: no public key at $KEYS_DIR/$JOE_USER.pub - start the demo overlay (straza-harness generates it) and re-run: docker compose up eval-seed"

# --- Demo policy: without an active PolicySet the enterprise profile denies
# every governed call; a first boot must demo the allow lanes and the
# deny-with-reason out of the box. One set per
# application role, named after the role plus -access as the role page
# writes it, plus agent-guardrails for the hook lane. Seeded only when
# absent: a live edit is never clobbered by a re-run, which is also why a
# changed file needs a wipe to land. The files render VERBATIM on the
# console's policy page, comments included, so their comments read like a
# policy author's notes and seeder mechanics stay HERE.
seed_policy() { # $1 set name; the file is /seed/policies/$1.yaml
  if curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/policies" \
    | jq -e ".[] | select(.name==\"$1\")" >/dev/null 2>&1; then
    say "policy $1 already present - not touching it"
    return 0
  fi
  curl -sf -X PUT -H "Authorization: Bearer $ST" -H 'Content-Type: application/yaml' \
    --data-binary "@/seed/policies/$1.yaml" "$WD/v1/admin/policies" >/dev/null \
    && curl -sf -X POST -H "Authorization: Bearer $ST" -H 'Content-Type: application/json' \
      -d '{"status":"active"}' "$WD/v1/admin/policies/$1/activate" >/dev/null \
    && say "policy $1 seeded + activated" \
    || say "WARNING: policy $1 seed failed - its gates are missing until it is applied by hand"
}
for p in demo-tools-sandbox-access midpoint-self-service-access midpoint-operations-access \
         demo-tools-readers-access agent-guardrails; do
  seed_policy "$p"
done

if [ -n "$KC_NS" ]; then
  say "waiting for $JOE_USER's Keycloak login (his explicit AR:Keycloak:Account grant)..."
  # A real password grant, not an existence check: an existence check passes
  # for a recreated account whose password midPoint skipped (an "unchanged"
  # focus password is not re-pushed to a fresh account), while the login is
  # dead.
  # If the account exists but the grant fails, heal it: set the advertised
  # password over the admin API and verify again. Fresh master token per
  # attempt (its TTL is shorter than this loop's 120s budget). This is also
  # the positive control for the AR:Keycloak:Account chain (the chat face
  # authenticates joe exactly this way).
  n=0
  while :; do
    JOET=$(curl -sf -d "grant_type=password&client_id=straza&username=$JOE_USER&password=Ev4l-St4ck!26&scope=openid" \
      "$KC/realms/straza/protocol/openid-connect/token" | jq -r '.access_token // empty')
    [ -n "$JOET" ] && break
    KCT=$(curl -sf -d "grant_type=password&client_id=admin-cli&username=admin&password=$KC_ADMIN_PASSWORD" \
      "$KC/realms/master/protocol/openid-connect/token" | jq -r '.access_token // empty')
    KID=$(curl -sf -H "Authorization: Bearer $KCT" \
      "$KC/admin/realms/straza/users?username=$JOE_USER&exact=true" | jq -r '.[0].id // empty')
    if [ -n "$KID" ]; then
      curl -sf -X PUT -H "Authorization: Bearer $KCT" -H 'Content-Type: application/json' \
        -d '{"type":"password","value":"Ev4l-St4ck!26","temporary":false}' \
        "$KC/admin/realms/straza/users/$KID/reset-password" >/dev/null 2>&1 \
        && say "$JOE_USER's Keycloak account had no working password - advertised password set over the admin API"
    fi
    n=$((n + 1))
    [ "$n" -gt 60 ] && fail "$JOE_USER cannot log in to Keycloak within 120s (account or password missing) - check the Keycloak resource, his AR:Keycloak:Account assignment, and the seed log above"
    sleep 2
  done
  say "$JOE_USER's Keycloak login VERIFIED by a real password grant ✔"

  # The access packages' Keycloak inducement is human-gated: sam (autonomous,
  # key lane only) must have NO Keycloak account. HARD: sam is a fresh
  # username, so any row here means the human gate on the packages regressed.
  if kc_token; then
    SAM_KID=$(curl -sf -H "Authorization: Bearer $KC_TOKEN" \
      "$KC/admin/realms/straza/users?username=$SAM_USER&exact=true" | jq -r '.[0].id // empty')
    [ -z "$SAM_KID" ] || fail "$SAM_USER has a Keycloak account ($SAM_KID) - the human gate on the access package inducement regressed. AI agents must stay off the login lane"
    say "$SAM_USER has no Keycloak account ✔ (the human gate on the access packages holds, so agents ride the key lane)"
  else
    say "WARNING: could not mint a Keycloak admin token to verify $SAM_USER's login absence"
  fi
fi

# --- Finale: the admin midPoint assigns alice, then the bootstrap demote.
# HARD assert first: alice must be in Straza with straza-admin before the
# transient actor loses anything, so a half-failed seed never strands a
# stack without a working admin (break-glass stays the floor regardless).
# jq lane tolerant of bare-array vs wrapped list answers (users_jq above).
say "waiting for midPoint to provision alice into Straza with straza-admin..."
# First-boot ordering note, root-caused in midPoint source: alice's
# projection can run BEFORE AR:straza-admin is linked to its wire-group
# shadow, in which case associationFromLink evaluates to nothing, silently.
# The FIX is midPoint-native and lives on the Straza role metarole: a
# linked-objects policy rule that, when an imported role's shadow link lands,
# recomputes the role's members WITH reconcile, so midPoint itself replays the
# membership through its own provisioning. This wait only needs to be long
# enough for that recompute task to run (hence 240s, not 120s). The seeder
# adds NO out-of-band convergence of its own.
n=0
while :; do
  ALICE_ID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/users?q=alice&limit=10" \
    | users_jq '.[] | select(.username=="alice") | .id' | head -1)
  if [ -n "$ALICE_ID" ]; then
    ALICE_ROLES=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/users/$ALICE_ID" \
      | jq -r '.effective_roles[]?' 2>/dev/null | tr '\n' ',')
    case ",$ALICE_ROLES" in *,straza-admin,*) break ;; esac
  fi
  n=$((n + 1))
  [ "$n" -gt 120 ] && fail "alice did not land in Straza with straza-admin within 240s - check BR:Straza-admin-access ($ADMIN_PKG_OID), that Straza roles render on the SCIM wire (rev 14), and the metarole's recompute-members-on-link policy rule"
  sleep 2
done
say "alice is in Straza with straza-admin assigned by midPoint ✔ (origin: the identity manager)"

# ivan's decider package now also carries the self-enrollment roles (BR
# inducements above). Same recompute caveat as alice, but WARNING-grade:
# the seed never fails on ivan, and a warning here is the first thing to
# read if a decider's self-enroll answers 403.
n=0
while :; do
  IVAN_ID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/users?q=ivan&limit=10" \
    | users_jq '.[] | select(.username=="ivan") | .id' | head -1)
  if [ -n "$IVAN_ID" ]; then
    IVAN_ROLES=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/users/$IVAN_ID" \
      | jq -r '.effective_roles[]?' 2>/dev/null | tr '\n' ',')
    case ",$IVAN_ROLES" in *,straza-enroll-browser,*) break ;; esac
  fi
  n=$((n + 1))
  if [ "$n" -gt 120 ]; then
    say "WARNING: ivan did not land with straza-enroll-browser within 240s - check BR:Straza-approver-access ($APPROVER_PKG_OID) inducements and the metarole recompute rule; a decider without the role cannot self-enroll a device"
    break
  fi
  sleep 2
done
[ "$n" -le 120 ] && say "ivan holds straza-enroll-browser and mobile from midPoint ✔ (deciders can self-enroll)"

# --- Delegated MCP administration:
# two offices under alice's platform admin, both assigned by this identity
# manager. dave, the platform engineer, holds straza-global-mcp-admin through
# the access package BR:Straza-global-mcp-admin-access and administers every
# MCP server. carol, whose team's agent calls demo-tools, holds the server
# admin role that server got at its registration and administers that one
# server. Neither role is created here: strazad creates the first at boot and
# the demo-tools install created the second, and the seeder only assigns
# them, which is the IGA story.
# These assignments run AFTER the user import loop on purpose, because that
# loop imports the user objects with options=overwrite and would drop an
# assignment added before it on every re-seed. Like ivan above, a strazad
# build without delegated MCP administration has neither role, so every
# step here warns and skips instead of failing the seed.
wait_ar_role_soft() { # $1 AR-prefixed role name, $2 tries; sets AR_OID, returns 1 when it never came
  Q="<q:query xmlns:q=\"http://prism.evolveum.com/xml/ns/public/query-3\"><q:filter><q:equal><q:path>name</q:path><q:value>$1</q:value></q:equal></q:filter></q:query>"
  AR_OID=""
  n=0
  while :; do
    AR_OID=$(curl -sf -u "$MP_AUTH" -H 'Content-Type: application/xml' \
      -X POST --data-binary "$Q" "$MP/ws/rest/roles/search" \
      | grep -o 'oid="[0-9a-f-]\{36\}"' | head -1 | cut -d'"' -f2)
    [ -n "$AR_OID" ] && break
    n=$((n + 1))
    if [ "$n" -gt "$2" ]; then
      say "WARNING: imported role $1 never materialized, so that half of the delegated MCP administration demo is skipped. Check that this strazad build creates straza-global-mcp-admin at boot and one admin role per server, and that the role recon (1c19) and the role LiveSync (1c1a) are running"
      return 1
    fi
    sleep 2
  done
  say "imported role $1 materialized (oid $AR_OID) ✔"
}
assign_mp_role() { # $1 user oid, $2 username, $3 role oid, $4 what the role is
  # Idempotent the way the rest of this seeder is: read the focus first and
  # add the assignment only when the OID is absent, so a re-run converges
  # instead of stacking duplicate assignments.
  if curl -sf -u "$MP_AUTH" -H 'Accept: application/xml' "$MP/ws/rest/users/$1" | body_has "$3"; then
    say "$2 already holds $4"
    return 0
  fi
  cat > /tmp/assign-role.xml <<EOF
<objectModification
    xmlns="http://midpoint.evolveum.com/xml/ns/public/common/api-types-3"
    xmlns:c="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
    xmlns:t="http://prism.evolveum.com/xml/ns/public/types-3">
  <itemDelta>
    <t:modificationType>add</t:modificationType>
    <t:path>c:assignment</t:path>
    <t:value>
      <c:targetRef oid="$3" type="RoleType"/>
    </t:value>
  </itemDelta>
</objectModification>
EOF
  HTTP=$(curl -s -o /tmp/mp-assign.out -w '%{http_code}' -u "$MP_AUTH" -H 'Content-Type: application/xml' \
    -X PATCH --data-binary @/tmp/assign-role.xml "$MP/ws/rest/users/$1")
  case "$HTTP" in
    2*) say "$2 assigned $4 (HTTP $HTTP)" ;;
    *) head -c 400 /tmp/mp-assign.out >&2; echo >&2
       say "WARNING: assigning $4 to $2 answered HTTP $HTTP, so the delegated MCP administration demo is incomplete" ;;
  esac
}
wait_effective_role() { # $1 username, $2 Straza role name, $3 what the role grants
  # Same stale-token hazard as seed_server_role: midPoint provisioning has been
  # running for minutes by now, so re-checkin before reading the admin API.
  ST=$(curl -sf -H 'Content-Type: application/json' \
    -d "{\"device_token\":\"$DT\",\"harness\":{\"name\":\"strazactl\",\"version\":\"eval-seed\"}}" \
    "$WD/v1/checkin" | jq -r '.session_token // empty')
  [ -n "$ST" ] || { say "WARNING: seed re-checkin failed, so $1 holding $2 was not verified"; return 0; }
  n=0
  while :; do
    WHO_ID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/users?q=$1&limit=10" \
      | users_jq ".[] | select(.username==\"$1\") | .id" | head -1)
    if [ -n "$WHO_ID" ]; then
      HELD=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/users/$WHO_ID" \
        | jq -r '.effective_roles[]?' 2>/dev/null | tr '\n' ',')
      case ",$HELD" in *,"$2",*) say "$1 holds $2, assigned by midPoint ✔ ($3)"; return 0 ;; esac
    fi
    n=$((n + 1))
    if [ "$n" -gt 120 ]; then
      say "WARNING: $1 did not land with $2 within 240s - check the business role inducements, the Straza role metarole's recompute-members-on-link rule, and that this strazad build knows the role; without it $1 cannot administer anything"
      return 0
    fi
    sleep 2
  done
}
if wait_ar_role_soft AR:straza-global-mcp-admin 30; then
  sed -e "s|__MCP_ADMIN_ROLE_OID__|$AR_OID|" -e "s|__KC_INDUCEMENT__|$KC_INDUCEMENT|" \
    /mp/objects/roles/role-br-straza-global-mcp-admin-access.xml.tmpl > /tmp/mcp-admin-role.xml
  import_mp roles /tmp/mcp-admin-role.xml "role 'BR:Straza-global-mcp-admin-access'"
  assign_mp_role "$DAVE_OID" dave "$MCP_ADMIN_PKG_OID" "BR:Straza-global-mcp-admin-access"
  wait_effective_role dave straza-global-mcp-admin "every MCP server"
fi
if wait_ar_role_soft AR:mcp-admin-demo-tools 30; then
  # No access package for a per-server office: there is one admin role per
  # server, so a package per server would be an object per server. carol
  # holds the imported role directly, the same lane as nina's assign-a-role
  # demo, plus the two account roles that carry her governed account and her
  # console login.
  DEMO_TOOLS_ADMIN_OID="$AR_OID"
  assign_mp_role "$CAROL_OID" carol "$ACCOUNT_ROLE_OID" "AR:Straza:Account, the governed account"
  if [ -n "$KC_NS" ]; then
    assign_mp_role "$CAROL_OID" carol "$KC_ACCOUNT_ROLE_OID" "AR:Keycloak:Account, the console login"
  fi
  assign_mp_role "$CAROL_OID" carol "$DEMO_TOOLS_ADMIN_OID" "AR:mcp-admin-demo-tools, the demo-tools server alone"
  wait_effective_role carol mcp-admin-demo-tools "the demo-tools server alone"
fi
# alice holds both midPoint application roles directly, so her own assistant
# sessions fall under agent-guardrails and her catalog carries the midPoint
# tools with the approval inbox. Her midPoint writes stay gated by
# midpoint-operations-access.
for ar in midpoint-self-service midpoint-operations; do
  if wait_ar_role_soft "AR:$ar" 30; then
    assign_mp_role "$ALICE_OID" alice "$AR_OID" "AR:$ar, the midPoint tools"
    wait_effective_role alice "$ar" "the midPoint tools"
  fi
done
# The built-in End user role lets a person request requestable roles only
# for themselves. One request-phase grant lets a manager request them for the
# people in an org she manages, so the Get access view's "Request for" a team
# member reaches midPoint's approval. It is relation based, so it reaches no
# one a holder does not manage, and a PATCH keeps the rest of the role as
# midPoint ships it.
END_USER_OID="00000000-0000-0000-0000-000000000008"
if curl -sf -u "$MP_AUTH" -H 'Accept: application/xml' "$MP/ws/rest/roles/$END_USER_OID" | body_has 'manager-request-for-team'; then
  say "End user already lets a manager request roles for their team"
else
  cat > /tmp/end-user-manager.xml <<'EOF'
<objectModification
    xmlns="http://midpoint.evolveum.com/xml/ns/public/common/api-types-3"
    xmlns:c="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
    xmlns:org="http://midpoint.evolveum.com/xml/ns/public/common/org-3"
    xmlns:q="http://prism.evolveum.com/xml/ns/public/query-3"
    xmlns:t="http://prism.evolveum.com/xml/ns/public/types-3">
  <itemDelta>
    <t:modificationType>add</t:modificationType>
    <t:path>c:authorization</t:path>
    <t:value>
      <c:name>manager-request-for-team</c:name>
      <c:action>http://midpoint.evolveum.com/xml/ns/public/security/authorization-model-3#assign</c:action>
      <c:phase>request</c:phase>
      <c:object>
        <c:type>c:UserType</c:type>
        <c:orgRelation>
          <c:subjectRelation>org:manager</c:subjectRelation>
        </c:orgRelation>
      </c:object>
      <c:target>
        <c:type>c:RoleType</c:type>
        <c:filter>
          <q:text>requestable = true</q:text>
        </c:filter>
      </c:target>
    </t:value>
  </itemDelta>
</objectModification>
EOF
  HTTP=$(curl -s -o /tmp/mp-end-user.out -w '%{http_code}' -u "$MP_AUTH" -H 'Content-Type: application/xml' \
    -X PATCH --data-binary @/tmp/end-user-manager.xml "$MP/ws/rest/roles/$END_USER_OID")
  case "$HTTP" in
    2*) say "End user lets a manager request requestable roles for their team (HTTP $HTTP)" ;;
    *) head -c 400 /tmp/mp-end-user.out >&2; echo >&2
       say "WARNING: adding the manager request grant to End user answered HTTP $HTTP, so a manager cannot request a role for their team" ;;
  esac
fi
# Every person holds BOTH device roles directly, whatever their office: each
# enrolls their own phone and browser. Agents never do.
for who in "$ALICE_OID:alice" "$IVAN_OID:ivan" "$CAROL_OID:carol" "$DAVE_OID:dave"; do
  assign_mp_role "${who%%:*}" "${who#*:}" "$ENROLL_MOBILE_OID" "AR:straza-enroll-mobile, the phone enroll right"
  assign_mp_role "${who%%:*}" "${who#*:}" "$ENROLL_BROWSER_OID" "AR:straza-enroll-browser, the browser enroll right"
done
# The business roles: joe and sam landed in Straza above, and these read the closure
# they hold. WARNING-grade like the offices, so a slow recompute never fails
# a seed whose hard assertions already stood.
wait_effective_role "$JOE_USER" developer "the developer role"
wait_effective_role "$SAM_USER" operator "the operator role"

# Demote + lock the transient actor, idempotently: on a first boot it holds
# the admin the bootstrap knob gave it; on re-runs (alice fallback lane) it is already
# locked and these lookups come back empty. Locking also revokes its own
# live session, which is fine: the seed is done writing.
if [ "$BOOT_USER" = "seed-bootstrap" ]; then
  SB_ID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/users?q=seed-bootstrap&limit=10" \
    | users_jq '.[] | select(.username=="seed-bootstrap") | .id' | head -1)
  ADMIN_RID=$(curl -sf -H "Authorization: Bearer $ST" "$WD/v1/admin/roles" \
    | users_jq '.[] | select(.name=="straza-admin") | .id' | head -1)
  if [ -n "$SB_ID" ] && [ -n "$ADMIN_RID" ]; then
    for AID in $(curl -sf -H "Authorization: Bearer $ST" \
      "$WD/v1/admin/assignments?subject_kind=user&subject_id=$SB_ID" \
      | users_jq ".[] | select(.role_id==\"$ADMIN_RID\") | .id"); do
      curl -sf -X DELETE -H "Authorization: Bearer $ST" "$WD/v1/admin/assignments/$AID" >/dev/null \
        && say "seed-bootstrap's bootstrap admin assignment removed (alice's assignment from the identity manager is the admin now)"
    done
    curl -sf -X POST -H "Authorization: Bearer $ST" -H 'Content-Type: application/json' \
      -d '{"reason":"transient bootstrap actor retired: alice holds the admin role the identity manager assigned"}' \
      "$WD/v1/admin/users/$SB_ID/lock" >/dev/null \
      && say "seed-bootstrap locked in Straza (kept, auditable: the one transient local)"
  fi
  if kc_token; then
    SBKID=$(curl -sf -H "Authorization: Bearer $KC_TOKEN" \
      "$KC/admin/realms/straza/users?username=seed-bootstrap&exact=true" | jq -r '.[0].id // empty')
    [ -n "$SBKID" ] && curl -sf -X PUT -H "Authorization: Bearer $KC_TOKEN" -H 'Content-Type: application/json' \
      -d '{"enabled":false}' "$KC/admin/realms/straza/users/$SBKID" >/dev/null 2>&1 \
      && say "seed-bootstrap disabled in Keycloak"
  fi
fi

say "============================================================"
say "EVAL STACK READY - the agent workforce was born in midPoint and"
say "provisioned into Straza (SCIM), zero manual steps: joe (supervised"
say "Java developer agent, the harness identity and chat face, with an"
say "explicit Keycloak login) holds the developer role and sam (autonomous"
say "SRE agent, key lane only) the operator role. Both are sponsored by"
say "alice: Straza sends their gated calls to her phone."
say "Business roles: developer (joe) composes demo-tools-sandbox, views-demo-tools and midpoint-self-service,"
say "operator (sam) composes demo-tools-readers and midpoint-operations, and"
say "analyst (nobody yet) composes demo-tools-readers and midpoint-self-service."
say "Assign BR:Straza-analyst-access to nina-data-analyst-agent in midPoint to"
say "watch an AI agent land in Straza. Each application role's gates"
say "live in <role>-access, as the role page writes them; agent-guardrails"
say "holds the hook lane."
say "IGA showcase: archetypes and templates seeded. The Straza catalog"
say "materializes as MCP server services and one imported"
say "role per Straza role, each under the archetype of its kind"
say "(see https://docs.straza.ai/guides/connect-identity/midpoint/)."
say "AI-agent lane: sam enrolls headless on the Ed25519 key lane in the"
say "agent-sam pod: docker compose logs -f agent-sam"
say "Approval lane: gated calls route to the raising agent's SPONSOR"
say "(alice) by default; the deploy ticket adds the sec-approvers deciders"
say "(ivan and alice, who hold BR:Straza-approver-access). No decider list"
say "names straza-admin (separation of duties). The decider package also"
say "carries straza-enroll-mobile/browser, mastered by the identity manager,"
say "for self-enrolling approval devices. Users without the roles are refused."
say "Delegated MCP admin: dave holds straza-global-mcp-admin (every server) and"
say "carol holds mcp-admin-demo-tools (that server alone), both assigned by midPoint."
say "Server-owned roles: each application role belongs to the server it reaches."
say "demo-tools-readers reaches only the read-only tools of demo-tools. The"
say "operator and analyst roles compose it, so midPoint hands it out and sam"
say "holds it from boot."
say "Landing page: http://localhost:8400  <- start here"
say "Console:  http://localhost:8420/console/   (alice / alice)"
say "midPoint: http://localhost:8087/midpoint   (administrator / \$MP_ADMIN_PASSWORD)"
say "Kill-switch demo: disable sam-sre-agent in midPoint -> auth AND access die."
say "============================================================"
