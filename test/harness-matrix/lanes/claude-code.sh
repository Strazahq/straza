# lanes/claude-code.sh: the claude-code lane, sourced by run.sh (shares
# ROOT/WORK/TOOLS/note/gate). Gates: claude-floor, a real claude
# 2.x; claude-static, the installer's files are the shapes claude reads;
# claude-spawn, claude EXECUTES them with intact argv and the right
# hook_event_name; claude-output, claude ACCEPTS what `straza hook` prints.
# Key-free by construction: a FAKE ANTHROPIC_API_KEY opens a real session
# whose SessionStart, UserPromptSubmit and SessionEnd hooks fire before the
# auth failure ends the turn. Stop, PreToolUse and the Subagent pair need a
# completing model turn, so they are key-gated, not failures.
#
# ISOLATION, non-negotiable: every claude and every `straza install` here runs
# with HOME and CLAUDE_CONFIG_DIR inside .work/ and EVERY CLAUDE*/ANTHROPIC*
# variable unset, because a run inside a Claude Code session carries markers
# that make the child believe it is nested. The box's real config is never
# touched, and the binary is npm-installed into .tools/, never globally.

CLAUDE_VERSION="${CLAUDE_VERSION:-$CLAUDE_PINNED}"
CC="$WORK/claude-code"
mkdir -p "$CC"

# render-claude is this lane's own tool (run.sh builds the shared ones).
( cd "$ROOT" && go build -o "$WORK/bin/render-claude" ./test/harness-matrix/render-claude )

# ------------------------------------------------------------ claude binary
if [ -n "${CLAUDE_BIN:-}" ]; then
  CLAUDE="$CLAUDE_BIN"
else
  CLAUDE_PREFIX="$TOOLS/claude-$CLAUDE_VERSION"
  CLAUDE="$CLAUDE_PREFIX/node_modules/.bin/claude"
  if [ ! -x "$CLAUDE" ] || [ "$CLAUDE_VERSION" = "latest" ]; then
    echo "harness-matrix: npm-installing @anthropic-ai/claude-code@$CLAUDE_VERSION (local prefix, never global)"
    mkdir -p "$CLAUDE_PREFIX"
    npm install --prefix "$CLAUDE_PREFIX" --no-fund --no-audit --loglevel=error "@anthropic-ai/claude-code@$CLAUDE_VERSION"
  fi
fi

# cc_unset: -u flags stripping every CLAUDE*/ANTHROPIC* variable this process
# carries, so a session marker added by a future claude release cannot leak
# into the child on its own. The three variables the lane controls are set
# explicitly after it.
cc_unset=""
for v in $(env | sed -n 's/^\(CLAUDE[A-Z_]*\|ANTHROPIC_[A-Z_]*\)=.*/\1/p'); do
  cc_unset="$cc_unset -u $v"
done

# cc_isolated <homedir> <configdir> <command...>: run anything with the box's
# real claude/anthropic environment stripped and both config roots redirected.
# shellcheck disable=SC2086 # cc_unset is a deliberate list of -u flags
cc_isolated() {
  local home=$1 cfg=$2; shift 2
  env $cc_unset HOME="$home" CLAUDE_CONFIG_DIR="$cfg" STRAZA_HOME="$home/.straza" \
    ANTHROPIC_API_KEY=sk-harness-matrix-fake "$@"
}

mkdir -p "$CC/version/home" "$CC/version/cfg"
CLAUDE_VERSION_OUT=$(cc_isolated "$CC/version/home" "$CC/version/cfg" "$CLAUDE" --version 2>/dev/null | tail -1)
note "claude-code under test: \`$CLAUDE_VERSION_OUT\` (requested: $CLAUDE_VERSION)"
if [ -e /etc/claude-code/managed-settings.json ]; then
  note "NOTE: this box carries /etc/claude-code/managed-settings.json; its hooks merge into every run below."
fi

gate claude-floor -version "$CLAUDE_VERSION_OUT"

# ------------------------------------------------------------------- static
# The real binary writes the user-scope files, fully redirected: settings.json
# (hooks) and .claude.json (MCP registration). Their PATHS are part of what is
# under test: the lane names only CLAUDE_CONFIG_DIR and lets SettingsPath /
# MCPConfigPath choose, so a resolution change shows up as an unreadable file.
rm -rf "$CC/static"; mkdir -p "$CC/static/cfg" "$CC/static/home"
cc_isolated "$CC/static/home" "$CC/static/cfg" \
  "$WORK/bin/straza" install claude-code > "$CC/static/install.log" 2>&1

gate claude-static -req "$CC/static/cfg/settings.json" -hooks-json "$CC/static/cfg/.claude.json"

# ------------------------------------------------------- live-fire plumbing
# fire_claude <dir> [extra env VAR=VAL ...]: one real `claude -p` turn against
# the wiring in <dir>/cfg, from an EMPTY project dir, because a
# .claude/settings.json in the CWD would merge in. Exit code is NOT a signal
# (auth dies by design); the sentinel records and claude's own telemetry are.
#
# --debug is what makes that telemetry exist: claude writes per-hook accept /
# reject lines to $CLAUDE_CONFIG_DIR/debug/<session>.txt and nowhere else (the
# `latest` copy is this run's, the config dir being fresh). Hook execution
# itself is unchanged by the flag. The capture the gates read is claude's
# stderr followed by that log.
# shellcheck disable=SC2086 # cc_unset is a deliberate list of -u flags
fire_claude() {
  local dir=$1; shift
  mkdir -p "$dir/cfg" "$dir/home" "$dir/proj"
  ( cd "$dir/proj" && timeout 120 env $cc_unset \
      HOME="$dir/home" CLAUDE_CONFIG_DIR="$dir/cfg" STRAZA_HOME="$dir/home/.straza" \
      ANTHROPIC_API_KEY=sk-harness-matrix-fake "$@" \
      "$CLAUDE" --debug -p 'say hi' </dev/null >"$dir/stdout" 2>"$dir/stderr" ) || true
  cat "$dir/stderr" "$dir/cfg/debug/latest" > "$dir/telemetry.txt" 2>/dev/null || true
}

# --------------------------------------------------------------- G-2 spawn
# The config under test is the installer's real shape with the sentinel as the
# hook binary, rendered through the installer's own writer (render-claude), so
# it tracks install.go and not a copy of it.
echo "harness-matrix: claude-spawn (real claude executes the installed hooks)"
rm -rf "$CC/spawn"; mkdir -p "$CC/spawn/cfg"
"$WORK/bin/render-claude" -out "$CC/spawn/cfg/settings.json" -bin "$WORK/bin/sentinel" >/dev/null
fire_claude "$CC/spawn" SENTINEL_OUT="$CC/spawn/records.jsonl"
gate claude-spawn -records "$CC/spawn/records.jsonl" -harness-stderr "$CC/spawn/telemetry.txt" -hook-bin "$WORK/bin/sentinel"

# -------------------------------------------------------------- G-3 output
# Same wiring, but the sentinel relays to the REAL `straza hook` and claude
# judges what it prints (no enrollment, no server: --conformance-policy).
echo "harness-matrix: claude-output (claude accepts the real encoder's stdout)"
rm -rf "$CC/output"; mkdir -p "$CC/output/cfg"
cp "$CC/spawn/cfg/settings.json" "$CC/output/cfg/settings.json"
fire_claude "$CC/output" \
  SENTINEL_OUT="$CC/output/records.jsonl" \
  SENTINEL_EXEC="$WORK/bin/straza hook --harness claude-code --conformance-policy $POLICY"
gate claude-output -records "$CC/output/records.jsonl" -harness-stderr "$CC/output/telemetry.txt"
