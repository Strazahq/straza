"""Shared wiring for the OpenAI Agents SDK glue tests.

The lane is split across two test modules that drive the same stub:
`test_openai_agents.py` (the unit layer: guardrails exercised directly on the
SDK's own data objects) and `test_openai_agents_runloop.py` (the run-loop
layer: a scripted FakeModel drives the real `Runner.run`, plus the private
SDK attribute contract the delegation walk depends on). Everything both of
them need lives here.

Same stubbing mechanism as test_core.py: straza is a small Python script
selected via STRAZA_BIN, driven by env vars, recording every payload it
receives. Both layers are offline (no API key, no network).

The framework imports happen here, once. When openai-agents is not installed
the names still exist (bound to None), so both test modules import cleanly
and every test that touches one is skipped by HAVE_AGENTS.
"""

import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import straza_agentkit as straza  # noqa: E402

try:
    import agents as agents_sdk
    from agents.lifecycle import RunHooksBase
    from agents.run_context import RunContextWrapper
    from agents.tool_context import ToolContext

    HAVE_AGENTS = True
except ImportError:
    agents_sdk = RunHooksBase = RunContextWrapper = ToolContext = None

    HAVE_AGENTS = False

__all__ = [
    "FakeModel",
    "HAVE_AGENTS",
    "ModelResponse",
    "RunContextWrapper",
    "RunHooksBase",
    "SKIP_REASON",
    "STUB",
    "StubEnv",
    "ToolContext",
    "Usage",
    "agents_sdk",
    "call",
    "say",
]

STUB = r"""
import json, os, sys
payload = json.load(sys.stdin)
with open(os.environ["STUB_CAPTURE"], "a", encoding="utf-8") as f:
    f.write(json.dumps({"payload": payload, "harness": os.environ.get("STRAZA_HARNESS", "")}) + "\n")
mode = os.environ.get("STUB_MODE", "allow")
if mode == "crash":
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

SKIP_REASON = "openai-agents not installed (pip install straza-agentkit[openai-agents])"

if HAVE_AGENTS:
    from agents.items import ModelResponse
    from agents.models.interface import Model
    from agents.usage import Usage
    from openai.types.responses import (
        ResponseFunctionToolCall,
        ResponseOutputMessage,
        ResponseOutputText,
    )

    def say(text):
        """One assistant text message in the Responses output shape."""
        return ResponseOutputMessage(
            id="msg",
            role="assistant",
            status="completed",
            type="message",
            content=[ResponseOutputText(text=text, type="output_text", annotations=[])],
        )

    def call(name, **args):
        """One function-tool call. A handoff is just a call to the SDK's
        generated `transfer_to_<agent>` tool."""
        return ResponseFunctionToolCall(
            id="ft",
            call_id="call_" + name,
            name=name,
            arguments=json.dumps(args),
            type="function_call",
        )

    class FakeModel(Model):
        """Offline model: replays scripted output items, one list per turn (the
        last entry repeats). No API key, no network, no tracing export."""

        def __init__(self, *turns):
            self.turns = list(turns) or [[say("ok")]]
            self.seen = 0

        async def get_response(self, *args, **kwargs):
            items = self.turns[min(self.seen, len(self.turns) - 1)]
            self.seen += 1
            return ModelResponse(output=list(items), usage=Usage(), response_id=None)

        def stream_response(self, *args, **kwargs):
            raise NotImplementedError("these tests use the non-streaming path")

else:
    ModelResponse = Model = Usage = None
    say = call = FakeModel = None


class StubEnv:
    """straza stubbed by a scripted python script; every invocation appends its
    payload to a capture file. Mixed into the TestCases in both modules."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.stub = os.path.join(self.dir, "stub.py")
        self.capture = os.path.join(self.dir, "capture.jsonl")
        with open(self.stub, "w", encoding="utf-8") as f:
            f.write(STUB)
        os.environ["STRAZA_BIN"] = f'"{sys.executable}" "{self.stub}"'
        os.environ["STUB_CAPTURE"] = self.capture
        os.environ["STUB_MODE"] = "allow"
        # Treat the session as already checked in: session.start is test_core's
        # business, and this file's assertions read the events under test.
        straza._session_started = True

    def tearDown(self):
        for k in ("STRAZA_BIN", "STUB_CAPTURE", "STUB_MODE"):
            os.environ.pop(k, None)

    def invocations(self):
        if not os.path.exists(self.capture):
            return []
        with open(self.capture, encoding="utf-8") as f:
            return [json.loads(line) for line in f if line.strip()]

    def payloads(self, event):
        """Recorded payloads for one canonical event kind."""
        return [
            i["payload"] for i in self.invocations() if i["payload"]["hook_event_name"] == event
        ]
