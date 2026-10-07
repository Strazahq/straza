#!/usr/bin/env sh
# Chart render gate: lint, golden-render the scenario matrix, assert the
# shapes that matter, and prove the guards refuse the silently-broken ones.
# Run from anywhere; UPDATE=1 regenerates goldens (eyeball the diff).
set -eu

CHART="$(cd "$(dirname "$0")/straza" && pwd)"
TESTS="$CHART/tests"
GOLDEN="$TESTS/golden"
mkdir -p "$GOLDEN"

fail() { echo "FAIL: $*" >&2; exit 1; }

helm lint "$CHART" >/dev/null || fail "helm lint"

# --- goldens ---------------------------------------------------------------
# Blank lines are normalized away: helm releases differ on a blank line
# before each document separator, and a golden must pin OUR chart, not
# helm's incidental formatting. YAML is indifferent to them.
for s in turnkey byo standalone apps; do
  out="$(helm template straza "$CHART" -f "$TESTS/values-$s.yaml" | sed '/^[[:space:]]*$/d')" \
    || fail "scenario $s does not render"
  if [ "${UPDATE:-}" = 1 ]; then
    printf '%s\n' "$out" > "$GOLDEN/$s.yaml"
  else
    printf '%s\n' "$out" | diff -u "$GOLDEN/$s.yaml" - \
      || fail "scenario $s drifted from its golden (UPDATE=1 to accept)"
  fi
done

# --- shape assertions ------------------------------------------------------
grep -q 'kind: StatefulSet' "$GOLDEN/turnkey.yaml" \
  || fail "turnkey must bundle NATS + Postgres StatefulSets"
grep -q 'STRAZA_EVENTS_URL' "$GOLDEN/turnkey.yaml" \
  || fail "turnkey must point strazad at the bundled broker"
grep -q 'postgres://straza:$(STRAZA_PG_PASSWORD)@' "$GOLDEN/turnkey.yaml" \
  || fail "turnkey DSN must expand the password from the secret, not inline it"

if grep -qE 'kind: (StatefulSet|Secret)' "$GOLDEN/byo.yaml"; then
  fail "bring-your-own must suppress every bundled backend"
fi
grep -q 'scheme: HTTPS' "$GOLDEN/byo.yaml" \
  || fail "tls.existingSecret must flip the probes to HTTPS"

if grep -qE 'StatefulSet|STRAZA_EVENTS_URL' "$GOLDEN/standalone.yaml"; then
  fail "standalone must stay embedded (no bundle, no events url)"
fi

# strazad needs 30 s to stop in every scenario: requests, then the audit
# spool's drain. A shorter grace kills it before its last records are written.
for s in turnkey byo standalone apps; do
  grep -q 'terminationGracePeriodSeconds: 30' "$GOLDEN/$s.yaml" \
    || fail "scenario $s must give strazad terminationGracePeriodSeconds: 30"
done

# NetworkPolicies: default-on, one per bundled backend, narrowing
# ingress to the strazad pods. Only where a bundle renders: bring-your-own
# backends are outside the cluster and standalone bundles nothing.
[ "$(grep -c 'kind: NetworkPolicy' "$GOLDEN/turnkey.yaml")" = 2 ] \
  || fail "turnkey must render exactly two NetworkPolicies (bundled NATS + Postgres)"
if grep -q 'kind: NetworkPolicy' "$GOLDEN/byo.yaml"; then
  fail "bring-your-own must render no NetworkPolicy (backends live outside the cluster)"
fi
if grep -q 'kind: NetworkPolicy' "$GOLDEN/standalone.yaml"; then
  fail "standalone must render no NetworkPolicy (nothing bundled to guard)"
fi
if helm template straza "$CHART" -f "$TESTS/values-turnkey.yaml" \
  --set networkPolicy.enabled=false | grep -q 'kind: NetworkPolicy'; then
  fail "networkPolicy.enabled=false must suppress both policies"
fi

grep -q 'STRAZA_APPS_DIR' "$GOLDEN/apps.yaml" \
  || fail "apps.manifests must wire STRAZA_APPS_DIR"
grep -q 'checksum/apps' "$GOLDEN/apps.yaml" \
  || fail "apps.manifests must roll pods on manifest change"

# --- publicUrl: never a loopback issuer, file lane respected ---------------
# Without STRAZA_PUBLIC_URL every pod keeps strazad's 127.0.0.1:8420 default
# as its session-token issuer, so enroll and login hand clients a URL
# only the pod itself can dial.
for s in turnkey byo standalone apps; do
  grep -q 'STRAZA_PUBLIC_URL' "$GOLDEN/$s.yaml" \
    || fail "scenario $s must carry STRAZA_PUBLIC_URL (loopback issuer otherwise)"
done
grep -q 'value: "http://straza-straza.default.svc:8420"' "$GOLDEN/standalone.yaml" \
  || fail "derived publicUrl must be the in-cluster Service DNS"
grep -q 'value: "https://straza-straza.default.svc:8420"' "$GOLDEN/byo.yaml" \
  || fail "tls.existingSecret must flip the derived publicUrl to https"
helm template straza "$CHART" -f "$TESTS/values-turnkey.yaml" \
  --set ingress.enabled=true --set ingress.host=straza.example.com --set ingress.tls=true \
  | grep -q 'value: "https://straza.example.com"' \
  || fail "ingress.enabled must derive publicUrl from the ingress host"
printf 'oidc:\n  issuer: https://idp\nconfigYaml: |\n  server:\n    publicUrl: https://straza.example.com\n' > "$GOLDEN/.publicurl-file.yaml"
if helm template straza "$CHART" -f "$GOLDEN/.publicurl-file.yaml" | grep -q 'STRAZA_PUBLIC_URL'; then
  rm -f "$GOLDEN/.publicurl-file.yaml"
  fail "configYaml server.publicUrl must suppress the env injection (env beats file)"
fi
rm -f "$GOLDEN/.publicurl-file.yaml"
# A malformed configYaml (scalar server: key) must not abort the render with
# a raw interface-conversion error: there is no file-lane publicUrl to
# protect, so the derived env is injected, and strazad refuses the
# malformed file at boot anyway, so the injection never governs a pod.
printf 'oidc:\n  issuer: https://idp\nconfigYaml: |\n  server: just-a-string\n' > "$GOLDEN/.publicurl-scalar.yaml"
helm template straza "$CHART" -f "$GOLDEN/.publicurl-scalar.yaml" | grep -q 'STRAZA_PUBLIC_URL' \
  || { rm -f "$GOLDEN/.publicurl-scalar.yaml"; fail "scalar configYaml server: key must render (no suppression; strazad refuses the file at boot)"; }
rm -f "$GOLDEN/.publicurl-scalar.yaml"
# Scheme case: url.Parse lowercases, so HTTP:// boots, and the render-time
# mirror must accept exactly what strazad's checkBaseURL accepts.
helm template straza "$CHART" --set oidc.issuer=https://idp --set publicUrl=HTTP://straza.example.com \
  | grep -q 'value: "HTTP://straza.example.com"' \
  || fail "uppercase-scheme publicUrl must render (strazad accepts it at boot)"

# --- webpush VAPID key custody ---------------------------------------------
# The data dir is an emptyDir: auto-minting the VAPID key there would rotate
# the deployment's push identity on every restart. The knob mounts a Secret
# read-only; unset = no mount at all. The chart also wires
# STRAZA_APPROVAL_PUSH_WEBPUSH_VAPID_KEY_FILE at the mount, because a mount whose
# key strazad never reads is webpush silently off, the exact shape the
# deployment template's header refuses.
helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set webpush.existingSecret=straza-webpush-vapid \
  | grep -q 'mountPath: /etc/straza/webpush' \
  || fail "webpush.existingSecret must mount the key at /etc/straza/webpush"
helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set webpush.existingSecret=straza-webpush-vapid \
  | grep -q 'value: /etc/straza/webpush/vapid.pem' \
  || fail "webpush.existingSecret must wire STRAZA_APPROVAL_PUSH_WEBPUSH_VAPID_KEY_FILE at the mount (0.9.2)"
if helm template straza "$CHART" --set oidc.issuer=https://idp \
  | grep -q 'WEBPUSH_VAPID_KEY_FILE'; then
  fail "no webpush env may render when the knob is unset"
fi
helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set webpush.existingSecret=straza-webpush-vapid \
  | grep -q 'secretName: straza-webpush-vapid' \
  || fail "webpush volume must reference the named secret"
if helm template straza "$CHART" --set oidc.issuer=https://idp \
  | grep -q '/etc/straza/webpush'; then
  fail "no webpush mount may render when the knob is unset"
fi

# --- apns auth key custody -------------------------------------------------
# Same custody shape as webpush, and the same rule: the Secret
# mounts read-only AND the chart wires the key-file env at the mount in one
# knob, because a mounted key strazad's config never reads is the apns lane
# silently off. Companion identifiers ride configYaml; strazad fails boot
# naming them when the key file is set without them, so a forgotten
# configYaml block is loud, not dormant.
helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set apns.existingSecret=straza-apns-key --set pushRelay.enabled=false \
  | grep -q 'mountPath: /etc/straza/apns' \
  || fail "apns.existingSecret must mount the key at /etc/straza/apns"
helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set apns.existingSecret=straza-apns-key --set pushRelay.enabled=false \
  | grep -q 'value: /etc/straza/apns/apns.p8' \
  || fail "apns.existingSecret must wire STRAZA_APPROVAL_PUSH_APNS_KEY_FILE at the mount"
helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set apns.existingSecret=straza-apns-key --set pushRelay.enabled=false \
  | grep -q 'secretName: straza-apns-key' \
  || fail "apns volume must reference the named secret"
if helm template straza "$CHART" --set oidc.issuer=https://idp \
  | grep -qE 'APNS_KEY_FILE|/etc/straza/apns'; then
  fail "no apns env or mount may render when the knob is unset"
fi

# --- generated-password smoke (nondeterministic, no golden) ----------------
helm template straza "$CHART" --set oidc.issuer=https://idp \
  | grep -q 'kind: Secret' \
  || fail "turnkey without passwordSecret must generate the password secret"

# --- guards: these shapes boot healthy and misbehave silently --------------
must_fail() {
  msg="$1"; shift
  if helm template straza "$CHART" "$@" >/dev/null 2>&1; then
    fail "guard missing: $msg"
  fi
}
must_fail "enterprise replicas>1 with embedded per-pod events" \
  --set oidc.issuer=https://idp --set nats.enabled=false
must_fail "enterprise HPA ceiling >1 with embedded per-pod events" \
  --set oidc.issuer=https://idp --set nats.enabled=false \
  --set replicaCount=1 --set autoscaling.enabled=true
must_fail "standalone above one replica (per-pod sqlite)" \
  --set profile=standalone
must_fail "publicUrl without a scheme (strazad refuses it at boot; refuse at render)" \
  --set oidc.issuer=https://idp --set publicUrl=straza.example.com
must_fail "publicUrl with a trailing slash (verbatim token issuer; double-slash discovery endpoints)" \
  --set oidc.issuer=https://idp --set publicUrl=https://straza.example.com/
must_fail "publicUrl with embedded credentials (strazad refuses user:pass@ at boot; refuse at render)" \
  --set oidc.issuer=https://idp --set publicUrl=https://user:pass@straza.example.com

# --- approver listener face ------------------------------------------------
# strazad refuses a half-configured server.approverTLS block at boot
# (all-or-none); the chart refuses the same shapes at render, where the
# message can name the value. No auto-mint lane: emptyDir data dir = a new
# SPKI pin per pod and per restart.
ap_on() {
  helm template straza "$CHART" --set oidc.issuer=https://idp \
    --set approverTLS.enabled=true --set approverTLS.existingSecret=approver-tls \
    "$@"
}
ap_on --set approverTLS.publicUrl=https://approve.example.com \
  | grep -q 'name: approver' \
  || fail "approverTLS.enabled must publish the approver containerPort + Service port"
ap_on --set approverTLS.publicUrl=https://approve.example.com \
  | grep -q 'value: "https://approve.example.com"' \
  || fail "approverTLS.enabled must wire STRAZA_APPROVER_TLS_PUBLIC_URL verbatim"
ap_on --set approverTLS.publicUrl=https://approve.example.com \
  | grep -q 'targetPort: approver' \
  || fail "the Service must route its approver port at the named containerPort"
# Trailing slash is ACCEPTED here, unlike the main publicUrl guard above:
# the enroll QR builder trims it (internal/server/enrollqr.go), while the
# main URL is a verbatim token issuer.
ap_on --set approverTLS.publicUrl=https://approve.example.com/ >/dev/null \
  || fail "approverTLS.publicUrl with a bare trailing slash must render (QR builder trims it)"
if helm template straza "$CHART" --set oidc.issuer=https://idp \
  | grep -q 'APPROVER_TLS\|/etc/straza/approver-tls'; then
  fail "no approverTLS env or mount may render when the face is off"
fi
must_fail "approverTLS without existingSecret (no auto-mint lane; boot refusal)" \
  --set oidc.issuer=https://idp --set approverTLS.enabled=true \
  --set approverTLS.publicUrl=https://approve.example.com
must_fail "approverTLS without publicUrl (the QR needs the URL the phone dials)" \
  --set oidc.issuer=https://idp --set approverTLS.enabled=true \
  --set approverTLS.existingSecret=approver-tls
must_fail "approverTLS with an http publicUrl (approver devices are https+pin only)" \
  --set oidc.issuer=https://idp --set approverTLS.enabled=true \
  --set approverTLS.existingSecret=approver-tls \
  --set approverTLS.publicUrl=http://approve.example.com
must_fail "approverTLS publicUrl with a path (QR consumes scheme+host only)" \
  --set oidc.issuer=https://idp --set approverTLS.enabled=true \
  --set approverTLS.existingSecret=approver-tls \
  --set approverTLS.publicUrl=https://approve.example.com/v1
must_fail "approverTLS.port colliding with service.port (two sockets, one pod)" \
  --set oidc.issuer=https://idp --set approverTLS.enabled=true \
  --set approverTLS.existingSecret=approver-tls \
  --set approverTLS.publicUrl=https://approve.example.com \
  --set approverTLS.port=8420

# --- hosted push relay -----------------------------------------------------
# On by default and visible: every scenario golden carries the relay env
# lines. enabled=false renders none of them, so a relay block in configYaml
# is never clobbered by an env face, and url renders only when set (empty
# means the official relay, resolved by strazad). The relay and a direct
# APNs key both send to iOS phones and strazad refuses that pair at boot;
# the chart refuses it at render, naming the value to flip.
for s in turnkey byo standalone apps; do
  grep -q 'STRAZA_APPROVAL_PUSH_RELAY_ENABLED' "$GOLDEN/$s.yaml" \
    || fail "scenario $s must carry the relay env lines (the relay is on by default, visibly)"
done
if helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set pushRelay.enabled=false | grep -q 'PUSH_RELAY'; then
  fail "pushRelay.enabled=false must render no relay env (a configYaml relay block would be clobbered)"
fi
if helm template straza "$CHART" --set oidc.issuer=https://idp \
  | grep -q 'STRAZA_APPROVAL_PUSH_RELAY_URL'; then
  fail "no relay url env may render while pushRelay.url is empty (strazad resolves the default)"
fi
helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set pushRelay.url=https://relay.example.com \
  | grep -q 'value: "https://relay.example.com"' \
  || fail "pushRelay.url must wire STRAZA_APPROVAL_PUSH_RELAY_URL verbatim"
must_fail "relay on beside apns.existingSecret (two iOS senders; strazad refuses the pair at boot)" \
  --set oidc.issuer=https://idp --set apns.existingSecret=straza-apns-key

# --- demo upstream ---------------------------------------------------------
helm template straza "$CHART" --set oidc.issuer=https://idp \
  --set demoTools.enabled=true \
  | grep -q 'name: straza-straza-demo-tools' \
  || fail "demoTools.enabled must render the demo-tools Deployment+Service"
if helm template straza "$CHART" --set oidc.issuer=https://idp \
  | grep -q 'demo-tools'; then
  fail "no demo-tools object may render when the knob is off"
fi

echo "chart render gate: OK"
