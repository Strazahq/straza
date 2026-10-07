"""Shared wiring for the LangChain glue tests.

The lane is split across two test modules that drive the same stub:
`test_langchain.py` (the middleware: enforcement, tool mapping, capture) and
`test_langchain_subagents.py` (delegation, the observational callback lane, and
the deepagents subagent boundary). Everything both of them need lives here.

Same stubbing mechanism as test_core.py: straza is a small Python script
selected via STRAZA_BIN, driven by env vars, recording every payload it
receives.

The framework imports happen here, once. When langchain (or deepagents) is not
installed the names still exist (bound to None), so both test modules import
cleanly and every test that touches one is skipped by HAS_LANGCHAIN /
HAS_DEEPAGENTS.
"""

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import straza_agentkit as straza  # noqa: E402

try:
    from langchain.agents import create_agent
    from langchain.agents.middleware import AgentMiddleware
    from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
    from langchain_core.messages import AIMessage, HumanMessage, SystemMessage, ToolMessage
    from langchain_core.outputs import ChatGeneration, LLMResult
    from langchain_core.tools import tool
    from langgraph.prebuilt.tool_node import ToolCallRequest

    from straza_agentkit.integrations.langchain import (
        StrazaCallbackHandler,
        StrazaMiddleware,
        govern_subagents,
    )

    HAS_LANGCHAIN = True
except ImportError:
    # The names have to exist either way: the test modules import them at module
    # level (class bases, and calls inside test bodies). Every test that touches
    # one is skipped by HAS_LANGCHAIN.
    create_agent = AgentMiddleware = GenericFakeChatModel = None
    AIMessage = HumanMessage = SystemMessage = ToolMessage = None
    ChatGeneration = LLMResult = tool = ToolCallRequest = None
    StrazaCallbackHandler = StrazaMiddleware = govern_subagents = None

    HAS_LANGCHAIN = False

try:
    from deepagents.backends.state import StateBackend
    from deepagents.middleware.subagents import SubAgentMiddleware

    HAS_DEEPAGENTS = HAS_LANGCHAIN
except ImportError:
    StateBackend = SubAgentMiddleware = None

    HAS_DEEPAGENTS = False

__all__ = [
    "AIMessage",
    "AgentMiddleware",
    "ChatGeneration",
    "DEEPAGENTS_SKIP_REASON",
    "GenericFakeChatModel",
    "HAS_DEEPAGENTS",
    "HAS_LANGCHAIN",
    "HumanMessage",
    "LLMResult",
    "SKIP_REASON",
    "STUB",
    "StateBackend",
    "StrazaCallbackHandler",
    "StrazaMiddleware",
    "StubbedCase",
    "SubAgentMiddleware",
    "SystemMessage",
    "ToolCallRequest",
    "ToolCallingFakeModel",
    "ToolMessage",
    "create_agent",
    "govern_subagents",
    "tool",
    "tool_request",
]

STUB = r"""
import json, os, sys
payload = json.load(sys.stdin)
with open(os.environ["STUB_CAPTURE"], "a", encoding="utf-8") as f:
    f.write(json.dumps({"payload": payload, "harness": os.environ.get("STRAZA_HARNESS", "")}) + "\n")
mode = os.environ.get("STUB_MODE", "allow")
selective = [t for t in os.environ.get("STUB_DENY_TOOLS", "").split(",") if t]
if selective:
    mode = "deny" if payload.get("tool_name") in selective else "allow"
if mode == "crash":
    sys.stderr.write("stub exploded")
    sys.exit(1)
if mode == "deny":
    print(json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": "Straza: destructive delete blocked",
    }}))
    sys.exit(2)
print(json.dumps({"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "allow"}}))
sys.exit(0)
"""

SKIP_REASON = "langchain not installed (pip install straza-agentkit[langchain])"
DEEPAGENTS_SKIP_REASON = "deepagents not installed (pip install deepagents)"


class StubbedCase(unittest.TestCase):
    """Shared straza stub wiring (mirrors test_core.py)."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.stub = os.path.join(self.dir, "stub.py")
        self.capture = os.path.join(self.dir, "capture.jsonl")
        with open(self.stub, "w", encoding="utf-8") as f:
            f.write(STUB)
        os.environ["STRAZA_BIN"] = f'"{sys.executable}" "{self.stub}"'
        os.environ["STUB_CAPTURE"] = self.capture
        os.environ["STUB_MODE"] = "allow"
        # The kit's lazy session.start is test_core.py's subject; here it would
        # just prepend a SessionStart to every event list and make this lane's
        # assertions depend on module discovery order. Pin it checked-in.
        straza._session_started = True

    def tearDown(self):
        for k in ("STRAZA_BIN", "STUB_CAPTURE", "STUB_MODE", "STUB_DENY_TOOLS"):
            os.environ.pop(k, None)

    def invocations(self):
        if not os.path.exists(self.capture):
            return []
        with open(self.capture, encoding="utf-8") as f:
            return [json.loads(line) for line in f if line.strip()]

    def events(self):
        return [i["payload"]["hook_event_name"] for i in self.invocations()]

    def payloads(self, event):
        return [i["payload"] for i in self.invocations() if i["payload"]["hook_event_name"] == event]

    def decisions(self):
        """Canonical tool of every ENFORCED check, in order."""
        return [p["tool_name"] for p in self.payloads("PreToolUse")]

    def posts(self):
        """Canonical tool of every observational tool.post, in order."""
        return [p["tool_name"] for p in self.payloads("PostToolUse")]


def tool_request(name, args, call_id="call-1"):
    """A real langgraph ToolCallRequest, as wrap_tool_call receives it."""
    return ToolCallRequest(
        tool_call={"name": name, "args": args, "id": call_id, "type": "tool_call"},
        tool=None,
        state={"messages": []},
        runtime=None,
    )


class ToolCallingFakeModel(GenericFakeChatModel if HAS_LANGCHAIN else object):
    """GenericFakeChatModel + a bind_tools no-op: create_agent binds tools to
    the model, and langchain_core's fakes don't implement bind_tools."""

    def bind_tools(self, tools, **kwargs):
        return self
