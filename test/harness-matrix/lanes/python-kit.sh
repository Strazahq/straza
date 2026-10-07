# lanes/python-kit.sh: the python framework drift lane, sourced by run.sh
# (shares ROOT/TOOLS/WORK/REPORT/note/FAILED; no harness binary, no /etc
# overlay, no MODE). Other lanes live-fire harness BINARIES; this one fires
# the python frameworks kits/python governs. The pinned channel uses
# versions.env, so red is OUR bug; PYTHON_KIT_VERSION=latest resolves what pip
# has today into a fresh venv, so red is vendor drift, never a regression.
#
# Gates: py-floor (python3 with venv machinery, below it all is vacuous);
# py-install (the kit installs the way a user does, via pip resolving
# ./kits/python[extras]); py-suite (the whole unittest suite green under
# STRAZA_KIT_REQUIRE_EXTRAS=1); py-active (>= PY_SUITE_FLOOR tests ran and
# ZERO skipped, the vacuousness guard: an import-broken framework deactivates
# its sub-suite while the run stays green). Venvs live under .tools/: pinned
# is reused while its pin-set matches versions.env, latest is REBUILT each
# run. Tests import the kit from the TREE, so no venv can test stale glue.

PYTHON_KIT_VERSION="${PYTHON_KIT_VERSION:-pinned}"
P="$WORK/python-kit"
mkdir -p "$P"

# The suite size when every extra is installed, with zero skips. It grows
# with the suite, and a DROP below it means a test module stopped being
# discovered.
PY_SUITE_FLOOR=109

echo "harness-matrix: python-kit ($PYTHON_KIT_VERSION channel)"
PYBASE=$(command -v python3 || true)
if [ -z "$PYBASE" ] || ! "$PYBASE" -m venv -h >/dev/null 2>&1; then
  note "- py-floor: **FAIL** (no python3 with venv machinery on this box, so every other gate is vacuous)"
  FAILED=1
else
  note "- py-floor: PASS (\`$("$PYBASE" --version 2>&1)\`)"

  VENV="$TOOLS/python-kit-$PYTHON_KIT_VERSION"
  PINSET="langchain==$LANGCHAIN_PINNED langgraph==$LANGGRAPH_PINNED openai-agents==$OPENAI_AGENTS_PINNED claude-agent-sdk==$CLAUDE_AGENT_SDK_PINNED deepagents==$DEEPAGENTS_PINNED"

  build=1
  if [ "$PYTHON_KIT_VERSION" = latest ]; then
    rm -rf "$VENV" # drift channel: resolve today's releases, always
  elif [ -x "$VENV/bin/python" ] && [ "$(cat "$VENV/.pins" 2>/dev/null)" = "$PINSET" ]; then
    build=0 # cached pinned venv still matches versions.env
  else
    rm -rf "$VENV"
  fi

  install_ok=1
  if [ "$build" = 1 ]; then
    CONSTRAINTS=""
    if [ "$PYTHON_KIT_VERSION" != latest ]; then
      # shellcheck disable=SC2086 -- PINSET word-splits into one dist==ver each
      printf '%s\n' $PINSET > "$P/constraints.txt"
      CONSTRAINTS="-c $P/constraints.txt"
    fi
    if "$PYBASE" -m venv "$VENV" >"$P/install.log" 2>&1 &&
      "$VENV/bin/pip" install --quiet --upgrade pip >>"$P/install.log" 2>&1 &&
      # shellcheck disable=SC2086 -- CONSTRAINTS is "" or "-c <path>"
      "$VENV/bin/pip" install --quiet $CONSTRAINTS \
        "$ROOT/kits/python[langchain,openai-agents,claude-agent-sdk,test]" >>"$P/install.log" 2>&1; then
      [ "$PYTHON_KIT_VERSION" = latest ] || printf '%s' "$PINSET" > "$VENV/.pins"
    else
      install_ok=0
    fi
  fi

  if [ "$install_ok" = 0 ]; then
    note "- py-install: **FAIL** (pip could not build the venv or resolve the kit + extras)"
    { sed 's/^/    /' "$P/install.log" | tail -25 | tee -a "$REPORT"; } || true
    FAILED=1
  else
    note "- py-install: PASS"
    note "  frameworks under test:"
    { "$VENV/bin/pip" list --format=freeze 2>/dev/null |
      grep -iE '^(langchain|langgraph|openai-agents|claude-agent-sdk|deepagents)==' |
      sed 's/^/    /' | tee -a "$REPORT"; } || true

    if (cd "$ROOT/kits/python" &&
      STRAZA_KIT_REQUIRE_EXTRAS=1 "$VENV/bin/python" -m unittest discover -s tests -v \
        >"$P/suite.out" 2>&1); then
      note "- py-suite: PASS"
    else
      note "- py-suite: **FAIL**"
      { grep -vE '\.\.\. ok$' "$P/suite.out" | tail -40 | sed 's/^/    /' | tee -a "$REPORT"; } || true
      FAILED=1
    fi

    ran=$(sed -n 's/^Ran \([0-9][0-9]*\) tests\{0,1\}.*/\1/p' "$P/suite.out" | tail -1)
    skips=$(grep -cE '\.\.\. skipped' "$P/suite.out" || true)
    if [ -n "$ran" ] && [ "$ran" -ge "$PY_SUITE_FLOOR" ] && [ "$skips" = 0 ]; then
      note "- py-active: PASS ($ran tests ran, 0 skipped)"
    else
      note "- py-active: **FAIL** (ran=${ran:-?} floor=$PY_SUITE_FLOOR skipped=$skips; a skip here means a framework sub-suite silently deactivated)"
      { grep -E '\.\.\. skipped' "$P/suite.out" | sed 's/^/    /' | tee -a "$REPORT"; } || true
      FAILED=1
    fi
  fi
fi
