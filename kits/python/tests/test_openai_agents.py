"""OpenAI Agents SDK glue tests: the unit layer (stdlib unittest).

Skips cleanly when the `agents` package (openai-agents) is not installed, so
the stdlib-only CI job stays green. The guardrails are exercised directly by
constructing the SDK's own data objects (ToolContext / ToolInputGuardrailData /
RunContextWrapper) and awaiting `.run()`, the exact path the SDK runner
takes. Offline: no API key, no network.

The run-loop layer, a scripted FakeModel driving the real `Runner.run`
(handoffs, agent-as-tool nesting, capture) plus the private-attribute contract
the delegation walk depends on, is test_openai_agents_runloop.py. The stub
straza binary and the shared framework imports live in openai_agents_support.py.
"""

import asyncio
import json
import os
import sys
import unittest

from openai_agents_support import (
    HAVE_AGENTS,
    SKIP_REASON,
    RunContextWrapper,
    StubEnv,
    ToolContext,
    agents_sdk,
)

from straza_agentkit.integrations import openai_agents as glue  # noqa: E402


class ImportHintTest(unittest.TestCase):
    """Runs with or without the SDK installed."""

    def test_missing_sdk_raises_actionable_import_error(self):
        saved = sys.modules.get("agents", None)
        sys.modules["agents"] = None  # makes `import agents` raise ImportError
        try:
            # Every public entry point imports the SDK lazily and says the same
            # actionable thing when it is absent - the kit itself stays
            # dependency-free and importable.
            for build in (
                glue.straza_tool_guardrail,
                glue.straza_input_guardrail,
                glue.straza_run_config,
                glue.straza_run_hooks,
                lambda: glue.guard_tools([]),
                lambda: glue.guard_agent(None),
            ):
                with self.subTest(entry=getattr(build, "__name__", "lambda")):
                    with self.assertRaises(ImportError) as ctx:
                        build()
                    self.assertIn("straza-agentkit[openai-agents]", str(ctx.exception))
        finally:
            if saved is None:
                sys.modules.pop("agents", None)
            else:
                sys.modules["agents"] = saved


@unittest.skipUnless(HAVE_AGENTS, SKIP_REASON)
class GuardrailTest(StubEnv, unittest.TestCase):
    def data_for(self, tool_name, arguments):
        """Build the SDK's guardrail input the way the runner does."""
        if not isinstance(arguments, str):
            arguments = json.dumps(arguments)
        ctx = ToolContext(
            context=None,
            tool_name=tool_name,
            tool_call_id="call_1",
            tool_arguments=arguments,
        )
        return agents_sdk.ToolInputGuardrailData(context=ctx, agent=agents_sdk.Agent(name="t"))

    def run_guardrail(self, guardrail, tool_name, arguments):
        return asyncio.run(guardrail.run(self.data_for(tool_name, arguments)))

    # --- tool-input guardrail: decisions ---

    def test_deny_rejects_with_straza_reason(self):
        os.environ["STUB_MODE"] = "deny"
        gr = glue.straza_tool_guardrail(tool_map={"run_shell": "shell.exec"})
        self.assertIsInstance(gr, agents_sdk.ToolInputGuardrail)
        out = self.run_guardrail(gr, "run_shell", {"command": "rm -rf /tmp/x"})
        self.assertEqual(out.behavior["type"], "reject_content")
        self.assertIn("Straza", out.behavior["message"])
        self.assertIn("destructive delete blocked", out.behavior["message"])
        (inv,) = self.invocations()
        self.assertEqual(inv["harness"], "python-sdk")
        p = inv["payload"]
        self.assertEqual(p["hook_event_name"], "PreToolUse")
        self.assertEqual(p["tool_name"], "shell.exec")
        self.assertEqual(p["tool_input"], {"command": "rm -rf /tmp/x"})

    def test_allow_passes_through(self):
        gr = glue.straza_tool_guardrail(tool_map={"run_shell": "shell.exec"})
        out = self.run_guardrail(gr, "run_shell", {"command": "git status"})
        self.assertEqual(out.behavior["type"], "allow")
        self.assertTrue(out.output_info["allowed"])
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["tool_input"], {"command": "git status"})

    def test_unmapped_tool_uses_default(self):
        out = self.run_guardrail(glue.straza_tool_guardrail(), "frobnicate", {"x": 1})
        self.assertEqual(out.behavior["type"], "allow")
        out2 = self.run_guardrail(
            glue.straza_tool_guardrail(default_tool="task.spawn"), "spawn_worker", {}
        )
        self.assertEqual(out2.behavior["type"], "allow")
        first, second = self.invocations()
        self.assertEqual(first["payload"]["tool_name"], "other")
        self.assertNotIn("tool_input", first["payload"])  # no conventional args extracted
        self.assertEqual(second["payload"]["tool_name"], "task.spawn")

    # --- arg extraction ---

    def test_file_arg_extraction(self):
        cases = [
            ("write_file", "file.write", {"path": "/tmp/a", "content": "x"}, ["/tmp/a"]),
            ("edit_file", "file.edit", {"file_path": "E:\\w\\b.go", "diff": "-"}, ["E:\\w\\b.go"]),
            ("read_files", "file.read", {"paths": ["/tmp/c", "/tmp/d"]}, ["/tmp/c", "/tmp/d"]),
        ]
        tool_map = {name: canonical for name, canonical, _, _ in cases}
        gr = glue.straza_tool_guardrail(tool_map=tool_map)
        for i, (name, canonical, args, want) in enumerate(cases):
            with self.subTest(tool=name):
                out = self.run_guardrail(gr, name, args)
                self.assertEqual(out.behavior["type"], "allow")
                p = self.invocations()[i]["payload"]
                self.assertEqual(p["tool_name"], canonical)
                self.assertEqual(p["tool_input"], {"paths": want})

    def test_net_fetch_url_extraction(self):
        gr = glue.straza_tool_guardrail(tool_map={"fetch": "net.fetch"})
        out = self.run_guardrail(gr, "fetch", {"url": "https://example.com/x", "method": "GET"})
        self.assertEqual(out.behavior["type"], "allow")
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["tool_input"], {"url": "https://example.com/x"})

    def test_dict_arguments_accepted_defensively(self):
        # The SDK contract is a JSON string; a pre-decoded dict must still work.
        gr = glue.straza_tool_guardrail(tool_map={"run_shell": "shell.exec"})
        ctx = ToolContext(
            context=None,
            tool_name="run_shell",
            tool_call_id="call_1",
            tool_arguments={"command": "ls"},  # type: ignore[arg-type]
        )
        data = agents_sdk.ToolInputGuardrailData(context=ctx, agent=agents_sdk.Agent(name="t"))
        out = asyncio.run(gr.run(data))
        self.assertEqual(out.behavior["type"], "allow")
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["tool_input"], {"command": "ls"})

    # --- fail-closed drills ---

    def test_malformed_args_json_fails_closed_without_engine(self):
        gr = glue.straza_tool_guardrail(tool_map={"run_shell": "shell.exec"})
        for bad in ("{not json", '["a", "b"]', '"just a string"'):
            with self.subTest(args=bad):
                out = self.run_guardrail(gr, "run_shell", bad)
                self.assertEqual(out.behavior["type"], "reject_content")
                self.assertIn("Straza", out.behavior["message"])
                self.assertIn("failing closed", out.behavior["message"])
        self.assertEqual(self.invocations(), [])  # engine never consulted

    def test_missing_binary_fails_closed(self):
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "no-such-binary")
        gr = glue.straza_tool_guardrail(tool_map={"run_shell": "shell.exec"})
        out = self.run_guardrail(gr, "run_shell", {"command": "ls"})
        self.assertEqual(out.behavior["type"], "reject_content")
        self.assertIn("failing closed", out.behavior["message"])

    def test_crashing_stub_fails_closed(self):
        os.environ["STUB_MODE"] = "crash"
        gr = glue.straza_tool_guardrail(tool_map={"run_shell": "shell.exec"})
        out = self.run_guardrail(gr, "run_shell", {"command": "ls"})
        self.assertEqual(out.behavior["type"], "reject_content")
        self.assertIn("Straza", out.behavior["message"])

    # --- agent-level input guardrail (capture) ---

    def test_input_guardrail_emits_prompt_capture(self):
        ig = glue.straza_input_guardrail()
        self.assertIsInstance(ig, agents_sdk.InputGuardrail)
        agent = agents_sdk.Agent(name="t")
        result = asyncio.run(ig.run(agent, "what users exist?", RunContextWrapper(context=None)))
        self.assertFalse(result.output.tripwire_triggered)
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["hook_event_name"], "UserPromptSubmit")
        self.assertEqual(inv["payload"]["prompt"], "what users exist?")

    def test_input_guardrail_list_input_and_never_trips(self):
        ig = glue.straza_input_guardrail()
        agent = agents_sdk.Agent(name="t")
        items = [
            {"role": "user", "content": "hello"},
            {"role": "assistant", "content": "ignored"},
            {"role": "user", "content": [{"type": "input_text", "text": "world"}]},
        ]
        result = asyncio.run(ig.run(agent, items, RunContextWrapper(context=None)))
        self.assertFalse(result.output.tripwire_triggered)
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["prompt"], "hello\nworld")
        # Capture stays silent and untripped even with no binary at all.
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        result = asyncio.run(ig.run(agent, "still fine", RunContextWrapper(context=None)))
        self.assertFalse(result.output.tripwire_triggered)

    # --- guard_tools convenience ---

    def test_guard_tools_attaches_and_is_idempotent(self):
        @agents_sdk.function_tool
        def run_shell(command: str) -> str:
            """Run a shell command."""
            return "ok"

        @agents_sdk.function_tool
        def write_file(path: str, content: str) -> str:
            """Write a file."""
            return "ok"

        sentinel = object()  # non-FunctionTool passes through untouched
        tools = glue.guard_tools(
            [run_shell, write_file, sentinel],
            tool_map={"run_shell": "shell.exec", "write_file": "file.write"},
        )
        self.assertEqual(tools[2], sentinel)
        for tool in tools[:2]:
            self.assertEqual([g.get_name() for g in tool.tool_input_guardrails], ["straza"])
        glue.guard_tools(tools)  # second pass must not double-attach
        self.assertEqual(len(run_shell.tool_input_guardrails), 1)

        # The attached guardrail enforces end to end with the canonical mapping.
        os.environ["STUB_MODE"] = "deny"
        out = asyncio.run(
            run_shell.tool_input_guardrails[0].run(
                self.data_for("run_shell", {"command": "rm -rf /tmp/x"})
            )
        )
        self.assertEqual(out.behavior["type"], "reject_content")
        self.assertIn("destructive delete blocked", out.behavior["message"])
        (inv,) = self.invocations()
        self.assertEqual(inv["payload"]["tool_name"], "shell.exec")


if __name__ == "__main__":
    unittest.main()
