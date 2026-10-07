# lanes/codex.sh: the codex lane, sourced by run.sh (shares WORK/TOOLS/gate/
# overlay_exec/MODE). Gates: floor (codex >= 0.124, below it every gate is
# vacuous); g1-static (the rendered files are the shapes the vendor parser
# accepts, no quoted exe head); g2-spawn and g2-quoted (codex EXECUTES the
# managed hooks with argv and payload intact, bare and quoted exe form);
# g3-output (codex ACCEPTS what `straza hook` prints per key-free event);
# g3-live (a real COMPLETING `codex exec --oss` turn on ollama, where Stop
# exists and every ack is asserted silent); g4-e2e (the same turn against the
# whole product path, asserted from BOTH sides: hook acks and the server's own
# read-back). ANSI colour survival is observed, never gated.
#
# Codex runs only with CODEX_HOME and HOME inside .work/, reads its managed
# /etc/codex/requirements.toml through run.sh's overlay, and is npm-installed
# into .tools/, never globally. The live gates run ollama and a throwaway
# strazad, killed BY PID (a pattern kill hits this script and the real strazad).

CODEX_VERSION="${CODEX_VERSION:-$CODEX_PINNED}"
mkdir -p "$WORK"/{g1,g2,g3,g3live,g4,ansi,quoted,codex-home,oss-cwd} "$WORK/etc-upper/codex"

# ------------------------------------------------------------ codex binary
if [ -n "${CODEX_BIN:-}" ]; then
  CODEX="$CODEX_BIN"
else
  CODEX_PREFIX="$TOOLS/codex-$CODEX_VERSION"
  CODEX="$CODEX_PREFIX/node_modules/.bin/codex"
  if [ ! -x "$CODEX" ] || [ "$CODEX_VERSION" = "latest" ]; then
    echo "harness-matrix: npm-installing @openai/codex@$CODEX_VERSION (local prefix, never global)"
    mkdir -p "$CODEX_PREFIX"
    npm install --prefix "$CODEX_PREFIX" --no-fund --no-audit --loglevel=error "@openai/codex@$CODEX_VERSION"
  fi
fi
CODEX_VERSION_OUT=$("$CODEX" --version 2>/dev/null | tail -1)
note "codex under test: \`$CODEX_VERSION_OUT\` (requested: $CODEX_VERSION)"

gate floor -version "$CODEX_VERSION_OUT"

# The live-model lane's own toolbox (g4boot: free port, admin bootstrap,
# server-side evidence, stdio MCP echo server). Built here rather than in
# run.sh's shared build so adding a lane stays a one-file change.
( cd "$ROOT" && go build -o "$WORK/bin/g4boot" ./test/harness-matrix/g4boot )

# ------------------------------------------------------------------ render
# Managed block: the installer's own generator (see render/main.go). Two
# renders, same code path: g1 carries straza as the hook binary (the
# production shape, statically checked), g2 carries the sentinel (what the
# live gates execute).
"$WORK/bin/render" -out "$WORK/g1/requirements.toml" -bin "$WORK/bin/straza"
"$WORK/bin/render" -out "$WORK/g2/requirements.toml" -bin "$WORK/bin/sentinel"
# User lane: the real binary writes hooks.json + the MCP block, fully
# redirected. Codex will never RUN these here, since user-scope hooks sit
# behind the operator trust gate, which is unforgeable by design.
HOME="$WORK/fakehome" STRAZA_HOME="$WORK/fakehome/.straza" CODEX_HOME="$WORK/codex-home" \
  "$WORK/bin/straza" install codex >/dev/null

gate g1-static -req "$WORK/g1/requirements.toml" -hooks-json "$WORK/codex-home/hooks.json"

# ------------------------------------------------------- live-fire plumbing
# run_codex <requirements.toml> <stderr-file> [extra env VAR=VAL ...]
# One real `codex exec` turn with the given managed file. Exit code is NOT a
# signal (auth dies by design); the sentinel records and codex's stderr are.
run_codex() {
  local req=$1 errfile=$2; shift 2
  rm -rf "$WORK/codex-home"; mkdir -p "$WORK/codex-home"
  install -m 0644 "$req" "$WORK/etc-upper/codex/requirements.toml"
  overlay_exec /dev/null "$errfile" 150 \
    HOME="$WORK/fakehome" CODEX_HOME="$WORK/codex-home" \
    OPENAI_API_KEY=sk-harness-matrix-fake RUST_LOG=codex_hooks=trace "$@" \
    "$CODEX" exec --skip-git-repo-check 'say hi'
}

# run_codex_ansi <codex-home> <stdout> <stderr> <color> [env VAR=VAL ...]
# The ANSI observation's turn: same key-free shape as run_codex, but codex's
# STDOUT is captured and --color is pinned explicitly, because piped stdout makes
# the default `auto` resolve to "never", so the forced run is what says
# whether the sanitizing is the tty heuristic or the renderer itself.
run_codex_ansi() {
  local chome=$1 outfile=$2 errfile=$3 color=$4; shift 4
  rm -rf "$chome"; mkdir -p "$chome"
  install -m 0644 "$WORK/g2/requirements.toml" "$WORK/etc-upper/codex/requirements.toml"
  overlay_exec "$outfile" "$errfile" 150 \
    HOME="$WORK/fakehome" CODEX_HOME="$chome" \
    OPENAI_API_KEY=sk-harness-matrix-fake RUST_LOG=codex_hooks=trace "$@" \
    "$CODEX" exec --skip-git-repo-check --color "$color" 'say hi'
}

# ---------------------------------------------------------- live-model deps
# Knobs (all optional):
#   HARNESS_MATRIX_OSS_TIMEOUT           per-turn wall clock, seconds (900)
#   HARNESS_MATRIX_TOOL_RETRIES          extra turns the attempt ladder may
#                                        burn (2). A completed turn that made
#                                        no tool call does NOT consume one
#                                        unless the tool event is REQUIRED,
#                                        since the defect below is deterministic and
#                                        CPU inference here costs minutes.
#   HARNESS_MATRIX_REQUIRE_TOOL_EVENT=1  harden the attempt into a failure
OSS_TIMEOUT="${HARNESS_MATRIX_OSS_TIMEOUT:-900}"
OSS_ATTEMPTS=$(( 1 + ${HARNESS_MATRIX_TOOL_RETRIES:-2} ))
OLLAMA_BIN="$TOOLS/ollama-$OLLAMA_PINNED/bin/ollama"
OLLAMA_ADDR="${HARNESS_MATRIX_OLLAMA_ADDR:-127.0.0.1:11434}"
OLLAMA_OWN_PID=""   # only an ollama THIS run started is ever killed
STRAZAD_PID=""
G4_USER=g4-agent
LIVE_SKIP=""

# ensure_ollama makes the lane self-sufficient: reuse a reachable server,
# otherwise start the pinned native binary against the lane's own model
# store; then make sure the pinned model is present. Sets LIVE_SKIP with an
# honest reason instead of failing, since a box without the model is not a straza
# regression.
ensure_ollama() {
  if [ ! -x "$OLLAMA_BIN" ]; then
    LIVE_SKIP="no ollama $OLLAMA_PINNED binary at $OLLAMA_BIN (unpack the native tarball into .tools/)"
    return 0
  fi
  if OLLAMA_HOST="$OLLAMA_ADDR" "$OLLAMA_BIN" list >/dev/null 2>&1; then
    echo "harness-matrix: reusing the ollama already serving $OLLAMA_ADDR"
  else
    echo "harness-matrix: starting ollama $OLLAMA_PINNED on $OLLAMA_ADDR"
    OLLAMA_HOST="$OLLAMA_ADDR" OLLAMA_MODELS="$TOOLS/ollama-models" \
      nohup "$OLLAMA_BIN" serve >"$WORK/ollama-serve.log" 2>&1 &
    OLLAMA_OWN_PID=$!
    i=0
    while [ "$i" -lt 60 ]; do
      if OLLAMA_HOST="$OLLAMA_ADDR" "$OLLAMA_BIN" list >/dev/null 2>&1; then break; fi
      sleep 1; i=$((i + 1))
    done
    if ! OLLAMA_HOST="$OLLAMA_ADDR" "$OLLAMA_BIN" list >/dev/null 2>&1; then
      LIVE_SKIP="ollama did not come up on $OLLAMA_ADDR (see $WORK/ollama-serve.log)"
      return 0
    fi
  fi
  if ! OLLAMA_HOST="$OLLAMA_ADDR" "$OLLAMA_BIN" list 2>/dev/null |
       awk 'NR>1 {print $1}' | grep -qx "$CODEX_OSS_MODEL"; then
    echo "harness-matrix: pulling $CODEX_OSS_MODEL into $TOOLS/ollama-models (first run only)"
    if ! OLLAMA_HOST="$OLLAMA_ADDR" OLLAMA_MODELS="$TOOLS/ollama-models" \
         "$OLLAMA_BIN" pull "$CODEX_OSS_MODEL" >>"$WORK/ollama-serve.log" 2>&1; then
      LIVE_SKIP="model $CODEX_OSS_MODEL is absent and could not be pulled (see $WORK/ollama-serve.log)"
    fi
  fi
}

# run_codex_oss <home> <codex-home> <requirements.toml> <stdout> <stderr>
#               <config.toml|-> <prompt> [extra env VAR=VAL ...]
# One real, KEY-FREE, model-completing turn: `codex exec --oss` against the
# local ollama. No OPENAI_API_KEY anywhere: the whole point is that a
# completing turn (and therefore Stop / PreToolUse) is reachable without a
# vendor key. Budget is minutes, not seconds: CPU inference takes about 60 s
# on an idle box and about 6 min under load.
run_codex_oss() {
  local chome_home=$1 chome=$2 req=$3 outfile=$4 errfile=$5 conf=$6 prompt=$7; shift 7
  rm -rf "$chome"; mkdir -p "$chome"
  if [ "$conf" != "-" ]; then install -m 0644 "$conf" "$chome/config.toml"; fi
  install -m 0644 "$req" "$WORK/etc-upper/codex/requirements.toml"
  overlay_exec "$outfile" "$errfile" "$OSS_TIMEOUT" \
    HOME="$chome_home" CODEX_HOME="$chome" RUST_LOG=codex_hooks=trace \
    OLLAMA_HOST="$OLLAMA_ADDR" "$@" \
    "$CODEX" exec --oss --local-provider ollama -m "$CODEX_OSS_MODEL" \
    --skip-git-repo-check --cd "$WORK/oss-cwd" "$prompt"
}

# codex_have_event <records.jsonl> <Event>: did this event reach the hook?
# The payload is a JSON string INSIDE the record, so the quotes are escaped.
codex_have_event() {
  [ -s "$1" ] && grep -Fq "\\\"hook_event_name\\\":\\\"$2\\\"" "$1"
}

# codex_turn_complete <records.jsonl>: did some turn get all the way to the
# end? Stop only exists once the model actually answered.
codex_turn_complete() {
  local ev
  for ev in SessionStart UserPromptSubmit Stop SessionEnd; do
    if ! codex_have_event "$1" "$ev"; then return 1; fi
  done
  return 0
}

# codex_lane_cleanup tears down everything this lane started, by stored PID
# only, and destroys the throwaway deployment's state: the strazad data dir
# (a real store), the enrolled NHI's private key, and the admin token. The
# bootstrap admin password is redacted in place rather than deleted so a failed
# run still has its log.
codex_lane_cleanup() {
  if [ -n "${STRAZAD_PID:-}" ]; then kill "$STRAZAD_PID" 2>/dev/null || true; STRAZAD_PID=""; fi
  if [ -n "${OLLAMA_OWN_PID:-}" ]; then kill "$OLLAMA_OWN_PID" 2>/dev/null || true; OLLAMA_OWN_PID=""; fi
  if [ -f "$WORK/g4/strazad.log" ]; then
    sed -i 's/"password":"[^"]*"/"password":"<redacted by harness-matrix>"/' "$WORK/g4/strazad.log" || true
  fi
  rm -rf "$WORK/g4/data" "$WORK/g4/state" "$WORK/g4/home" 2>/dev/null || true
}
# Chain onto run.sh's own EXIT trap (only the CI sudo mode installs one, and
# it is cleanup_etc) so an aborted run never leaves a strazad behind.
if [ -n "$(trap -p EXIT)" ]; then
  trap 'codex_lane_cleanup; cleanup_etc' EXIT
else
  trap codex_lane_cleanup EXIT
fi

if [ -z "$MODE" ]; then
  note ""
  note "codex live gates SKIPPED: no unprivileged user+mount namespace on this box and the CI sudo fallback is not enabled. g2-spawn / g2-quoted / g3-output / g3-live / g4-e2e need a real codex reading a managed requirements.toml. Static gates above still hold."
else
  # -------------------------------------------------------------- G-2 spawn
  echo "harness-matrix: g2-spawn (real codex executes the managed hooks)"
  run_codex "$WORK/g2/requirements.toml" "$WORK/g2/stderr" SENTINEL_OUT="$WORK/g2/records.jsonl"
  gate g2-spawn -records "$WORK/g2/records.jsonl" -harness-stderr "$WORK/g2/stderr" -hook-bin "$WORK/bin/sentinel"

  # ------------------------------------------------------------- G-2 quoted
  echo "harness-matrix: g2-quoted (quoted-exe tolerance pin)"
  sed -e "s|@SENTINEL@|$WORK/bin/sentinel|" -e "s|@DIR@|$WORK/bin|" \
    "$MATRIX/cases/quoted-command.toml.tmpl" > "$WORK/quoted/requirements.toml"
  run_codex "$WORK/quoted/requirements.toml" "$WORK/quoted/stderr" SENTINEL_OUT="$WORK/quoted/records.jsonl"
  gate g2-quoted -records "$WORK/quoted/records.jsonl" -harness-stderr "$WORK/quoted/stderr" -hook-bin "$WORK/bin/sentinel"

  # ------------------------------------------------------------- G-3 output
  echo "harness-matrix: g3-output (codex accepts the real encoder's stdout)"
  run_codex "$WORK/g2/requirements.toml" "$WORK/g3/stderr" \
    SENTINEL_OUT="$WORK/g3/records.jsonl" \
    SENTINEL_EXEC="$WORK/bin/straza hook --harness codex --conformance-policy $POLICY"
  gate g3-output -records "$WORK/g3/records.jsonl" -harness-stderr "$WORK/g3/stderr"

  # ------------------------------------------------------ ANSI observation
  # Key-free by design: systemMessage renders at SessionStart, which fires
  # long before the auth failure. Two turns, ~seconds each.
  echo "harness-matrix: ANSI observation (does colour survive codex's systemMessage?)"
  for ansi_color in auto always; do
    run_codex_ansi "$WORK/ansi/codex-home-$ansi_color" \
      "$WORK/ansi/$ansi_color.out" "$WORK/ansi/$ansi_color.err" "$ansi_color" \
      SENTINEL_OUT="$WORK/ansi/$ansi_color.records.jsonl" SENTINEL_ANSI=1
    {
      echo "== stdout =="; cat "$WORK/ansi/$ansi_color.out"
      echo "== stderr =="; cat "$WORK/ansi/$ansi_color.err"
    } > "$WORK/ansi/$ansi_color.capture"
  done
  "$WORK/bin/check" -gate ansi-observe \
    -req "$WORK/ansi/auto.capture" -hooks-json "$WORK/ansi/always.capture" \
    > "$WORK/ansi/observation.txt" 2>&1 || true
  note ""
  while IFS= read -r ansi_line; do
    case "$ansi_line" in "- "*|"    - "*) note "$ansi_line" ;; esac
  done < "$WORK/ansi/observation.txt"

  # ------------------------------------------------------- live-model gates
  note ""
  ensure_ollama
  if [ -n "$LIVE_SKIP" ]; then
    note "- g3-live / g4-e2e: **SKIPPED**. $LIVE_SKIP. Every gate above still ran; only the completing-turn events (Stop, PreToolUse) and the server-side path go uncovered."
  else
    # --------------------------------------------------------- G-3 live
    # The MCP echo server is registered as `github`, whose get_* tools the
    # tier-1 conformance policy ALLOWs, so a dispatched call is an ALLOW
    # ack (silence, exit 0) and not a deny.
    echo "harness-matrix: g3-live (real completing codex --oss turn; minutes, CPU inference)"
    sed -e "s|@BIN@|$WORK/bin/g4boot|" -e "s|@NAME@|github|" \
      "$MATRIX/cases/codex-mcp.toml.tmpl" > "$WORK/g3live/config.toml"
    : > "$WORK/g3live/records.jsonl"
    : > "$WORK/g3live/stderr"
    codex_attempt=1
    while [ "$codex_attempt" -le "$OSS_ATTEMPTS" ]; do
      echo "harness-matrix:   g3-live turn $codex_attempt/$OSS_ATTEMPTS (timeout ${OSS_TIMEOUT}s)"
      run_codex_oss "$WORK/fakehome" "$WORK/g3live/codex-home" "$WORK/g2/requirements.toml" \
        "$WORK/g3live/stdout.$codex_attempt" "$WORK/g3live/stderr.$codex_attempt" \
        "$WORK/g3live/config.toml" \
        'Call the tool mcp__github__get_item with name set to "release". Then report the tool output verbatim. Do not run any shell command.' \
        SENTINEL_OUT="$WORK/g3live/records.jsonl" \
        SENTINEL_EXEC="$WORK/bin/straza hook --harness codex --conformance-policy $POLICY"
      cat "$WORK/g3live/stderr.$codex_attempt" >> "$WORK/g3live/stderr"
      if codex_have_event "$WORK/g3live/records.jsonl" PreToolUse; then break; fi
      if codex_turn_complete "$WORK/g3live/records.jsonl" &&
         [ "${HARNESS_MATRIX_REQUIRE_TOOL_EVENT:-}" != "1" ]; then break; fi
      codex_attempt=$((codex_attempt + 1))
    done
    gate g3-live -records "$WORK/g3live/records.jsonl" -harness-stderr "$WORK/g3live/stderr"
    if codex_have_event "$WORK/g3live/records.jsonl" PreToolUse; then
      note "- g3-live: PreToolUse LIVE (mcp): the model dispatched mcp__github__get_item and its ALLOW ack (exit 0, empty stdout) was hard-asserted."
    else
      note "- g3-live: **OBSERVATION: PreToolUse live coverage NOT achieved this run.** The recorded vendor defect held: under \`codex exec --oss\`, ollama's /v1/responses tool calls reach codex's router with an EMPTY function name (\`codex_core::tools::router: error=unsupported call:\`), so no MCP dispatch reaches the hook. Direct /v1/responses probes return the name correctly, so this is vendor drift, not a Straza bug. tool.pre acceptance on codex stays unit-test-covered. Set HARNESS_MATRIX_REQUIRE_TOOL_EVENT=1 to make this a hard failure once the vendor stack is fixed or CI gets gpt-oss."
    fi

    # --------------------------------------------------------- G-4 e2e
    echo "harness-matrix: g4-e2e (full product path: strazad + enrolled NHI + real hook)"
    mkdir -p "$WORK/g4/home/.straza" "$WORK/g4/state"
    g4_ready=0
    if ( cd "$ROOT" && go build -o "$WORK/bin/strazad" ./cmd/strazad ); then
      g4_port=$("$WORK/bin/g4boot" -mode port)
      g4_approver_port=$("$WORK/bin/g4boot" -mode port)
      G4_SERVER="http://127.0.0.1:$g4_port"
      # publicUrl must match the real listener: the built-in issuer
      # advertises it in OIDC discovery and `straza enroll` follows it;
      # with a mismatch the enroll leg dies on a 404.
      # The approver listener gets a free port too; its default :8443 is
      # taken on any box that also runs a real deployment.
      STRAZA_PUBLIC_URL="$G4_SERVER" STRAZA_APPROVER_TLS_LISTEN="127.0.0.1:$g4_approver_port" \
        nohup "$WORK/bin/strazad" serve --profile standalone \
          --data-dir "$WORK/g4/data" --listen "127.0.0.1:$g4_port" \
          >"$WORK/g4/strazad.log" 2>&1 &
      STRAZAD_PID=$!
      if env HOME="$WORK/g4/home" STRAZA_HOME="$WORK/g4/home/.straza" \
           "$WORK/bin/g4boot" -mode bootstrap -server "$G4_SERVER" \
           -log "$WORK/g4/strazad.log" -user "$G4_USER" \
           -straza "$WORK/bin/straza" -state "$WORK/g4/state" -wait 90s \
           >"$WORK/g4/bootstrap.log" 2>&1 &&
         env HOME="$WORK/g4/home" STRAZA_HOME="$WORK/g4/home/.straza" \
           "$WORK/bin/straza" enroll --headless --user "$G4_USER" --server "$G4_SERVER" \
           >>"$WORK/g4/bootstrap.log" 2>&1; then
        g4_ready=1
      fi
    fi
    if [ "$g4_ready" != 1 ]; then
      note "- g4-e2e: **FAIL** (bootstrap never completed; see $WORK/g4/bootstrap.log and $WORK/g4/strazad.log)"
      sed 's/^/    /' "$WORK/g4/bootstrap.log" 2>/dev/null | tail -20 | tee -a "$REPORT"
      FAILED=1
    else
      # The MCP echo server is registered as `straza` here: codex prefixes
      # its tools mcp__straza__*, which normalize.go flags GatewayProxied
      # and LocalPDP.Decide answers with the whole-decision deferral.
      # No --conformance-policy: this is the REAL hook against the REAL
      # enrolled session.
      sed -e "s|@BIN@|$WORK/bin/g4boot|" -e "s|@NAME@|straza|" \
        "$MATRIX/cases/codex-mcp.toml.tmpl" > "$WORK/g4/config.toml"
      : > "$WORK/g4/records.jsonl"
      : > "$WORK/g4/stderr"
      codex_attempt=1
      while [ "$codex_attempt" -le "$OSS_ATTEMPTS" ]; do
        echo "harness-matrix:   g4-e2e turn $codex_attempt/$OSS_ATTEMPTS (timeout ${OSS_TIMEOUT}s)"
        run_codex_oss "$WORK/g4/home" "$WORK/g4/codex-home" "$WORK/g2/requirements.toml" \
          "$WORK/g4/stdout.$codex_attempt" "$WORK/g4/stderr.$codex_attempt" \
          "$WORK/g4/config.toml" \
          'Call the tool mcp__straza__get_item with name set to "release". Then report the tool output verbatim. Do not run any shell command.' \
          STRAZA_HOME="$WORK/g4/home/.straza" \
          SENTINEL_OUT="$WORK/g4/records.jsonl" \
          SENTINEL_EXEC="$WORK/bin/straza hook --harness codex"
        cat "$WORK/g4/stderr.$codex_attempt" >> "$WORK/g4/stderr"
        if codex_have_event "$WORK/g4/records.jsonl" PreToolUse; then break; fi
        if codex_turn_complete "$WORK/g4/records.jsonl" &&
           [ "${HARNESS_MATRIX_REQUIRE_TOOL_EVENT:-}" != "1" ]; then break; fi
        codex_attempt=$((codex_attempt + 1))
      done
      "$WORK/bin/g4boot" -mode evidence -server "$G4_SERVER" -user "$G4_USER" \
        -state "$WORK/g4/state" -out "$WORK/g4/server.json" -wait 60s \
        >>"$WORK/g4/bootstrap.log" 2>&1 || true
      gate g4-e2e -records "$WORK/g4/records.jsonl" -harness-stderr "$WORK/g4/stderr" \
        -req "$WORK/g4/server.json"
      if codex_have_event "$WORK/g4/records.jsonl" PreToolUse; then
        note "- g4-e2e: PreToolUse LIVE (mcp, gateway-proxied): the hook allowed mcp__straza__get_item and the tool.pre audit row's gateway-deferral reason was hard-asserted server-side."
      else
        note "- g4-e2e: **OBSERVATION: gateway-deferral live coverage NOT achieved this run** (same vendor defect as g3-live). The whole-decision deferral stays proven by unit tests and by a hand-fired probe, not by this lane."
      fi
    fi
  fi
fi

codex_lane_cleanup
