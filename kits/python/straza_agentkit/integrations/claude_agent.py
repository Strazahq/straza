"""Claude Agent SDK integration: Straza hooks for ``ClaudeAgentOptions(hooks=...)``.

Near zero-code framework: the Claude Agent SDK speaks Claude Code's own hook
contract, and by default ``query()`` loads shell-command hooks from settings
files, so on a machine where ``straza install`` wired user-level hooks, a
Python Claude-Agent-SDK app is already governed with NO code. This module is
the in-process variant for apps that configure hooks programmatically (or
disable ``setting_sources``)::

    from claude_agent_sdk import ClaudeAgentOptions, query
    from straza_agentkit.integrations.claude_agent import straza_hooks

    options = ClaudeAgentOptions(hooks=straza_hooks())

Because the SDK's hook payloads ARE Claude Code hook payloads, no event
mapping happens here: the PreToolUse callback relays the SDK's own
``input_data`` dict verbatim to ``straza hook`` under the **claude-code**
dialect (``STRAZA_HARNESS=claude-code``, not python-sdk) and returns the
engine's decision JSON (``hookSpecificOutput.permissionDecision`` +
``permissionDecisionReason``) verbatim in the SDK's expected hook output
shape (``SyncHookJSONOutput``). The capture callbacks (UserPromptSubmit,
Stop, SubagentStart, SubagentStop) relay their payloads for conversation,
reply and lineage capture, and never block.

Verbatim relay is what makes delegation visible. A tool call made inside a
Task-spawned sub-agent fires an ordinary ``PreToolUse`` (so policy already
covers delegated work) and its payload carries ``agent_id`` +
``agent_type``, which the main thread's does not. ``SubagentStop`` adds
``agent_transcript_path`` (the sub-agent's own transcript; ``transcript_path``
stays the PARENT's on every claude-code event) plus the CLI's
``last_assistant_message``. None of that is hand-picked here: the payload
goes to straza as it arrived, and the claude-code adapter maps the events.

Fail-closed contract, same as the rest of the kit: with
``enforce=True`` (default), a missing binary, spawn failure, timeout, or
unusable engine output is a DENY with an actionable reason. With
``enforce=False`` the hooks are monitor-only: every event is still relayed
(so audit/capture see it) but the callbacks always return a non-blocking
output: they never deny, not even on a policy deny.
"""

from __future__ import annotations

import asyncio
import json
import os
import subprocess  # nosec B404 - spawning straza IS the design
from typing import Any, Optional

from claude_agent_sdk import HookMatcher

from .. import _DENY_EXIT, _TIMEOUT_SECS, _straza_argv, _stdout_json

__all__ = [
    "straza_hooks",
    "pre_tool_use_callback",
    "capture_callback",
    "user_prompt_submit_callback",
]

# The SDK's hook payloads are claude-code hook payloads by construction, so the
# relay uses the claude-code dialect, not the kit's python-sdk dialect.
_DIALECT = "claude-code"


def _relay(payload: dict) -> "tuple[Optional[int], Optional[dict], str]":
    """Spawn ``straza hook`` with the SDK's payload under the claude-code
    dialect. Returns ``(returncode, stdout_json, detail)``; a spawn failure of
    any kind returns ``(None, None, reason)`` and the caller decides (enforce
    ⇒ deny). Honors $STRAZA_BIN and the kit's timeout, via the same
    plumbing as the core (``_straza_argv`` / ``_stdout_json``)."""
    argv = _straza_argv()
    if not argv:
        return None, None, (
            "straza binary not found. Install it and enroll "
            "(https://github.com/strazahq/straza), or set STRAZA_BIN"
        )
    env = dict(os.environ, STRAZA_HARNESS=_DIALECT)
    try:
        proc = subprocess.run(  # nosec B603 - fixed binary, no shell
            argv + ["hook"],
            input=json.dumps(payload).encode("utf-8"),
            capture_output=True,
            timeout=_TIMEOUT_SECS,
            env=env,
        )
    except (OSError, ValueError, subprocess.TimeoutExpired) as exc:
        return None, None, f"decision unavailable ({exc.__class__.__name__}), failing closed"
    detail = (proc.stderr or b"").decode("utf-8", "replace").strip()
    return proc.returncode, _stdout_json(proc.stdout), detail


def _deny_output(reason: str) -> dict:
    """A PreToolUse deny in the SDK's SyncHookJSONOutput shape."""
    if not reason.startswith("Straza"):
        reason = "Straza: " + reason
    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }


def pre_tool_use_callback(enforce: bool = True):
    """Build the PreToolUse HookCallback: relay the SDK's hook payload to
    straza and return the engine's permissionDecision verbatim."""

    async def relay_pre_tool_use(
        input_data: dict, tool_use_id: "Optional[str]", context: Any
    ) -> dict:
        try:
            payload = dict(input_data or {})
        except (TypeError, ValueError):
            payload = {}
        code, out, detail = await asyncio.to_thread(_relay, payload)
        if not enforce:
            return {}  # monitor mode: relayed for audit, never blocks
        if code == 0:
            # Allow. Relay the engine's JSON verbatim (it may carry an explicit
            # allow decision, updatedInput, additionalContext, ...).
            return out if out is not None else {}
        if code == _DENY_EXIT and out is not None and out.get("hookSpecificOutput"):
            # Policy deny: the engine already emits the claude-code decision
            # encoding, which is exactly the SDK's output shape. Verbatim.
            return out
        # Everything else fails closed: exit 2 without usable JSON, unexpected
        # exit codes, spawn failure, timeout, missing binary.
        reason = ""
        if out is not None:
            hso = out.get("hookSpecificOutput") or {}
            reason = hso.get("permissionDecisionReason", "") or out.get("reason", "")
        return _deny_output(reason or detail or "denied by policy")

    return relay_pre_tool_use


def capture_callback():
    """Build a capture-only HookCallback: relay the event's payload verbatim
    for conversation / reply / lineage capture and return an empty (silent,
    non-blocking) output.

    Observational: NEVER blocks, regardless of the engine's answer or of any
    error. That is deliberate for every event it serves:

    - ``SubagentStart`` is context-only in Claude Code and *cannot* block;
      wiring it as an enforcement point would be a lie. Delegation is enforced
      at ``PreToolUse`` on the spawn tool (``Task``/``Agent`` → task.spawn).
    - ``SubagentStop`` technically *can* block via exit 2, but denying there
      would kill a sub-agent whose work is already done: pointless and
      confusing. It is capture.
    - ``Stop`` / ``UserPromptSubmit`` are the turn's reply and prompt capture.

    Capture never spawns anything, so there is no hook-recursion risk.
    """

    async def relay_capture(input_data: dict, tool_use_id: "Optional[str]", context: Any) -> dict:
        try:
            await asyncio.to_thread(_relay, dict(input_data or {}))
        except Exception:  # nosec B110 - capture must never break the turn
            pass
        return {}

    return relay_capture


def user_prompt_submit_callback():
    """Build the UserPromptSubmit HookCallback: relay the prompt for
    conversation capture. Observational: NEVER blocks, regardless of errors.

    Alias of :func:`capture_callback`, kept as the named prompt lane."""
    return capture_callback()


def straza_hooks(enforce: bool = True) -> "dict[str, list[HookMatcher]]":
    """Hooks dict for ``ClaudeAgentOptions(hooks=straza_hooks())``.

    - ``PreToolUse``: one match-all :class:`HookMatcher` whose callback relays
      each tool call to ``straza hook`` (claude-code dialect) and returns
      the engine's ``permissionDecision`` + reason verbatim. Deny wins over
      all other hooks per the Claude Code hook contract. This is also the
      **only** enforcement point for delegation: it fires for tool calls made
      inside a Task-spawned sub-agent, and for the spawn tool itself.
    - ``UserPromptSubmit``: prompt capture.
    - ``Stop``: per-turn reply capture; without it, replies are captured only
      on a clean session exit.
    - ``SubagentStart``: sub-agent lineage (``agent_id``/``agent_type``).
      Context-only in Claude Code; it CANNOT block and is not an enforcement
      point.
    - ``SubagentStop``: sub-agent reply + its own ``agent_transcript_path``.
      Capture only: we never deny here.

    All four capture lanes are match-all and never block, in either mode.
    ``enforce=False`` switches ``PreToolUse`` to monitor-only as well (relay +
    audit, no deny).
    """
    return {
        "PreToolUse": [HookMatcher(matcher=None, hooks=[pre_tool_use_callback(enforce)])],
        "UserPromptSubmit": [HookMatcher(matcher=None, hooks=[user_prompt_submit_callback()])],
        "Stop": [HookMatcher(matcher=None, hooks=[capture_callback()])],
        "SubagentStart": [HookMatcher(matcher=None, hooks=[capture_callback()])],
        "SubagentStop": [HookMatcher(matcher=None, hooks=[capture_callback()])],
    }
