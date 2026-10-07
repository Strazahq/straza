"""Claude Agent SDK glue tests (stdlib unittest + the installed SDK).

Same stub mechanism as test_core: straza is a small Python script driven
by env vars, and the tests assert relay fidelity (the SDK's claude-code-shaped
payload passes through unchanged under STRAZA_HARNESS=claude-code), verbatim
permissionDecision relay, fail-closed behavior, monitor mode, delegation
capture (Stop / SubagentStart / SubagentStop, including the sub-agent
attribution fields), and that the returned hooks dict is structurally valid
for ClaudeAgentOptions.

Skips cleanly when claude-agent-sdk is not installed (the stdlib CI job).
No live Claude runs: the async callbacks are unit-tested directly.
"""

import json
import os
import sys
import tempfile
import unittest
from typing import get_args

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

try:
    from claude_agent_sdk import ClaudeAgentOptions, HookMatcher
    from claude_agent_sdk.types import HookEvent

    from straza_agentkit.integrations import claude_agent

    _SKIP = ""
except Exception as exc:  # pragma: no cover - exercised only without the SDK
    ClaudeAgentOptions = HookMatcher = HookEvent = claude_agent = None
    _SKIP = f"claude-agent-sdk not importable: {exc}"

STUB = r"""
import json, os, sys
payload = json.load(sys.stdin)
with open(os.environ["STUB_CAPTURE"], "a", encoding="utf-8") as f:
    f.write(json.dumps({"payload": payload, "harness": os.environ.get("STRAZA_HARNESS", "")}) + "\n")
mode = os.environ.get("STUB_MODE", "allow")
if mode == "explode":
    sys.stderr.write("stub exploded before deciding\n")
    sys.exit(3)
if mode == "deny":
    print(json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": "Straza: destructive delete blocked (rule shell-rm)",
    }}))
    sys.exit(2)
print(json.dumps({"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "allow"}}))
sys.exit(0)
"""

PRE_TOOL_USE_PAYLOAD = {
    "hook_event_name": "PreToolUse",
    "session_id": "sess-0123",
    "transcript_path": "",
    "cwd": "C:\\work",
    "tool_name": "Bash",
    "tool_input": {"command": "rm -rf /tmp/x"},
    "tool_use_id": "toolu_01",
}

PROMPT_PAYLOAD = {
    "hook_event_name": "UserPromptSubmit",
    "session_id": "sess-0123",
    "transcript_path": "",
    "cwd": "C:\\work",
    "prompt": "wipe the scratch dir",
}

# Delegation payloads. Field sets are the ones the harness actually emits
# (verified against a live claude-code run with a logging hook on
# all events, and against claude-agent-sdk 0.2.128's hook-input TypedDicts):
# a tool call made INSIDE a Task-spawned sub-agent carries agent_id +
# agent_type; the same call on the main thread carries neither.
PARENT_TRANSCRIPT = "/home/u/.claude/projects/p/sess-0123.jsonl"
AGENT_TRANSCRIPT = "/home/u/.claude/projects/p/sess-0123/subagents/agent-77.jsonl"

SUBAGENT_PRE_TOOL_USE_PAYLOAD = dict(
    PRE_TOOL_USE_PAYLOAD,
    tool_input={"command": "echo SUBAGENT_PROBE_MARKER"},
    tool_use_id="toolu_09",
    agent_id="agent-77",
    agent_type="general-purpose",
)

TASK_SPAWN_PAYLOAD = dict(
    PRE_TOOL_USE_PAYLOAD,
    tool_name="Task",
    tool_input={"subagent_type": "general-purpose", "prompt": "wipe the scratch dir"},
    tool_use_id="toolu_02",
)

STOP_PAYLOAD = {
    "hook_event_name": "Stop",
    "session_id": "sess-0123",
    "transcript_path": PARENT_TRANSCRIPT,
    "cwd": "C:\\work",
    "stop_hook_active": False,
    # CLI-only extra: not in the SDK TypedDict, but TypedDicts do not filter at
    # runtime and we relay verbatim, so straza receives it.
    "last_assistant_message": "scratch dir wiped",
}

# SubagentStart has NO agent_transcript_path in claude-code; transcript_path
# is the PARENT's. The child path only exists at SubagentStop.
SUBAGENT_START_PAYLOAD = {
    "hook_event_name": "SubagentStart",
    "session_id": "sess-0123",
    "transcript_path": PARENT_TRANSCRIPT,
    "cwd": "C:\\work",
    "agent_id": "agent-77",
    "agent_type": "general-purpose",
}

SUBAGENT_STOP_PAYLOAD = {
    "hook_event_name": "SubagentStop",
    "session_id": "sess-0123",
    "transcript_path": PARENT_TRANSCRIPT,  # still the parent's
    "cwd": "C:\\work",
    "stop_hook_active": False,
    "agent_id": "agent-77",
    "agent_type": "general-purpose",
    "agent_transcript_path": AGENT_TRANSCRIPT,  # the child's own file
    "last_assistant_message": "probe done",
    "exit_code": 0,
}

CAPTURE_PAYLOADS = {
    "UserPromptSubmit": PROMPT_PAYLOAD,
    "Stop": STOP_PAYLOAD,
    "SubagentStart": SUBAGENT_START_PAYLOAD,
    "SubagentStop": SUBAGENT_STOP_PAYLOAD,
}

CTX = {"signal": None}


@unittest.skipIf(bool(_SKIP), _SKIP)
class ClaudeAgentGlueTest(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.stub = os.path.join(self.dir, "stub.py")
        self.capture = os.path.join(self.dir, "capture.jsonl")
        with open(self.stub, "w", encoding="utf-8") as f:
            f.write(STUB)
        os.environ["STRAZA_BIN"] = f'"{sys.executable}" "{self.stub}"'
        os.environ["STUB_CAPTURE"] = self.capture
        os.environ["STUB_MODE"] = "allow"

    def tearDown(self):
        for k in ("STRAZA_BIN", "STUB_CAPTURE", "STUB_MODE"):
            os.environ.pop(k, None)

    def invocations(self):
        if not os.path.exists(self.capture):
            return []
        with open(self.capture, encoding="utf-8") as f:
            return [json.loads(line) for line in f if line.strip()]

    def hook_for(self, event, enforce=True):
        return claude_agent.straza_hooks(enforce=enforce)[event][0].hooks[0]

    def pre_hook(self, enforce=True):
        return self.hook_for("PreToolUse", enforce)

    def prompt_hook(self, enforce=True):
        return self.hook_for("UserPromptSubmit", enforce)

    @staticmethod
    def is_blocking(output):
        hso = output.get("hookSpecificOutput") or {}
        return (
            hso.get("permissionDecision") in ("deny", "ask")
            or output.get("decision") == "block"
            or output.get("continue_") is False
        )

    # --- PreToolUse -------------------------------------------------------

    async def test_deny_relays_engine_decision_verbatim(self):
        os.environ["STUB_MODE"] = "deny"
        out = await self.pre_hook()(dict(PRE_TOOL_USE_PAYLOAD), "toolu_01", CTX)
        self.assertEqual(
            out,
            {
                "hookSpecificOutput": {
                    "hookEventName": "PreToolUse",
                    "permissionDecision": "deny",
                    "permissionDecisionReason": "Straza: destructive delete blocked (rule shell-rm)",
                }
            },
        )

    async def test_payload_passes_through_under_claude_code_dialect(self):
        await self.pre_hook()(dict(PRE_TOOL_USE_PAYLOAD), "toolu_01", CTX)
        (inv,) = self.invocations()
        self.assertEqual(inv["harness"], "claude-code")
        self.assertEqual(inv["payload"], PRE_TOOL_USE_PAYLOAD)  # verbatim, no re-mapping

    async def test_allow_returns_non_blocking_output(self):
        out = await self.pre_hook()(dict(PRE_TOOL_USE_PAYLOAD), "toolu_01", CTX)
        self.assertFalse(self.is_blocking(out))
        # The engine's explicit allow is relayed verbatim too.
        self.assertEqual(out["hookSpecificOutput"]["permissionDecision"], "allow")

    async def test_missing_binary_fails_closed(self):
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "no-such-binary")
        out = await self.pre_hook()(dict(PRE_TOOL_USE_PAYLOAD), "toolu_01", CTX)
        hso = out["hookSpecificOutput"]
        self.assertEqual(hso["permissionDecision"], "deny")
        self.assertEqual(hso["hookEventName"], "PreToolUse")
        self.assertIn("failing closed", hso["permissionDecisionReason"])

    async def test_broken_binary_fails_closed_with_stderr_reason(self):
        os.environ["STUB_MODE"] = "explode"  # exit 3, no decision JSON
        out = await self.pre_hook()(dict(PRE_TOOL_USE_PAYLOAD), "toolu_01", CTX)
        hso = out["hookSpecificOutput"]
        self.assertEqual(hso["permissionDecision"], "deny")
        self.assertIn("stub exploded", hso["permissionDecisionReason"])
        self.assertTrue(hso["permissionDecisionReason"].startswith("Straza"))

    async def test_enforce_false_never_blocks(self):
        hook = self.pre_hook(enforce=False)
        for mode in ("deny", "explode"):
            os.environ["STUB_MODE"] = mode
            out = await hook(dict(PRE_TOOL_USE_PAYLOAD), "toolu_01", CTX)
            self.assertEqual(out, {}, f"mode={mode} must not block")
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        out = await hook(dict(PRE_TOOL_USE_PAYLOAD), "toolu_01", CTX)
        self.assertEqual(out, {})
        # Monitor mode still relays: the deny/explode calls reached the stub.
        self.assertEqual(len(self.invocations()), 2)

    # --- Delegation: enforcement stays at PreToolUse ----------------------

    async def test_delegated_tool_call_relays_agent_attribution(self):
        """A tool call from inside a Task-spawned sub-agent fires PreToolUse
        with agent_id/agent_type; verbatim relay puts both in front of straza."""
        await self.pre_hook()(dict(SUBAGENT_PRE_TOOL_USE_PAYLOAD), "toolu_09", CTX)
        (inv,) = self.invocations()
        self.assertEqual(inv["harness"], "claude-code")
        self.assertEqual(inv["payload"], SUBAGENT_PRE_TOOL_USE_PAYLOAD)
        self.assertEqual(inv["payload"]["agent_id"], "agent-77")
        self.assertEqual(inv["payload"]["agent_type"], "general-purpose")

    async def test_main_thread_tool_call_has_no_agent_attribution(self):
        """Positive control for the test above: the same call on the main
        thread carries neither field, so straza can tell them apart."""
        await self.pre_hook()(dict(PRE_TOOL_USE_PAYLOAD), "toolu_01", CTX)
        (inv,) = self.invocations()
        self.assertNotIn("agent_id", inv["payload"])
        self.assertNotIn("agent_type", inv["payload"])

    async def test_delegated_tool_call_is_enforced(self):
        """Policy applies to delegated work: a deny inside a sub-agent is
        relayed verbatim, exactly like a main-thread deny."""
        os.environ["STUB_MODE"] = "deny"
        out = await self.pre_hook()(dict(SUBAGENT_PRE_TOOL_USE_PAYLOAD), "toolu_09", CTX)
        self.assertTrue(self.is_blocking(out))
        self.assertEqual(out["hookSpecificOutput"]["permissionDecision"], "deny")

    async def test_spawn_tool_is_the_delegation_enforcement_point(self):
        """Delegation is denied at PreToolUse on the spawn tool (Task →
        task.spawn), NOT at SubagentStart, which cannot block."""
        os.environ["STUB_MODE"] = "deny"
        out = await self.pre_hook()(dict(TASK_SPAWN_PAYLOAD), "toolu_02", CTX)
        self.assertTrue(self.is_blocking(out))
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["tool_name"], "Task")

    # --- Capture lanes ----------------------------------------------------

    async def test_prompt_capture_records_and_never_blocks(self):
        out = await self.prompt_hook()(dict(PROMPT_PAYLOAD), None, CTX)
        self.assertEqual(out, {})
        (inv,) = self.invocations()
        self.assertEqual(inv["harness"], "claude-code")
        self.assertEqual(inv["payload"]["hook_event_name"], "UserPromptSubmit")
        self.assertEqual(inv["payload"]["prompt"], "wipe the scratch dir")

    async def test_prompt_capture_survives_exploding_stub(self):
        os.environ["STUB_MODE"] = "explode"
        out = await self.prompt_hook()(dict(PROMPT_PAYLOAD), None, CTX)
        self.assertEqual(out, {})
        (inv,) = self.invocations()  # recorded before the stub crashed
        self.assertEqual(inv["payload"]["prompt"], "wipe the scratch dir")
        # And with no binary at all: still a silent non-blocking output.
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        self.assertEqual(await self.prompt_hook()(dict(PROMPT_PAYLOAD), None, CTX), {})

    async def test_every_capture_event_relays_verbatim_and_never_blocks(self):
        for event, payload in CAPTURE_PAYLOADS.items():
            with self.subTest(event=event):
                if os.path.exists(self.capture):
                    os.remove(self.capture)  # reset the stub log per event
                out = await self.hook_for(event)(dict(payload), None, CTX)
                self.assertEqual(out, {}, event)
                (inv,) = self.invocations()
                self.assertEqual(inv["harness"], "claude-code", event)
                self.assertEqual(inv["payload"], payload, event)  # verbatim

    async def test_subagent_start_relays_lineage(self):
        """SubagentStart is context-only in Claude Code, and it CANNOT block. It
        is registered purely so straza learns the sub-agent's id/type."""
        out = await self.hook_for("SubagentStart")(dict(SUBAGENT_START_PAYLOAD), None, CTX)
        self.assertEqual(out, {})
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["hook_event_name"], "SubagentStart")
        self.assertEqual(inv["payload"]["agent_id"], "agent-77")
        self.assertEqual(inv["payload"]["agent_type"], "general-purpose")
        # claude-code has no child transcript path yet at start; the parent's
        # is what rides along. Do not synthesize one.
        self.assertNotIn("agent_transcript_path", inv["payload"])
        self.assertEqual(inv["payload"]["transcript_path"], PARENT_TRANSCRIPT)

    async def test_subagent_stop_relays_transcript_and_reply(self):
        """SubagentStop is where the sub-agent's OWN transcript path and its
        payload-borne reply become available. Verbatim relay hands straza both."""
        out = await self.hook_for("SubagentStop")(dict(SUBAGENT_STOP_PAYLOAD), None, CTX)
        self.assertEqual(out, {})
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["agent_transcript_path"], AGENT_TRANSCRIPT)
        self.assertEqual(inv["payload"]["last_assistant_message"], "probe done")
        self.assertEqual(inv["payload"]["exit_code"], 0)
        self.assertEqual(inv["payload"]["agent_id"], "agent-77")

    async def test_stop_relays_turn_reply(self):
        """Stop is the per-turn reply-capture point: without it replies land
        only on a clean session exit."""
        out = await self.hook_for("Stop")(dict(STOP_PAYLOAD), None, CTX)
        self.assertEqual(out, {})
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["hook_event_name"], "Stop")
        self.assertEqual(inv["payload"]["last_assistant_message"], "scratch dir wiped")

    async def test_capture_lanes_never_deny(self):
        """Capture is never an enforcement point: not on a policy deny, not on
        a broken binary, not on a missing one, in either enforce mode."""
        for event, payload in CAPTURE_PAYLOADS.items():
            for enforce in (True, False):
                hook = self.hook_for(event, enforce=enforce)
                for mode in ("deny", "explode"):
                    os.environ["STUB_MODE"] = mode
                    out = await hook(dict(payload), None, CTX)
                    self.assertEqual(out, {}, f"{event}/{enforce}/{mode}")
                    self.assertFalse(self.is_blocking(out), f"{event}/{enforce}/{mode}")
                os.environ["STUB_MODE"] = "allow"
        # ... and with no binary at all.
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        for event, payload in CAPTURE_PAYLOADS.items():
            self.assertEqual(await self.hook_for(event)(dict(payload), None, CTX), {}, event)

    async def test_capture_lanes_relay_in_monitor_mode(self):
        for event, payload in CAPTURE_PAYLOADS.items():
            await self.hook_for(event, enforce=False)(dict(payload), None, CTX)
        events = [inv["payload"]["hook_event_name"] for inv in self.invocations()]
        self.assertEqual(events, list(CAPTURE_PAYLOADS))

    # --- Options wiring ---------------------------------------------------

    async def test_hooks_dict_is_valid_for_claude_agent_options(self):
        hooks = claude_agent.straza_hooks()
        options = ClaudeAgentOptions(hooks=hooks)  # smoke: constructs cleanly
        self.assertIs(options.hooks, hooks)
        self.assertEqual(
            set(hooks),
            {"PreToolUse", "UserPromptSubmit", "Stop", "SubagentStart", "SubagentStop"},
        )
        for event, matchers in hooks.items():
            self.assertIsInstance(matchers, list, event)
            (matcher,) = matchers
            self.assertIsInstance(matcher, HookMatcher)
            self.assertIsNone(matcher.matcher)  # match ALL tools
            (callback,) = matcher.hooks
            self.assertTrue(callable(callback))

    async def test_registered_events_are_real_sdk_hook_events(self):
        """Typo guard: every key we register must be in the SDK's HookEvent
        union (the SDK passes event names through to the CLI unvalidated, so a
        misspelled one would silently never fire)."""
        valid = set()
        for literal in get_args(HookEvent):
            valid.update(get_args(literal))
        self.assertTrue(set(claude_agent.straza_hooks()) <= valid, valid)


if __name__ == "__main__":
    unittest.main()
