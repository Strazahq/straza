"""OpenAI Agents SDK glue tests: the run-loop layer (stdlib unittest).

Skips cleanly when the `agents` package (openai-agents) is not installed, so
the stdlib-only CI job stays green. A scripted FakeModel drives the real
`Runner.run`, so handoffs, agent-as-tool nesting, guardrails and lifecycle
hooks all fire for real. Offline: no API key, no network. Also pins the two
private SDK attributes the delegation walk depends on (PrivateAttrContractTest;
the failure mode those seams protect is fail-open by omission).

The unit layer (guardrails exercised directly on the SDK's own data objects)
is test_openai_agents.py. The stub straza binary and the shared framework
imports live in openai_agents_support.py.
"""

import asyncio
import os
import sys
import unittest
from types import SimpleNamespace

from openai_agents_support import (
    HAVE_AGENTS,
    SKIP_REASON,
    FakeModel,
    ModelResponse,
    RunContextWrapper,
    RunHooksBase,
    StubEnv,
    Usage,
    agents_sdk,
    call,
    say,
)

from straza_agentkit.integrations import openai_agents as glue  # noqa: E402


@unittest.skipUnless(HAVE_AGENTS, SKIP_REASON)
class RunLoopTest(StubEnv, unittest.TestCase):
    """Drills through the REAL `Runner.run` with a scripted model: handoffs,
    agent-as-tool nesting, prompt capture and reply capture."""

    TOOL_MAP = {
        "triage_shell": "shell.exec",
        "specialist_shell": "shell.exec",
        "auditor_read": "file.read",
        "researcher_fetch": "net.fetch",
        "ask_researcher": "task.spawn",
    }

    def setUp(self):
        super().setUp()
        agents_sdk.set_tracing_disabled(True)  # offline: never export a trace

    def config(self):
        """A RunConfig carrying straza prompt capture, tracing off."""
        return glue.straza_run_config(agents_sdk.RunConfig(tracing_disabled=True))

    def team(self, triage_turns, specialist_turns=(), researcher_turns=()):
        """A four-agent team where every agent owns one EXCLUSIVE tool:

            triage --handoff--> specialist        (bare Agent in handoffs)
                   --handoff--> auditor           (via agents.handoff())
                   --as_tool--> researcher        (nested Runner.run)

        `ran` records tools that actually executed, so a deny is observable as
        an absence."""
        ran = []

        @agents_sdk.function_tool
        def triage_shell(command: str) -> str:
            """Triage's own shell."""
            ran.append(("triage_shell", command))
            return "ok"

        @agents_sdk.function_tool
        def specialist_shell(command: str) -> str:
            """The specialist's exclusive shell - never shared with triage."""
            ran.append(("specialist_shell", command))
            return "ok"

        @agents_sdk.function_tool
        def auditor_read(path: str) -> str:
            """The auditor's exclusive read."""
            ran.append(("auditor_read", path))
            return "ok"

        @agents_sdk.function_tool
        def researcher_fetch(url: str) -> str:
            """The researcher's exclusive fetch."""
            ran.append(("researcher_fetch", url))
            return "ok"

        specialist = agents_sdk.Agent(
            name="specialist",
            tools=[specialist_shell],
            model=FakeModel(*(specialist_turns or ([say("specialist done")],))),
        )
        auditor = agents_sdk.Agent(
            name="auditor", tools=[auditor_read], model=FakeModel([say("audited")])
        )
        researcher = agents_sdk.Agent(
            name="researcher",
            tools=[researcher_fetch],
            model=FakeModel(*(researcher_turns or ([say("researched")],))),
        )
        triage = agents_sdk.Agent(
            name="triage",
            tools=[triage_shell, researcher.as_tool("ask_researcher", "Ask the researcher.")],
            handoffs=[specialist, agents_sdk.handoff(auditor)],
            model=FakeModel(*triage_turns),
        )
        specialist.handoffs = [triage]  # mutual handoff: the graph walk must not spin
        return SimpleNamespace(
            ran=ran, triage=triage, specialist=specialist, auditor=auditor, researcher=researcher
        )

    def guard_names(self, tool):
        return [g.get_name() for g in (tool.tool_input_guardrails or [])]

    # --- enforcement coverage across the delegation graph ---

    def test_guard_agent_covers_the_whole_delegation_graph(self):
        t = self.team([[say("hi")]])
        # The SDK binds tool guardrails per FunctionTool OBJECT, so guarding
        # the entry agent leaves every exclusively-owned tool ungoverned.
        glue.guard_tools(t.triage.tools, tool_map=self.TOOL_MAP)
        self.assertEqual(self.guard_names(t.specialist.tools[0]), [])
        self.assertEqual(self.guard_names(t.auditor.tools[0]), [])
        self.assertEqual(self.guard_names(t.researcher.tools[0]), [])

        self.assertIs(glue.guard_agent(t.triage, tool_map=self.TOOL_MAP), t.triage)
        for agent in (t.triage, t.specialist, t.auditor, t.researcher):
            for tool in agent.tools:
                with self.subTest(agent=agent.name, tool=tool.name):
                    self.assertEqual(self.guard_names(tool), ["straza"])
        glue.guard_agent(t.triage, tool_map=self.TOOL_MAP)  # idempotent
        self.assertEqual(len(t.specialist.tools[0].tool_input_guardrails), 1)

    def test_specialist_tool_is_enforced_after_a_handoff(self):
        os.environ["STUB_MODE"] = "deny"
        t = self.team(
            [[call("transfer_to_specialist")], [say("never reached")]],
            specialist_turns=([call("specialist_shell", command="rm -rf /")], [say("blocked")]),
        )
        glue.guard_agent(t.triage, tool_map=self.TOOL_MAP)
        result = asyncio.run(agents_sdk.Runner.run(t.triage, "clean up", run_config=self.config()))

        self.assertEqual(t.ran, [])  # the specialist's tool never executed
        self.assertEqual(result.final_output, "blocked")
        pre = self.payloads("PreToolUse")
        # Exactly one decision: the specialist's tool. The handoff CALL itself
        # is not a function tool, so the SDK never offers it for gating.
        self.assertEqual([p["tool_name"] for p in pre], ["shell.exec"])
        self.assertEqual(pre[0]["tool_input"], {"command": "rm -rf /"})

    def test_entry_agent_only_guarding_leaves_the_specialist_ungoverned(self):
        """Positive control for the gap `guard_agent` closes: guarding the
        starting agent's tool list alone lets the handoff target run free."""
        os.environ["STUB_MODE"] = "deny"
        t = self.team(
            [[call("transfer_to_specialist")], [say("never reached")]],
            specialist_turns=([call("specialist_shell", command="rm -rf /")], [say("done")]),
        )
        glue.guard_tools(t.triage.tools, tool_map=self.TOOL_MAP)
        asyncio.run(agents_sdk.Runner.run(t.triage, "clean up", run_config=self.config()))
        self.assertEqual([name for name, _ in t.ran], ["specialist_shell"])
        self.assertEqual(self.payloads("PreToolUse"), [])  # engine never consulted

    def test_as_tool_nesting_is_governed_and_inherits_prompt_capture(self):
        t = self.team(
            [[call("ask_researcher", input="dig into acme")], [say("all done")]],
            researcher_turns=(
                [call("researcher_fetch", url="https://acme.test")],
                [say("found it")],
            ),
        )
        glue.guard_agent(t.triage, tool_map=self.TOOL_MAP)
        asyncio.run(agents_sdk.Runner.run(t.triage, "research acme", run_config=self.config()))

        # Both the spawn call and the tool INSIDE the nested run are decided.
        self.assertEqual(
            [p["tool_name"] for p in self.payloads("PreToolUse")], ["task.spawn", "net.fetch"]
        )
        self.assertEqual([name for name, _ in t.ran], ["researcher_fetch"])
        # RunConfig guardrails are inherited by the nested run, so the nested
        # agent's generated input is captured too.
        self.assertEqual(
            [p["prompt"] for p in self.payloads("UserPromptSubmit")],
            ["research acme", "dig into acme"],
        )

    # --- capture ---

    def test_prompt_capture_does_not_depend_on_the_agent_it_hangs_on(self):
        t = self.team(
            [[call("transfer_to_specialist")], [say("x")]],
            specialist_turns=([say("specialist done")],),
        )
        # The trap: agent-level input guardrails run only for the STARTING
        # agent at turn 0, so capture attached to the specialist never fires.
        t.specialist.input_guardrails = [glue.straza_input_guardrail()]
        asyncio.run(agents_sdk.Runner.run(t.triage, "who am i?", run_config=self.config()))
        self.assertEqual([p["prompt"] for p in self.payloads("UserPromptSubmit")], ["who am i?"])

    def test_reply_capture_emits_end_turn_per_replying_turn(self):
        hooks = glue.straza_run_hooks(tool_map=self.TOOL_MAP)
        self.assertIsInstance(hooks, RunHooksBase)  # a real SDK RunHooks
        t = self.team([[call("triage_shell", command="git status")], [say("all clean")]])
        glue.guard_agent(t.triage, tool_map=self.TOOL_MAP)
        result = asyncio.run(
            agents_sdk.Runner.run(t.triage, "status?", hooks=hooks, run_config=self.config())
        )
        self.assertEqual(result.final_output, "all clean")
        # One reply, captured once: the tool-only turn carries no reply text,
        # and the final-output hook does not double-emit the same text.
        self.assertEqual(
            [p.get("prompt_response") for p in self.payloads("SessionEnd")], ["all clean"]
        )

    def test_reply_capture_survives_a_handoff(self):
        hooks = glue.straza_run_hooks(tool_map=self.TOOL_MAP)
        t = self.team(
            [[call("transfer_to_specialist")], [say("never reached")]],
            specialist_turns=([say("the specialist answers")],),
        )
        glue.guard_agent(t.triage, tool_map=self.TOOL_MAP)
        asyncio.run(agents_sdk.Runner.run(t.triage, "go", hooks=hooks, run_config=self.config()))
        self.assertEqual(
            [p.get("prompt_response") for p in self.payloads("SessionEnd")],
            ["the specialist answers"],
        )

    def test_hooks_record_handoff_lineage_and_tool_posts(self):
        hooks = glue.straza_run_hooks(tool_map=self.TOOL_MAP)
        t = self.team(
            [[call("transfer_to_specialist")], [say("x")]],
            specialist_turns=([call("specialist_shell", command="ls")], [say("done")]),
        )
        glue.guard_agent(t.triage, tool_map=self.TOOL_MAP)
        asyncio.run(agents_sdk.Runner.run(t.triage, "go", hooks=hooks, run_config=self.config()))
        post = self.payloads("PostToolUse")
        self.assertEqual([p["tool_name"] for p in post], ["task.spawn", "shell.exec"])
        self.assertIn("triage -> specialist", post[0]["tool_input"]["command"])
        self.assertEqual(post[1]["tool_input"], {"command": "ls"})

    def test_capture_hooks_never_raise_and_never_deny(self):
        agent = agents_sdk.Agent(name="solo", model=FakeModel([say("hi")]))
        ctx = RunContextWrapper(context=None)
        response = ModelResponse(output=[say("hi")], usage=Usage(), response_id=None)
        hooks = glue.straza_run_hooks()

        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        for coro in (
            hooks.on_agent_start(ctx, agent),
            hooks.on_llm_end(ctx, agent, response),
            hooks.on_handoff(ctx, agent, agent),
            hooks.on_tool_end(ctx, agent, None, "out"),
            hooks.on_agent_end(ctx, agent, "hi"),
        ):
            self.assertIsNone(asyncio.run(coro))  # hooks observe; they cannot deny
        self.assertEqual(self.invocations(), [])  # no binary: silent, no raise

        # A policy DENY reaching a capture hook is still not a decision.
        os.environ["STRAZA_BIN"] = f'"{sys.executable}" "{self.stub}"'
        os.environ["STUB_MODE"] = "deny"
        self.assertIsNone(asyncio.run(hooks.on_tool_end(ctx, agent, None, "out")))
        self.assertEqual([p["tool_name"] for p in self.payloads("PostToolUse")], ["other"])

    def test_straza_run_config_attaches_once_and_keeps_base_settings(self):
        base = agents_sdk.RunConfig(workflow_name="ops", tracing_disabled=True)
        cfg = glue.straza_run_config(base)
        self.assertIs(cfg, base)
        self.assertEqual([g.get_name() for g in cfg.input_guardrails], ["straza"])
        glue.straza_run_config(cfg)  # idempotent
        self.assertEqual(len(cfg.input_guardrails), 1)
        self.assertEqual(cfg.workflow_name, "ops")
        self.assertTrue(cfg.tracing_disabled)
        fresh = glue.straza_run_config()
        self.assertEqual([g.get_name() for g in fresh.input_guardrails], ["straza"])


@unittest.skipUnless(HAVE_AGENTS, SKIP_REASON)
class PrivateAttrContractTest(unittest.TestCase):
    """Pin the two private SDK attributes the delegation walk depends on.

    `guard_agent` discovers handoff and as_tool targets through
    `Handoff._agent_ref` and `FunctionTool._agent_instance`. Both reads are
    getattr-with-None so a rename cannot crash, which is the danger: the walk
    would just find fewer agents and delegated tools would silently run
    UNGOVERNED. Fail-open by omission is the one failure mode this package
    must not have.

    The behavioural tests above would also break on a rename, but they would
    report it as "the cross-handoff deny stopped firing". This says why.
    """

    def test_handoff_keeps_a_back_reference_to_its_agent(self):
        target = agents_sdk.Agent(name="specialist", instructions="x")
        ho = agents_sdk.handoff(target)
        self.assertIsNotNone(
            glue._handoff_target(ho),
            "Handoff._agent_ref no longer resolves to the target agent: guard_agent "
            "cannot see handoff targets, so tools they own exclusively would run "
            "UNGOVERNED. Re-derive the discovery seam in _handoff_target.",
        )

    def test_as_tool_tags_the_function_tool_with_its_agent(self):
        target = agents_sdk.Agent(name="nested", instructions="x")
        tool = target.as_tool(tool_name="nested", tool_description="d")
        self.assertIs(
            getattr(tool, "_agent_instance", None),
            target,
            "FunctionTool._agent_instance is gone: guard_agent cannot see as_tool "
            "targets, so tool calls inside a nested Runner.run would be UNGOVERNED. "
            "Re-derive the discovery seam in _reachable_agents.",
        )


if __name__ == "__main__":
    unittest.main()
