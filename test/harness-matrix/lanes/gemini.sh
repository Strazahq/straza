# lanes/gemini.sh: the gemini lane, sourced by run.sh (shares WORK/TOOLS/ROOT/
# POLICY/note/gate/overlay_exec/MODE). Gates: gemini-floor (>= 0.26, first
# release with hooksConfig); gemini-static (the REAL installer's settings.json
# files are shapes gemini's loader accepts, the managed one carrying the
# hooksConfig.enabled system pin); gemini-spawn (intact argv and payload keys);
# gemini-output (gemini ACCEPTS what the real `straza hook` prints);
# gemini-control (a hook DENY visibly stops the turn, so the acceptance gate's
# silence is evidence); trust battery (C5 kill switch, C6 system pin, C1 folder
# trust, C4 headless bypass, C7 redirect, C8 relocation) in geminitrust.go.
#
# Gemini has NO config-dir env var, so HOME inside .work/ is the whole
# user-scope isolation story (the test seam is STRAZA_GEMINI_CONFIG_DIR);
# system settings come through run.sh's overlay, and the binary is
# npm-installed into .tools/, never globally. Every gate goes through ggate,
# not gate(): with no per-hook telemetry, the check evidence rows ARE the report.

GEMINI_VERSION="${GEMINI_VERSION:-$GEMINI_PINNED}"
G="$WORK/gemini"
GEMINI_FAKE_KEY=straza-harness-matrix-fake
mkdir -p "$G"

# ggate <label> <gate> <check args...>: gate(), plus the check's stdout folded
# into the drift report. label keeps the report rows distinct when the same
# gate assertion is reused for several claims (gemini-nofire, gemini-fire).
ggate() {
  local label=$1 name=$2; shift 2
  if "$WORK/bin/check" -gate "$name" "$@" >"$G/$name.out" 2>"$G/$name.err"; then
    note "- $label ($name): PASS"
  else
    note "- $label ($name): **FAIL**"
    sed 's/^/    /' "$G/$name.err" | tee -a "$REPORT"
    FAILED=1
  fi
  [ -s "$G/$name.out" ] && sed 's/^/    /' "$G/$name.out" | tee -a "$REPORT"
  return 0
}

# ----------------------------------------------------------- gemini binary
if [ -n "${GEMINI_BIN:-}" ]; then
  GEMINI="$GEMINI_BIN"
else
  GEMINI_PREFIX="$TOOLS/gemini-$GEMINI_VERSION"
  GEMINI="$GEMINI_PREFIX/node_modules/.bin/gemini"
  if [ ! -x "$GEMINI" ] || [ "$GEMINI_VERSION" = "latest" ]; then
    echo "harness-matrix: npm-installing @google/gemini-cli@$GEMINI_VERSION (local prefix, never global)"
    mkdir -p "$GEMINI_PREFIX"
    npm install --prefix "$GEMINI_PREFIX" --no-fund --no-audit --loglevel=error "@google/gemini-cli@$GEMINI_VERSION"
  fi
fi
GEMINI_VERSION_OUT=$("$GEMINI" --version 2>/dev/null | tail -1)
note "gemini under test: \`$GEMINI_VERSION_OUT\` (requested: $GEMINI_VERSION)"

# Lane-owned tool: renders a gemini settings.json through the installer's own
# exported writers (see render-gemini/main.go). Built here, not in run.sh, so
# adding a lane stays one new file.
( cd "$ROOT" && go build -o "$WORK/bin/render-gemini" ./test/harness-matrix/render-gemini )
# A second sentinel under a different name makes hook records attributable to
# the SCOPE that registered them (C6 needs "the SYSTEM file put governance
# back on", not merely "something ran").
cp "$WORK/bin/sentinel" "$WORK/bin/sentinel-system"

ggate "version floor" gemini-floor -version "$GEMINI_VERSION_OUT"

# ------------------------------------------------------------------ static
# User scope: the real `straza install gemini` (hooks + the MCP registration,
# which for gemini lands in the SAME settings.json), fully redirected.
# Managed scope: the installer's own managed writer via render-gemini. The
# full `straza install --managed` additionally stages a binary copy and needs a
# reachable strazad for snapshot keys, which a file-shape gate does not.
mkdir -p "$G/static/home"
HOME="$G/static/home" STRAZA_HOME="$G/static/home/.straza" \
  "$WORK/bin/straza" install gemini >/dev/null 2>"$G/static/install.err"
"$WORK/bin/render-gemini" -managed -out "$G/static/managed/settings.json" \
  -bin "$WORK/bin/straza" >/dev/null 2>&1
ggate "installer file shapes" gemini-static \
  -req "$G/static/home/.gemini/settings.json" \
  -hooks-json "$G/static/managed/settings.json"

# ------------------------------------------------------- live-fire plumbing
# gemini_case <case> <seed-json> <hook-bin>: a fresh HOME + cwd whose user
# settings.json the REAL installer wrote over the given seed (the seed takes
# the same merge path an operator's own settings would).
gemini_case() {
  local c=$1 seed=$2 bin=$3
  rm -rf "${G:?}/$c"
  mkdir -p "$G/$c/home" "$G/$c/work"
  "$WORK/bin/render-gemini" -out "$G/$c/home/.gemini/settings.json" -bin "$bin" -seed "$seed" \
    >"$G/$c/render.out" 2>&1
}

# gemini_run <case> <extra-gemini-args|""> [VAR=VAL ...]: one real key-free
# turn. Exit code is NOT a signal (auth dies by design); the sentinel records
# and gemini's own stderr are. User-scope only: no /etc is involved, so these
# run on every box, MODE or not.
gemini_run() {
  local c=$1 extra=$2; shift 2
  # shellcheck disable=SC2086 -- $extra is deliberately word-split; its only
  # values are single flags chosen by this file.
  timeout 120 env -C "$G/$c/work" \
    HOME="$G/$c/home" STRAZA_HOME="$G/$c/home/.straza" \
    GEMINI_API_KEY="$GEMINI_FAKE_KEY" SENTINEL_OUT="$G/$c/records.jsonl" "$@" \
    "$GEMINI" -p 'say hi' $extra </dev/null >"$G/$c/stdout" 2>"$G/$c/stderr" || true
}

# gemini_run_etc: same, with $WORK/etc-upper presented over /etc (run.sh's
# overlay machinery). MODE-gated by the caller.
gemini_run_etc() {
  local c=$1 extra=$2; shift 2
  # shellcheck disable=SC2086 -- see gemini_run
  overlay_exec "$G/$c/stdout" "$G/$c/stderr" 120 \
    -C "$G/$c/work" \
    HOME="$G/$c/home" STRAZA_HOME="$G/$c/home/.straza" \
    GEMINI_API_KEY="$GEMINI_FAKE_KEY" SENTINEL_OUT="$G/$c/records.jsonl" "$@" \
    "$GEMINI" -p 'say hi' $extra
}

AUTH_SEED='{"selectedAuthType":"gemini-api-key"}'
KILL_SEED='{"selectedAuthType":"gemini-api-key","hooksConfig":{"enabled":false}}'
TRUST_SEED='{"selectedAuthType":"gemini-api-key","security":{"folderTrust":{"enabled":true}}}'

# ------------------------------------------------------------------- spawn
echo "harness-matrix: gemini-spawn (real gemini executes the installer's wiring)"
gemini_case spawn "$AUTH_SEED" "$WORK/bin/sentinel"
gemini_run spawn --skip-trust
ggate "hooks execute + payload keys" gemini-spawn \
  -records "$G/spawn/records.jsonl" -hook-bin "$WORK/bin/sentinel"

# ------------------------------------------------------------------ output
echo "harness-matrix: gemini-output (gemini accepts the real encoder's stdout)"
gemini_case output "$AUTH_SEED" "$WORK/bin/sentinel"
gemini_run output --skip-trust \
  "SENTINEL_EXEC=$WORK/bin/straza hook --harness gemini --conformance-policy $POLICY"
ggate "gemini accepts straza's ack" gemini-output \
  -records "$G/output/records.jsonl" -harness-stderr "$G/output/stderr"

# Positive control: gemini must visibly honor a hook DECISION, or the gate
# above is asserting silence from a channel nobody reads.
cat > "$G/deny-hook.sh" <<'EOF'
#!/bin/sh
# harness-matrix positive control: a hook that always denies.
cat >/dev/null
printf '%s' '{"decision":"deny","reason":"harness-matrix positive control"}'
EOF
chmod +x "$G/deny-hook.sh"
gemini_case control "$AUTH_SEED" "$WORK/bin/sentinel"
gemini_run control --skip-trust "SENTINEL_EXEC=$G/deny-hook.sh"
ggate "positive control: deny stops the turn" gemini-control -harness-stderr "$G/control/stderr"

# ------------------------------------------------------------- live gates
# A COMPLETING turn with NO key: geministub serves the model traffic behind
# GOOGLE_GEMINI_BASE_URL (method + auth-type edge recorded in
# adapters/gemini.yaml: the env var flips gemini's auth
# type, so the seed pins security.auth.selectedType). This unlocks the events
# every fake-key case above is blind to: AfterAgent (gemini's ONLY reply
# lane) and BeforeTool. The 0.53 model router's responseJsonSchema classifier
# is schema-answered by the stub or the turn stalls in router retries;
# list_directory's 0.53 param is dir_path (wrong name = CLI validator kills
# the call BEFORE the hook). Both edges come from this lane's bring-up probes.
( cd "$ROOT" && go build -o "$WORK/bin/geministub" ./test/harness-matrix/geministub )
LIVE_SEED='{"selectedAuthType":"gemini-api-key","security":{"auth":{"selectedType":"gemini-api-key"}}}'
LIVE_REPLY="straza harness-matrix live reply"

# gemini_live <case> <hook-bin> <sentinel-exec|""> [stub flags...]: stub up,
# one real turn against it, stub down (kill by recorded PID only).
gemini_live() {
  local c=$1 bin=$2 exec_relay=$3; shift 3
  gemini_case "$c" "$LIVE_SEED" "$bin"
  "$WORK/bin/geministub" -addr-file "$G/$c/stub.addr" -log "$G/$c/stub.jsonl" "$@" \
    2>"$G/$c/stub.err" &
  local stubpid=$! i=0
  while [ ! -s "$G/$c/stub.addr" ] && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
  if [ -n "$exec_relay" ]; then
    gemini_run "$c" --skip-trust \
      "GOOGLE_GEMINI_BASE_URL=http://$(cat "$G/$c/stub.addr")" "SENTINEL_EXEC=$exec_relay"
  else
    gemini_run "$c" --skip-trust "GOOGLE_GEMINI_BASE_URL=http://$(cat "$G/$c/stub.addr")"
  fi
  kill "$stubpid" 2>/dev/null || true
  wait "$stubpid" 2>/dev/null || true
}

echo "harness-matrix: gemini-live-reply (completing turn, AfterAgent reply lane)"
gemini_live live-reply "$WORK/bin/sentinel" "" -reply "$LIVE_REPLY"
ggate "live turn: AfterAgent carries the scripted reply" gemini-live-reply \
  -records "$G/live-reply/records.jsonl" -hook-bin "$WORK/bin/sentinel" -want "$LIVE_REPLY"

echo "harness-matrix: gemini-live-tool (completing tool round-trip, BeforeTool)"
gemini_live live-tool "$WORK/bin/sentinel" "" \
  -reply "$LIVE_REPLY" -tool list_directory -tool-args '{"dir_path":"."}'
ggate "live turn: BeforeTool fires with tool_name/tool_input" gemini-live-tool \
  -records "$G/live-tool/records.jsonl" -hook-bin "$WORK/bin/sentinel" -want "$LIVE_REPLY"

# Ack acceptance on the events the fake-key output case cannot reach: the
# REAL `straza hook` decides all four events of a completing tool turn (the
# tier1 policy allows list_directory, verified before this case was wired)
# and gemini accepts every ack without a complaint, turn still completing.
echo "harness-matrix: gemini-live-output (real hook decides a completing turn)"
gemini_live live-output "$WORK/bin/sentinel" \
  "$WORK/bin/straza hook --harness gemini --conformance-policy $POLICY" \
  -reply "$LIVE_REPLY" -tool list_directory -tool-args '{"dir_path":"."}'
ggate "live turn: gemini accepts straza's ack on all four events" gemini-output \
  -records "$G/live-output/records.jsonl" -harness-stderr "$G/live-output/stderr"
ggate "live turn under the real hook still completes all four events" gemini-live-tool \
  -records "$G/live-output/records.jsonl" -hook-bin "$WORK/bin/sentinel" -want "$LIVE_REPLY"

# --------------------------------------------------------- trust battery
note ""
note "### gemini trust battery: live verdicts on geminitrust.go's claims"

# C5, the kill switch. hooksConfig.enabled=false in USER settings, no system
# file anywhere. Doubles as C6's inverse control.
if [ -e /etc/gemini-cli/settings.json ]; then
  note "- C5/C4/C1 (no-system-file cases): **SKIPPED**. This box has a real /etc/gemini-cli/settings.json, which would merge into every run; the lane refuses to read a live managed config."
else
  echo "harness-matrix: gemini C5 (kill switch)"
  gemini_case c5 "$KILL_SEED" "$WORK/bin/sentinel"
  gemini_run c5 --skip-trust
  ggate "C5 kill switch: hooksConfig.enabled=false runs NO hooks" gemini-nofire -records "$G/c5/records.jsonl"

  # C1, folder trust. Feature on, cwd with no trust record, no bypass.
  echo "harness-matrix: gemini C1 (untrusted folder)"
  gemini_case c1 "$TRUST_SEED" "$WORK/bin/sentinel"
  gemini_run c1 ""
  ggate "C1 folder trust: untrusted cwd runs no hooks (headless: refuses to start)" gemini-untrusted \
    -records "$G/c1/records.jsonl" -harness-stderr "$G/c1/stderr"

  # C1-store, the same feature with a TRUST_FOLDER record: proves the
  # trustedFolders.json shape geminitrust.go's readGeminiFolderTrust parses is
  # the one gemini itself honors, and that trust alone restores the lane.
  echo "harness-matrix: gemini C1-store (TRUST_FOLDER record)"
  gemini_case c1store "$TRUST_SEED" "$WORK/bin/sentinel"
  printf '{"%s":"TRUST_FOLDER"}\n' "$G/c1store/work" > "$G/c1store/home/.gemini/trustedFolders.json"
  gemini_run c1store ""
  ggate "C1 control: a TRUST_FOLDER record restores the hooks" gemini-fire \
    -records "$G/c1store/records.jsonl" -hook-bin "$WORK/bin/sentinel"

  # C4, the documented headless bypass.
  echo "harness-matrix: gemini C4 (GEMINI_CLI_TRUST_WORKSPACE)"
  gemini_case c4 "$TRUST_SEED" "$WORK/bin/sentinel"
  gemini_run c4 "" GEMINI_CLI_TRUST_WORKSPACE=true
  ggate "C4 headless bypass: GEMINI_CLI_TRUST_WORKSPACE=true restores the hooks" gemini-fire \
    -records "$G/c4/records.jsonl" -hook-bin "$WORK/bin/sentinel"

  # C8, the vendor-documented trust-store relocation, honored by doctor's
  # geminiTrustStorePath: a TRUST_FOLDER record in a
  # RELOCATED store must restore the lane exactly like C1-store's default
  # path, or doctor is reading trust where gemini does not.
  echo "harness-matrix: gemini C8 (GEMINI_CLI_TRUSTED_FOLDERS_PATH relocation)"
  gemini_case c8 "$TRUST_SEED" "$WORK/bin/sentinel"
  printf '{"%s":"TRUST_FOLDER"}\n' "$G/c8/work" > "$G/c8/relocated-trust.json"
  gemini_run c8 "" "GEMINI_CLI_TRUSTED_FOLDERS_PATH=$G/c8/relocated-trust.json"
  ggate "C8 relocated store: a TRUST_FOLDER record via GEMINI_CLI_TRUSTED_FOLDERS_PATH restores the hooks" gemini-fire \
    -records "$G/c8/records.jsonl" -hook-bin "$WORK/bin/sentinel"
fi

# C6 / C7 need the system settings file at its real path, via run.sh's overlay.
# Run LAST: the CI sudo fallback stages etc-upper into the runner's real /etc,
# which would then be visible to the no-system-file cases above.
if [ -z "$MODE" ]; then
  note ""
  note "- C6 (system pin) / C7 (system-settings redirect): **SKIPPED**. No unprivileged user+mount namespace on this box and the CI sudo fallback is not enabled; both need a real gemini reading /etc/gemini-cli/settings.json. Every gate above still ran."
else
  mkdir -p "$WORK/etc-upper/gemini-cli"
  "$WORK/bin/render-gemini" -managed -out "$WORK/etc-upper/gemini-cli/settings.json" \
    -bin "$WORK/bin/sentinel-system" >/dev/null 2>&1

  # C6, the reason for the system pin: user says off,
  # system says on, gemini's precedence puts SYSTEM above user.
  echo "harness-matrix: gemini C6 (system pin beats the user kill switch)"
  gemini_case c6 "$KILL_SEED" "$WORK/bin/sentinel"
  gemini_run_etc c6 --skip-trust
  ggate "C6 system pin: /etc/gemini-cli hooksConfig.enabled=true beats user false" gemini-sysfire \
    -records "$G/c6/records.jsonl" -hook-bin "$WORK/bin/sentinel-system"

  # C7, the vendor-documented bypass: the same deployment, plus a user-set
  # GEMINI_CLI_SYSTEM_SETTINGS_PATH pointing away from the managed file. If
  # the managed pin stops applying, the redirect is honored and the managed
  # lane is abandoned for that session, exactly what doctor warns about.
  echo "harness-matrix: gemini C7 (system-settings redirect bypass)"
  gemini_case c7 "$KILL_SEED" "$WORK/bin/sentinel"
  printf '{}\n' > "$G/c7/decoy-system-settings.json"
  gemini_run_etc c7 --skip-trust \
    "GEMINI_CLI_SYSTEM_SETTINGS_PATH=$G/c7/decoy-system-settings.json"
  ggate "C7 redirect: GEMINI_CLI_SYSTEM_SETTINGS_PATH abandons the managed pin" gemini-nofire \
    -records "$G/c7/records.jsonl"
fi
