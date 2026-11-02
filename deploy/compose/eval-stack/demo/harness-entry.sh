#!/bin/bash
# Governed-harness keeper: the container idles, and the operator drives it
# through docker exec. Enrollment is HEADLESS with an Ed25519 key that the
# harness's straza service (straza-harness-service) makes at first boot and
# keeps in its own container: the entry publishes the public half for the
# seeder and keeps retrying the enroll until the registration lands (start
# the demo overlay, then `docker compose up eval-seed` re-runs the one-shot
# seeder if it ran before this container existed). The Anthropic login uses
# the operator's own subscription and stays a one-time manual step.
# /home/node is a volume, so the hook wiring and the login survive
# recreates, and the service's state volume keeps the key and enrollment.
set -u
STRAZA_SERVER="${STRAZA_SERVER:-http://127.0.0.1:8420}"
JOE_USER=joe-java-developer-agent
KEYS_DIR="${STRAZA_KEYS_DIR:-/keys}"

# Pre-trust the home workspace so claude-code does not ask every evaluator
# to trust /home/node (a governed demo box with a pinned harness; the trust
# question teaches nothing here). Merge-writes so an existing config from
# the persistent volume keeps its login and settings.
node -e '
const fs = require("fs"), p = process.env.HOME + "/.claude.json";
let c = {};
try { c = JSON.parse(fs.readFileSync(p, "utf8")); } catch {}
c.projects = c.projects || {};
const home = c.projects[process.env.HOME] || {};
home.hasTrustDialogAccepted = true;
home.hasCompletedProjectOnboarding = true;
c.projects[process.env.HOME] = home;
fs.writeFileSync(p, JSON.stringify(c, null, 2));
' 2>/dev/null || true

# The service holds joe's key and tokens, so no key or token file may live in
# this container: remove any that an older straza client left in the home
# volume.
rm -f "$HOME/.straza/state/nhi-key.json" "$HOME/.straza/state/identity.json" \
  "$HOME/.straza/state/session.json"

# Without --force keygen succeeds only when the service holds no key for
# joe, so it makes the key at first boot and is refused afterwards. A lost
# /keys volume regenerates with --force, and the re-run seeder registers the
# fresh key.
made=""
if straza keygen --user "$JOE_USER" >/tmp/keygen.log 2>&1; then
  made=yes
elif [ ! -s "$KEYS_DIR/$JOE_USER.pub" ]; then
  if straza keygen --user "$JOE_USER" --force >/tmp/keygen.log 2>&1; then
    made=yes
  else
    echo "[harness] keygen failed:"; cat /tmp/keygen.log
  fi
fi
if [ -n "$made" ]; then
  sed -n 's/^Public key: //p' /tmp/keygen.log > "$KEYS_DIR/$JOE_USER.pub"
  echo "[harness] the service holds joe's key; public half published at $KEYS_DIR/$JOE_USER.pub (the seeder registers it)"
fi

# Enroll headless in the background: retries until the seeder has
# registered the key (no manual step; the log line below flips when done).
(
  until straza enroll --headless --user "$JOE_USER" --server "$STRAZA_SERVER" >/tmp/enroll.log 2>&1; do
    sleep 10
  done
  echo "[harness] enrolled headless as $JOE_USER through the service (key lane; sessions are deviceless and bound to the key)"
) &

cat <<EOF
straza-harness ready (claude-code $(claude --version 2>/dev/null || echo "?"), straza $(straza version 2>/dev/null || echo "?")).

Enrollment is automatic (headless key lane as ${JOE_USER}; watch
this log). One-time ritual for the rest (state persists in the home volume):
  docker exec -it straza-harness bash
  straza install claude-code                # hook wiring into THIS home only
  claude                                    # first run: subscription login

For the midPoint demo (whoami answers as ${JOE_USER}), also once:
  straza connect midpoint                   # open the printed URL through your tunnel, sign in as ${JOE_USER}

Every later session:
  docker exec -it straza-harness claude
EOF

exec sleep infinity
