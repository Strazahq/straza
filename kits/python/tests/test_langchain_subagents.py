"""LangChain delegation tests: task.spawn, the observational callback lane,
and the deepagents subagent boundary (stdlib unittest; skipped wholesale when
langchain, or for the boundary lane deepagents, is not installed so the
stdlib-only CI job stays green).

The deepagents lane (SubagentBoundaryTest) is the regression net for the
enforcement boundary: middleware is GRAPH-scoped, so a bare SubAgentMiddleware
leaves the subagent's own tools ungoverned. Those tests pin that limitation and
the two things we can do about it: govern_subagents() for enforcement,
StrazaCallbackHandler for observational reach.

The middleware half of the lane (enforcement, tool mapping, and the capture
hooks) is test_langchain.py. Shared stub wiring lives in langchain_support.py.
"""

import os
import unittest
import uuid

from langchain_support import (
    DEEPAGENTS_SKIP_REASON,
    HAS_DEEPAGENTS,
    HAS_LANGCHAIN,
    SKIP_REASON,
    AIMessage,
    AgentMiddleware,
    ChatGeneration,
    HumanMessage,
    LLMResult,
    StateBackend,
    StrazaCallbackHandler,
    StrazaMiddleware,
    StubbedCase,
    SubAgentMiddleware,
    SystemMessage,
    ToolCallingFakeModel,
    ToolMessage,
    create_agent,
    govern_subagents,
    tool,
    tool_request,
)


@unittest.skipUnless(HAS_LANGCHAIN, SKIP_REASON)
class TaskSpawnMappingTest(StubbedCase):
    """Delegation is itself a governed tool call. deepagents' `task` tool maps
    to canonical `task.spawn` with no configuration, so a policy rule can deny
    delegation outright, the one enforcement lever that still works when the
    spawned subagent's own tools are out of middleware's reach."""

    def decide(self, mw, name, args):
        mw.wrap_tool_call(tool_request(name, args), lambda r: ToolMessage(content="ok", tool_call_id="x"))
        return self.invocations()[-1]["payload"]

    def test_task_tool_maps_to_task_spawn_without_configuration(self):
        p = self.decide(StrazaMiddleware(), "task", {"description": "go dig", "subagent_type": "researcher"})
        self.assertEqual(p["tool_name"], "task.spawn")
        self.assertEqual(p["tool_input"], {"command": "researcher"})  # which subagent
        p = self.decide(StrazaMiddleware(), "Task", {"subagent_type": "digger"})  # claude-code spelling
        self.assertEqual(p["tool_name"], "task.spawn")

    def test_explicit_tool_map_wins_over_the_builtin_default(self):
        p = self.decide(StrazaMiddleware(tool_map={"task": "other"}), "task", {"subagent_type": "r"})
        self.assertEqual(p["tool_name"], "other")

    def test_delegation_can_be_denied_outright(self):
        os.environ["STUB_DENY_TOOLS"] = "task.spawn"
        handled = []
        result = StrazaMiddleware().wrap_tool_call(
            tool_request("task", {"description": "d", "subagent_type": "r"}), handled.append)
        self.assertEqual(handled, [])  # the subagent provably never spawned
        self.assertEqual(result.status, "error")
        self.assertIn("Straza:", str(result.content))


def _llm_result(text):
    return LLMResult(generations=[[ChatGeneration(message=AIMessage(content=text))]])


@unittest.skipUnless(HAS_LANGCHAIN, SKIP_REASON)
class CallbackCaptureTest(StubbedCase):
    """The observational lane. Run-config callbacks are inherited by every
    child run (subgraphs, deepagents subagents); middleware is not."""

    def setUp(self):
        super().setUp()
        self.cb = StrazaCallbackHandler(tool_map={"run_shell": "shell.exec"})

    def cycle(self, reply="done"):
        """One observable turn (prompt, tool, reply), returning the events
        seen at the moment the tool STARTED."""
        rid = uuid.uuid4()
        self.cb.on_chat_model_start({}, [[HumanMessage(content="hi")]], run_id=rid)
        self.cb.on_tool_start({"name": "run_shell"}, "ls -la", run_id=rid, inputs={"command": "ls -la"})
        started = self.events()
        self.cb.on_tool_end(ToolMessage(content="ok", tool_call_id="c1"), run_id=rid)
        self.cb.on_llm_end(_llm_result(reply), run_id=rid)
        return started

    def test_prompt_captured_once_per_distinct_human_message(self):
        first = [SystemMessage(content="sys"), HumanMessage(content="first")]
        self.cb.on_chat_model_start({}, [first], run_id=uuid.uuid4())
        self.cb.on_chat_model_start({}, [first], run_id=uuid.uuid4())  # loop re-entry
        self.cb.on_chat_model_start(
            {}, [[*first, AIMessage(content="a"), HumanMessage(content="second")]],
            run_id=uuid.uuid4())
        self.assertEqual([p["prompt"] for p in self.payloads("UserPromptSubmit")],
                         ["first", "second"])

    def test_reply_captured_at_llm_end_but_never_for_a_pure_tool_call_step(self):
        self.cb.on_llm_end(_llm_result(""), run_id=uuid.uuid4())
        self.assertEqual(self.events(), [])
        self.cb.on_llm_end(_llm_result("kim and bob"), run_id=uuid.uuid4())
        self.assertEqual(self.payloads("SessionEnd")[0]["prompt_response"], "kim and bob")

    def test_tool_post_closes_the_pair_with_the_canonical_kind(self):
        self.assertNotIn("PostToolUse", self.cycle())  # nothing before the tool ran
        self.assertEqual(self.events(), ["UserPromptSubmit", "PostToolUse", "SessionEnd"])
        post = self.payloads("PostToolUse")[0]
        self.assertEqual(post["tool_name"], "shell.exec")
        self.assertEqual(post["tool_input"], {"command": "ls -la"})

    def test_lane_never_emits_a_decision_event(self):
        """Load-bearing. An observational lane must never emit tool.pre: the
        verdict could not be honored, so the audit trail would record a deny
        against a tool call that ran anyway. A tool.post with no matching
        tool.pre is the honest signal: it reads as 'ran, undecided'."""
        self.cycle()
        self.assertNotIn("PreToolUse", self.events())
        # And it cannot start denying by accident: langchain swallows whatever
        # a handler raises unless raise_error is set.
        self.assertFalse(self.cb.raise_error)

    def test_tool_error_closes_the_pair_but_an_unpaired_end_reports_nothing(self):
        rid = uuid.uuid4()
        self.cb.on_tool_start({"name": "run_shell"}, "boom", run_id=rid, inputs={"command": "boom"})
        self.cb.on_tool_error(RuntimeError("boom"), run_id=rid)  # a failed call still ran
        self.cb.on_tool_end(ToolMessage(content="ok", tool_call_id="c1"), run_id=uuid.uuid4())
        self.assertEqual(self.posts(), ["shell.exec"])

    def test_capture_never_raises_without_a_binary(self):
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        self.cycle()


class NoopMiddleware(AgentMiddleware if HAS_LANGCHAIN else object):
    """A stand-in for a caller's own subagent middleware."""

    def __init__(self):
        super().__init__()
        self.tools = []


def _spec(**over):
    """A raw deepagents SubAgent spec; `model`/`tools` are replaced by the
    end-to-end lane, which needs live objects."""
    return {"name": "digger", "description": "digs", "system_prompt": "dig",
            "model": "fake:model", "tools": [], **over}


@unittest.skipUnless(HAS_LANGCHAIN, SKIP_REASON)
class GovernSubagentsTest(unittest.TestCase):
    """govern_subagents() is the only way to close the enforcement hole: it
    puts a StrazaMiddleware into each subagent spec so the caller cannot
    forget to."""

    def test_injects_straza_middleware_innermost(self):
        mine = NoopMiddleware()
        (bare,) = govern_subagents([_spec()])
        self.assertEqual(len(bare["middleware"]), 1)
        (out,) = govern_subagents([_spec(middleware=[mine])], tool_map={"sh": "shell.exec"})
        chain = out["middleware"]
        self.assertIs(chain[0], mine)
        # Last in the list = innermost = closest to execution, so Straza sees
        # the args the tool will actually run with (langchain factory.py:
        # "first = outermost").
        self.assertIsInstance(chain[-1], StrazaMiddleware)
        self.assertEqual(chain[-1].tool_map["sh"], "shell.exec")

    def test_input_specs_are_never_mutated_and_never_share_an_instance(self):
        specs = [_spec(name="a"), _spec(name="b")]
        a, b = govern_subagents(specs)
        self.assertEqual([s.get("middleware") for s in specs], [None, None])
        self.assertIsNot(a["middleware"][-1], b["middleware"][-1])  # per-agent state

    def test_capture_is_off_for_subagents_by_default(self):
        # A subagent's driving instruction is a delegation, not a user prompt:
        # emitting prompt.submit for it would forge the conversation record.
        (out,) = govern_subagents([_spec()])
        self.assertFalse(out["middleware"][-1].capture)
        (opted_in,) = govern_subagents([_spec()], capture=True)
        self.assertTrue(opted_in["middleware"][-1].capture)

    def test_precompiled_subagents_are_refused_not_waved_through(self):
        with self.assertRaises(ValueError) as ctx:
            govern_subagents([{"name": "x", "description": "d", "runnable": object()}])
        message = str(ctx.exception)
        self.assertIn("CompiledSubAgent", message)
        self.assertIn("StrazaCallbackHandler", message)  # points at the fallback

    def test_bad_tool_map_fails_at_wiring_time(self):
        with self.assertRaises(ValueError):
            govern_subagents([_spec()], tool_map={"search": "midpoint:search_users"})
        with self.assertRaises(ValueError):  # even with nothing to inject into
            govern_subagents([], tool_map={"search": "midpoint:search_users"})


@unittest.skipUnless(HAS_DEEPAGENTS, DEEPAGENTS_SKIP_REASON)
class SubagentBoundaryTest(StubbedCase):
    """The enforcement boundary, end to end against real deepagents graphs.

    Parent asks for one `task` delegation; the subagent runs one shell tool and
    reports back. Parent and subagent get their own scripted fake model so a
    test can attach callbacks to one of them without touching the other.
    """

    def build(self, subagents, parent_middleware, parent_model_callbacks=None):
        self.executed = []
        executed = self.executed

        @tool
        def sub_shell(command: str) -> str:
            """Run a shell command."""
            executed.append(command)
            return f"sub ran: {command}"

        parent_model = ToolCallingFakeModel(messages=iter([
            AIMessage(content="", tool_calls=[
                {"name": "task", "args": {"description": "go dig", "subagent_type": "digger"},
                 "id": "t1", "type": "tool_call"}]),
            AIMessage(content="parent done"),
        ]), callbacks=parent_model_callbacks)
        sub_model = ToolCallingFakeModel(messages=iter([
            AIMessage(content="", tool_calls=[
                {"name": "sub_shell", "args": {"command": "rm -rf /sub"}, "id": "s1",
                 "type": "tool_call"}]),
            AIMessage(content="subagent report"),
        ]))
        specs = [dict(s, model=sub_model, tools=[sub_shell]) for s in subagents]
        return create_agent(
            parent_model,
            tools=[],
            middleware=[*parent_middleware,
                        SubAgentMiddleware(backend=StateBackend(), subagents=specs)],
        )

    def run_agent(self, agent, config=None):
        return agent.invoke({"messages": [HumanMessage(content="delegate please")]}, config)

    def test_bare_subagent_middleware_leaves_subagent_tools_ungoverned(self):
        """PINNED LIMITATION (langchain 1.3.14 / deepagents 0.6.12).

        This is the regression net for the boundary, not a wish: if it fails,
        the framework's behavior changed and our docs are now wrong.
        """
        agent = self.build([_spec()], [StrazaMiddleware(capture=False)])
        self.run_agent(agent)
        self.assertEqual(self.executed, ["rm -rf /sub"])
        self.assertEqual(
            self.decisions(), ["task.spawn"],
            "Middleware scope changed. StrazaMiddleware is GRAPH-scoped: deepagents' "
            "create_sub_agent builds each subagent from spec['middleware'] alone and never "
            "propagates the parent's, so only the parent's `task` call is decided and the "
            "subagent's own tools run UNGOVERNED. If the subagent's shell.exec now appears "
            "here, subagents inherit parent middleware. Drop the warning in "
            "integrations/langchain.py's docstring and rewrite govern_subagents' rationale.",
        )

    def test_govern_subagents_puts_the_subagent_tool_under_policy(self):
        agent = self.build(govern_subagents([_spec()], tool_map={"sub_shell": "shell.exec"}),
                           [StrazaMiddleware(capture=False)])
        self.run_agent(agent)
        self.assertEqual(self.decisions(), ["task.spawn", "shell.exec"])
        self.assertEqual(self.executed, ["rm -rf /sub"])

    def test_governed_subagent_tool_is_actually_blocked_on_deny(self):
        """Delegation allowed, the subagent's shell call denied: the boundary
        is real enforcement inside the child graph, not just visibility."""
        os.environ["STUB_DENY_TOOLS"] = "shell.exec"
        agent = self.build(govern_subagents([_spec()], tool_map={"sub_shell": "shell.exec"}),
                           [StrazaMiddleware(capture=False)])
        result = self.run_agent(agent)
        self.assertEqual(self.executed, [])  # subagent's tool provably never ran
        self.assertEqual(self.decisions(), ["task.spawn", "shell.exec"])
        self.assertEqual(str(result["messages"][-1].content), "parent done")

    def test_callback_lane_sees_the_subagent_middleware_cannot_reach(self):
        """Run-config callbacks ARE inherited by subagent graphs. Ungoverned
        stays ungoverned; it just stops being invisible: the subagent's tool
        call and its whole conversation turn land in the audit trail."""
        agent = self.build([_spec()], [StrazaMiddleware(capture=False)])
        handler = StrazaCallbackHandler(tool_map={"sub_shell": "shell.exec"})
        self.run_agent(agent, {"callbacks": [handler]})
        self.assertEqual(self.decisions(), ["task.spawn"])  # still ungoverned
        self.assertEqual(self.posts(), ["shell.exec", "task.spawn"])  # but visible
        self.assertEqual(self.payloads("PostToolUse")[0]["tool_input"],
                         {"command": "rm -rf /sub"})
        self.assertEqual([p["prompt"] for p in self.payloads("UserPromptSubmit")],
                         ["delegate please", "go dig"])
        self.assertEqual([p["prompt_response"] for p in self.payloads("SessionEnd")],
                         ["subagent report", "parent done"])

    def test_bound_run_config_reaches_subagents_too(self):
        """`with_config` is the run config by another name, so it inherits the
        same way `invoke(..., {"callbacks": ...})` does."""
        agent = self.build([_spec()], [StrazaMiddleware(capture=False)])
        handler = StrazaCallbackHandler(tool_map={"sub_shell": "shell.exec"})
        self.run_agent(agent.with_config({"callbacks": [handler]}))
        self.assertEqual(self.posts(), ["shell.exec", "task.spawn"])

    def test_constructor_callbacks_see_no_tool_calls_at_all(self):
        """Why the docs say RUN CONFIG. A handler handed to a component's
        constructor is scoped to that component's own runs: attached to the
        parent model it observes model calls only, not the parent's tool
        calls, let alone the subagent's."""
        handler = StrazaCallbackHandler(tool_map={"sub_shell": "shell.exec"})
        agent = self.build([_spec()], [StrazaMiddleware(capture=False)],
                           parent_model_callbacks=[handler])
        self.run_agent(agent)
        # Positive control: the handler WAS live (it captured the parent's
        # prompt), so the empty tool.post list is scope, not a dead handler.
        self.assertEqual([p["prompt"] for p in self.payloads("UserPromptSubmit")],
                         ["delegate please"])
        self.assertEqual(
            self.posts(), [],
            "Constructor callbacks now observe tool runs. The run-config "
            "requirement in integrations/langchain.py can be relaxed.",
        )


if __name__ == "__main__":
    unittest.main()
