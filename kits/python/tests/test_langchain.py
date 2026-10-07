"""LangChain middleware tests: enforcement, tool mapping, capture (stdlib
unittest; skipped wholesale when langchain is not installed so the stdlib-only
CI job stays green).

The unit tests drive StrazaMiddleware's hooks directly; the end-to-end tests run
a real langchain.agents graph against a deterministic fake chat model. The stub
straza binary and the shared framework imports live in langchain_support.py.

The delegation half of the lane (`task` → task.spawn mapping, the
StrazaCallbackHandler observational lane, govern_subagents(), and the deepagents
subagent boundary) is test_langchain_subagents.py.
"""

import os
import unittest

from langchain_support import (
    HAS_LANGCHAIN,
    SKIP_REASON,
    AIMessage,
    HumanMessage,
    StrazaMiddleware,
    StubbedCase,
    ToolCallingFakeModel,
    ToolMessage,
    create_agent,
    tool,
    tool_request,
)


@unittest.skipUnless(HAS_LANGCHAIN, SKIP_REASON)
class WrapToolCallTest(StubbedCase):
    def setUp(self):
        super().setUp()
        self.mw = StrazaMiddleware(tool_map={"run_shell": "shell.exec"})
        self.handled = []

    def handler(self, request):
        self.handled.append(request)
        return ToolMessage(content="tool ran", tool_call_id=request.tool_call["id"])

    def test_deny_short_circuits_with_straza_toolmessage(self):
        os.environ["STUB_MODE"] = "deny"
        result = self.mw.wrap_tool_call(tool_request("run_shell", {"command": "rm -rf /"}), self.handler)
        self.assertEqual(self.handled, [])  # provably not executed
        self.assertIsInstance(result, ToolMessage)
        self.assertEqual(result.status, "error")
        self.assertEqual(result.tool_call_id, "call-1")
        self.assertTrue(str(result.content).startswith("Straza:"))
        self.assertIn("destructive delete blocked", str(result.content))
        (inv,) = self.invocations()
        self.assertEqual(inv["harness"], "python-sdk")
        self.assertEqual(inv["payload"]["hook_event_name"], "PreToolUse")
        self.assertEqual(inv["payload"]["tool_name"], "shell.exec")
        self.assertEqual(inv["payload"]["tool_input"], {"command": "rm -rf /"})

    def test_allow_executes_wrapped_handler(self):
        request = tool_request("run_shell", {"command": "git status"})
        result = self.mw.wrap_tool_call(request, self.handler)
        self.assertEqual(self.handled, [request])  # same request passed through
        self.assertEqual(str(result.content), "tool ran")

    def test_missing_binary_fails_closed(self):
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "no-such-binary")
        result = self.mw.wrap_tool_call(tool_request("run_shell", {"command": "ls"}), self.handler)
        self.assertEqual(self.handled, [])
        self.assertEqual(result.status, "error")
        self.assertIn("failing closed", str(result.content))

    def test_erroring_binary_fails_closed(self):
        os.environ["STUB_MODE"] = "crash"
        result = self.mw.wrap_tool_call(tool_request("run_shell", {"command": "ls"}), self.handler)
        self.assertEqual(self.handled, [])
        self.assertEqual(result.status, "error")
        self.assertTrue(str(result.content).startswith("Straza:"))


@unittest.skipUnless(HAS_LANGCHAIN, SKIP_REASON)
class ToolMapExtractionTest(StubbedCase):
    def decide(self, mw, name, args):
        mw.wrap_tool_call(tool_request(name, args), lambda r: ToolMessage(content="ok", tool_call_id="x"))
        inv = self.invocations()[-1]
        return inv["payload"]

    def test_file_write_path_key_variants(self):
        mw = StrazaMiddleware(tool_map={"w": "file.write", "r": "file.read", "e": "file.edit"})
        p = self.decide(mw, "w", {"file_path": "/tmp/f", "text": "x"})
        self.assertEqual(p["tool_name"], "file.write")
        self.assertEqual(p["tool_input"], {"paths": ["/tmp/f"]})
        p = self.decide(mw, "r", {"paths": ["/a", "/b"]})
        self.assertEqual(p["tool_name"], "file.read")
        self.assertEqual(p["tool_input"], {"paths": ["/a", "/b"]})
        p = self.decide(mw, "e", {"path": "/etc/hosts"})
        self.assertEqual(p["tool_name"], "file.edit")
        self.assertEqual(p["tool_input"], {"paths": ["/etc/hosts"]})

    def test_net_fetch_url(self):
        mw = StrazaMiddleware(tool_map={"fetch": "net.fetch"})
        p = self.decide(mw, "fetch", {"url": "https://example.com/x"})
        self.assertEqual(p["tool_name"], "net.fetch")
        self.assertEqual(p["tool_input"], {"url": "https://example.com/x"})

    def test_shell_command_list_is_joined(self):
        mw = StrazaMiddleware(tool_map={"sh": "shell.exec"})
        p = self.decide(mw, "sh", {"command": ["echo a", "echo b"]})
        self.assertEqual(p["tool_input"], {"command": "echo a\necho b"})

    def test_unmapped_tool_uses_default_kind(self):
        mw = StrazaMiddleware(tool_map={"sh": "shell.exec"})
        p = self.decide(mw, "mystery_tool", {"whatever": 1})
        self.assertEqual(p["tool_name"], "other")
        self.assertNotIn("tool_input", p)  # nothing extractable for `other`

    def test_constructor_rejects_non_canonical_kinds(self):
        with self.assertRaises(ValueError):
            StrazaMiddleware(tool_map={"search": "midpoint:search_users"})  # MCP-style: gateway, not glue
        with self.assertRaises(ValueError):
            StrazaMiddleware(default_tool="mcp.call")


@unittest.skipUnless(HAS_LANGCHAIN, SKIP_REASON)
class CaptureTest(StubbedCase):
    def test_before_model_emits_prompt_once(self):
        mw = StrazaMiddleware()
        state = {"messages": [HumanMessage(content="what users exist?", id="h1")]}
        mw.before_model(state, None)
        mw.before_model(state, None)  # loop re-entry: same prompt, no resubmit
        self.assertEqual(self.events(), ["UserPromptSubmit"])
        self.assertEqual(self.invocations()[0]["payload"]["prompt"], "what users exist?")

    def test_before_model_emits_each_new_prompt(self):
        mw = StrazaMiddleware()
        mw.before_model({"messages": [HumanMessage(content="first", id="h1")]}, None)
        mw.before_model({"messages": [HumanMessage(content="first", id="h1"), AIMessage(content="a"),
                                      HumanMessage(content="second", id="h2")]}, None)
        prompts = [i["payload"]["prompt"] for i in self.invocations()]
        self.assertEqual(prompts, ["first", "second"])

    def test_after_model_emits_reply(self):
        mw = StrazaMiddleware()
        state = {"messages": [HumanMessage(content="hi"), AIMessage(content="kim and bob")]}
        mw.after_model(state, None)
        self.assertEqual(self.events(), ["SessionEnd"])
        self.assertEqual(self.invocations()[0]["payload"]["prompt_response"], "kim and bob")

    def test_after_model_skips_pure_tool_call_step(self):
        mw = StrazaMiddleware()
        ai = AIMessage(content="", tool_calls=[
            {"name": "run_shell", "args": {"command": "ls"}, "id": "c1", "type": "tool_call"}])
        mw.after_model({"messages": [HumanMessage(content="hi"), ai]}, None)
        self.assertEqual(self.events(), [])

    def test_capture_never_raises_without_binary(self):
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        mw = StrazaMiddleware()
        mw.before_model({"messages": [HumanMessage(content="still fine", id="h1")]}, None)
        mw.after_model({"messages": [AIMessage(content="also fine")]}, None)

    def test_capture_false_silences_this_lane_but_not_enforcement(self):
        """The handover to StrazaCallbackHandler, which reaches subagents:
        capture happens once, in one lane. Decisions are unaffected."""
        mw = StrazaMiddleware(tool_map={"run_shell": "shell.exec"}, capture=False)
        mw.before_model({"messages": [HumanMessage(content="hi")]}, None)
        mw.after_model({"messages": [AIMessage(content="there")]}, None)
        self.assertEqual(self.events(), [])
        mw.wrap_tool_call(tool_request("run_shell", {"command": "ls"}),
                          lambda r: ToolMessage(content="ok", tool_call_id="x"))
        self.assertEqual(self.decisions(), ["shell.exec"])


@unittest.skipUnless(HAS_LANGCHAIN, SKIP_REASON)
class EndToEndCreateAgentTest(StubbedCase):
    """A real create_agent loop: fake model asks for one governed tool call,
    then answers. Asserts enforcement AND the capture lane end to end."""

    def setUp(self):
        super().setUp()
        self.executed = []
        executed = self.executed

        @tool
        def run_shell(command: str) -> str:
            """Run a shell command."""
            executed.append(command)
            return f"ran: {command}"

        model = ToolCallingFakeModel(messages=iter([
            AIMessage(content="", tool_calls=[
                {"name": "run_shell", "args": {"command": "rm -rf /tmp/x"}, "id": "call-1",
                 "type": "tool_call"}]),
            AIMessage(content="all done"),
        ]))
        self.agent = create_agent(
            model,
            tools=[run_shell],
            middleware=[StrazaMiddleware(tool_map={"run_shell": "shell.exec"})],
        )

    def run_agent(self):
        return self.agent.invoke({"messages": [HumanMessage(content="clean up temp")]})

    def test_allowed_run_executes_tool_and_captures(self):
        result = self.run_agent()
        self.assertEqual(self.executed, ["rm -rf /tmp/x"])
        tool_msgs = [m for m in result["messages"] if isinstance(m, ToolMessage)]
        self.assertEqual(len(tool_msgs), 1)
        self.assertEqual(str(tool_msgs[0].content), "ran: rm -rf /tmp/x")
        # One prompt capture, one decision, one reply capture, in order.
        self.assertEqual(self.events(), ["UserPromptSubmit", "PreToolUse", "SessionEnd"])
        payloads = [i["payload"] for i in self.invocations()]
        self.assertEqual(payloads[0]["prompt"], "clean up temp")
        self.assertEqual(payloads[1]["tool_name"], "shell.exec")
        self.assertEqual(payloads[1]["tool_input"], {"command": "rm -rf /tmp/x"})
        self.assertEqual(payloads[2]["prompt_response"], "all done")

    def test_denied_run_blocks_tool_and_surfaces_reason_to_model(self):
        os.environ["STUB_MODE"] = "deny"
        result = self.run_agent()
        self.assertEqual(self.executed, [])  # tool provably never ran
        tool_msgs = [m for m in result["messages"] if isinstance(m, ToolMessage)]
        self.assertEqual(len(tool_msgs), 1)
        self.assertEqual(tool_msgs[0].status, "error")
        self.assertIn("Straza:", str(tool_msgs[0].content))
        self.assertIn("destructive delete blocked", str(tool_msgs[0].content))
        # The model saw the denial and still produced its final answer.
        self.assertEqual(str(result["messages"][-1].content), "all done")
        # Capture is observational: it still flows on a deny.
        self.assertEqual(self.events(), ["UserPromptSubmit", "PreToolUse", "SessionEnd"])


if __name__ == "__main__":
    unittest.main()
