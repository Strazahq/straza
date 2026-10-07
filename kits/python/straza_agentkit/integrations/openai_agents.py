"""OpenAI Agents SDK glue: Straza guardrails (enforcement) + hooks (capture).

Plugs straza-agentkit into the SDK's official seams. Three lanes:

**Enforcement: tool-input guardrails.** ``straza_tool_guardrail()`` returns a
``ToolInputGuardrail`` that runs before every function-tool invocation it is
attached to: it maps the framework tool name to Straza's canonical taxonomy,
extracts the policy-relevant arguments from the model's JSON argument string,
and asks the straza engine via ``straza_agentkit.check()``. A deny becomes
``ToolGuardrailFunctionOutput.reject_content("Straza: <reason>")``: the tool
is NOT executed and the model sees the reason as the tool result, mirroring
the hook deny contract (no exception, the agent can react and continue).

The SDK binds these **per ``FunctionTool`` object**, not per agent
(``_execute_tool_input_guardrails`` reads ``func_tool.tool_input_guardrails``),
so a multi-agent app must guard every agent's tools:
``guard_agent(entry_agent)`` walks the whole delegation graph (handoffs with
bare agents and ``handoff()`` objects, plus ``as_tool()`` targets, transitively)
and is the call to reach for. ``guard_tools(tools)`` remains for a raw list.

**Capture: prompt.** ``straza_run_config()`` returns a ``RunConfig`` carrying
the prompt-capture input guardrail. Attach it at the RUN level, not on an
agent: the SDK runs input guardrails only for the *starting* agent at
``current_turn == 0``, so agent-level capture is silently lost after a
handoff. A ``RunConfig`` is also inherited by nested ``as_tool()`` runs, so
run-level capture fires at turn 0 of each of those too.

**Capture: replies, delegation, tool results.** ``straza_run_hooks()`` returns
a ``RunHooks`` subclass instance for ``Runner.run(hooks=...)``: ``on_llm_end``
emits the model reply via ``end_turn`` (per turn; a reply captured only at
process exit is a reply lost), ``on_handoff`` records the delegation lineage,
``on_tool_end`` closes the ``tool.pre``/``tool.post`` pair. ``RunHooks``
persists across the run loop as the current agent changes, so it covers
handoffs; nested ``as_tool()`` runs need it passed explicitly.

Usage::

    from agents import Agent, Runner, function_tool
    from straza_agentkit.integrations.openai_agents import (
        guard_agent, straza_run_config, straza_run_hooks,
    )

    TOOLS = {"run_shell": "shell.exec", "write_file": "file.write"}
    triage = Agent(name="triage", tools=[run_shell], handoffs=[specialist])

    guard_agent(triage, tool_map=TOOLS)          # every agent in the graph
    result = await Runner.run(
        triage, prompt,
        run_config=straza_run_config(),          # prompt capture
        hooks=straza_run_hooks(tool_map=TOOLS),  # reply + lineage capture
    )

Fail-closed contract: enforced checks inherit the kit's fail-closed behavior
(missing/erroring straza => deny), and a tool call whose argument JSON cannot
be parsed is rejected without execution. Capture is observational: it never
denies and never raises.

Honest limitations (framework-inherent, not oversights):

- **The handoff call itself cannot be gated.** Handoffs are vendor-documented
  to bypass the function-tool pipeline: a ``Handoff`` is not a ``FunctionTool``
  and has no ``tool_input_guardrails`` seam. Straza records the delegation
  after the fact (``on_handoff`` -> ``task.spawn`` tool.post) but cannot deny
  it. Gate what the delegated agent *does* instead: that is what
  ``guard_agent`` guarantees.
- **``as_tool()`` runs are invisible to the parent's hooks.** ``Agent.as_tool``
  spawns a nested ``Runner.run`` with only the ``hooks`` argument passed to
  ``as_tool()`` itself, so the parent sees the spawn call and its summary and
  nothing between. Pass ``as_tool(..., hooks=straza_run_hooks(...))`` for
  capture inside. Enforcement inside needs no extra wiring: guardrails ride on
  the tool objects, which ``guard_agent`` already reached.
- **Hooks cannot deny.** All 14 ``RunHooks`` methods return ``None``; the SDK
  ignores anything else. Enforcement lives in the guardrails, only.
- **In-process interception is advisory** on an open machine (see the kit
  docstring): boundary-grade enforcement is the MCP gateway or a sandbox.

The ``agents`` package (PyPI: ``openai-agents``) is imported lazily so that
straza-agentkit itself stays dependency-free.
"""

from __future__ import annotations

import json
from typing import Any, Iterable, Mapping, Optional

import straza_agentkit as straza

__all__ = [
    "straza_tool_guardrail",
    "straza_input_guardrail",
    "straza_run_config",
    "straza_run_hooks",
    "guard_tools",
    "guard_agent",
]

_GUARDRAIL_NAME = "straza"


def _import_agents():
    """Import the OpenAI Agents SDK, failing with an actionable message."""
    try:
        import agents
    except ImportError as exc:
        raise ImportError(
            "the OpenAI Agents SDK is not installed - run "
            "pip install 'straza-agentkit[openai-agents]' "
            "(PyPI package 'openai-agents', imported as 'agents')"
        ) from exc
    return agents


def _parse_arguments(raw: Any) -> tuple[dict, str]:
    """Parse the tool-call arguments into a dict.

    In this SDK the model's arguments arrive as a raw JSON string
    (``ToolContext.tool_arguments: str``); an already-decoded mapping is
    accepted defensively. Returns ``(args, error)``; a non-empty error means
    the arguments could not be understood and the enforced check must fail
    closed (we cannot build an honest policy payload from garbage).
    """
    if isinstance(raw, Mapping):
        return dict(raw), ""
    if raw is None or (isinstance(raw, str) and not raw.strip()):
        return {}, ""
    if isinstance(raw, str):
        try:
            value = json.loads(raw)
        except ValueError as exc:
            return {}, f"tool arguments are not valid JSON ({exc})"
        if isinstance(value, dict):
            return value, ""
        return {}, f"tool arguments are not a JSON object (got {type(value).__name__})"
    return {}, f"unsupported tool arguments type ({type(raw).__name__})"


def _check_kwargs(canonical: str, args: Mapping[str, Any]) -> dict:
    """Extract the conventional policy-relevant fields for a canonical tool:
    command for shell.exec; path/file_path/paths for file.*; url for net.fetch.
    Everything else is decided on the tool kind alone."""
    kw: dict[str, Any] = {}
    if canonical == "shell.exec":
        cmd = args.get("command")
        if isinstance(cmd, (list, tuple)):
            cmd = " ".join(str(part) for part in cmd)
        if isinstance(cmd, str) and cmd:
            kw["command"] = cmd
    elif canonical.startswith("file."):
        paths: list[str] = []
        for key in ("path", "file_path", "paths"):
            value = args.get(key)
            if isinstance(value, str) and value:
                paths.append(value)
            elif isinstance(value, (list, tuple)):
                paths.extend(str(p) for p in value)
        if paths:
            kw["paths"] = paths
    elif canonical == "net.fetch":
        url = args.get("url")
        if isinstance(url, str) and url:
            kw["url"] = url
    return kw


def _straza_message(reason: str) -> str:
    reason = reason or "denied by policy"
    return reason if reason.startswith("Straza") else "Straza: " + reason


def straza_tool_guardrail(
    tool_map: Optional[Mapping[str, str]] = None,
    default_tool: str = "other",
):
    """Build a ``ToolInputGuardrail`` that gates tool calls through Straza.

    ``tool_map`` maps framework tool names to the canonical taxonomy
    (shell.exec / file.write / file.edit / file.read / net.fetch / task.spawn /
    other); unmapped tools fall back to ``default_tool``. One guardrail
    instance is reusable across any number of tools: it reads the tool name
    and arguments from the SDK's ``ToolInputGuardrailData.context`` at call
    time. Attach it via ``FunctionTool.tool_input_guardrails`` (or use
    :func:`guard_tools`).

    Deny (and every fail-closed path) rejects the call with
    ``reject_content("Straza: <reason>")``: the tool does not run and the
    message is returned to the model in place of the tool result.
    """
    agents_sdk = _import_agents()
    mapping = dict(tool_map or {})

    def _decide(data):
        ctx = data.context
        tool_name = getattr(ctx, "tool_name", "") or ""
        canonical = mapping.get(tool_name, default_tool)
        info: dict[str, Any] = {"tool": canonical, "tool_name": tool_name}
        args, err = _parse_arguments(getattr(ctx, "tool_arguments", ""))
        if err:
            # Enforced check with unintelligible input: fail closed without
            # consulting the engine (no honest payload can be built).
            info["error"] = err
            return agents_sdk.ToolGuardrailFunctionOutput.reject_content(
                message=f"Straza: tool call blocked - {err}; failing closed",
                output_info=info,
            )
        decision = straza.check(canonical, **_check_kwargs(canonical, args))
        info["allowed"] = decision.allowed
        if decision.allowed:
            return agents_sdk.ToolGuardrailFunctionOutput.allow(output_info=info)
        info["reason"] = decision.reason
        return agents_sdk.ToolGuardrailFunctionOutput.reject_content(
            message=_straza_message(decision.reason), output_info=info
        )

    return agents_sdk.ToolInputGuardrail(guardrail_function=_decide, name=_GUARDRAIL_NAME)


def _attach(agents_sdk, tools: Iterable[Any], guardrail) -> None:
    """Attach ``guardrail`` to every ``FunctionTool`` in ``tools``, once."""
    for tool in tools:
        if not isinstance(tool, agents_sdk.FunctionTool):
            continue
        existing = list(tool.tool_input_guardrails or [])
        if any(g.get_name() == _GUARDRAIL_NAME for g in existing):
            continue
        tool.tool_input_guardrails = existing + [guardrail]


def guard_tools(
    tools: Iterable[Any],
    tool_map: Optional[Mapping[str, str]] = None,
    default_tool: str = "other",
):
    """Attach one shared straza guardrail to every ``FunctionTool`` in ``tools``.

    Non-function tools (hosted tools, MCP tools) have no tool-input-guardrail
    seam and pass through untouched. Govern MCP traffic via the gateway
    (Tier 2). Idempotent: a tool already carrying a straza guardrail is left
    alone. Returns the same list object for inline use in ``Agent(tools=...)``.

    This governs exactly the tools handed to it. In a multi-agent app prefer
    :func:`guard_agent`, which cannot miss a delegated agent's tools.
    """
    agents_sdk = _import_agents()
    tools = list(tools)
    _attach(agents_sdk, tools, straza_tool_guardrail(tool_map=tool_map, default_tool=default_tool))
    return tools


def _handoff_target(handoff: Any):
    """The agent behind a ``Handoff`` object. ``handoff()`` stores a weakref to
    it (``Handoff._agent_ref``); a hand-built ``Handoff`` has none, and its
    target must be passed to :func:`guard_agent` in its own call."""
    ref = getattr(handoff, "_agent_ref", None)
    try:
        return ref() if callable(ref) else None
    except Exception:  # noqa: BLE001 - discovery is best effort, never fatal
        return None


def _reachable_agents(agents_sdk, root: Any) -> list:
    """Every agent reachable from ``root`` by delegation: handoffs (bare agents
    and ``handoff()`` objects) and ``as_tool()`` targets (``as_tool`` tags the
    ``FunctionTool`` it builds with ``_agent_instance``), transitively.
    Cycle-safe, because mutual handoffs are common and must not spin."""
    seen: set[int] = set()
    found: list = []
    stack = [root]
    while stack:
        agent = stack.pop()
        if agent is None or id(agent) in seen or not hasattr(agent, "tools"):
            continue
        seen.add(id(agent))
        found.append(agent)
        for entry in list(getattr(agent, "handoffs", None) or []):
            stack.append(entry if isinstance(entry, agents_sdk.Agent) else _handoff_target(entry))
        for tool in list(getattr(agent, "tools", None) or []):
            stack.append(getattr(tool, "_agent_instance", None))
    return found


def guard_agent(
    agent: Any,
    tool_map: Optional[Mapping[str, str]] = None,
    default_tool: str = "other",
):
    """Govern ``agent`` AND every agent it can delegate to (the call to use).

    The SDK binds tool guardrails per ``FunctionTool`` object, so an agent
    reached by a handoff is governed only for the tool objects it happens to
    share with the entry agent; anything it owns exclusively would run
    ungoverned. This walks the delegation graph (handoffs, ``handoff()``
    objects, ``as_tool()`` targets, transitively) and attaches one shared
    straza guardrail to every ``FunctionTool`` it finds, including the
    ``as_tool()`` spawn calls themselves.

    Idempotent, cycle-safe, and returns ``agent`` for inline use. Call it once
    per root agent, after the graph is assembled; agents that are not reachable
    (a hand-built ``Handoff`` keeps no back-reference to its target) need their
    own call.

    Two things this cannot do, both framework-inherent: it cannot gate the
    handoff call itself (handoffs bypass the function-tool pipeline), and it
    does not add capture; see :func:`straza_run_config` and
    :func:`straza_run_hooks`.
    """
    agents_sdk = _import_agents()
    guardrail = straza_tool_guardrail(tool_map=tool_map, default_tool=default_tool)
    for target in _reachable_agents(agents_sdk, agent):
        _attach(agents_sdk, list(getattr(target, "tools", None) or []), guardrail)
    return agent


def _prompt_text(value: Any) -> str:
    """Best-effort extraction of the user-authored text from the SDK's agent
    input (a plain string or a list of response input items)."""
    if isinstance(value, str):
        return value
    parts: list[str] = []
    if isinstance(value, (list, tuple)):
        for item in value:
            if not isinstance(item, Mapping):
                continue
            if item.get("role") not in (None, "user"):
                continue
            content = item.get("content")
            if isinstance(content, str):
                parts.append(content)
            elif isinstance(content, (list, tuple)):
                for chunk in content:
                    if isinstance(chunk, Mapping) and isinstance(chunk.get("text"), str):
                        parts.append(chunk["text"])
    return "\n".join(p for p in parts if p)


def straza_input_guardrail():
    """Build an ``InputGuardrail`` that emits ``prompt.submit`` conversation
    capture via ``straza_agentkit.submit_prompt``.

    Observational only: the tripwire is never triggered, capture failures are
    swallowed (the kit's capture lane already never raises), and
    ``run_in_parallel=False`` so the prompt lands in the audit trail before the
    first tool decision.

    Attach it through :func:`straza_run_config`, not through
    ``Agent(input_guardrails=[...])``: the SDK runs input guardrails only for
    the starting agent at turn 0, so an agent-level attachment captures nothing
    once a handoff moves the run to another agent, and nothing at all inside a
    nested ``as_tool()`` run. Attaching in both places double-captures.
    """
    agents_sdk = _import_agents()

    def _capture(context, agent, agent_input):
        try:
            text = _prompt_text(agent_input)
            if text:
                straza.submit_prompt(text)
        except Exception:  # noqa: BLE001 - capture must never break the run
            pass
        return agents_sdk.GuardrailFunctionOutput(output_info=None, tripwire_triggered=False)

    return agents_sdk.InputGuardrail(
        guardrail_function=_capture, name=_GUARDRAIL_NAME, run_in_parallel=False
    )


def straza_run_config(base: Any = None):
    """Return a ``RunConfig`` that carries Straza prompt capture.

    Run-level is the only attachment point that survives delegation: the SDK
    reads ``starting_agent.input_guardrails + run_config.input_guardrails`` at
    ``current_turn == 0``, and a nested ``as_tool()`` run inherits the parent's
    ``RunConfig``, so capture fires at turn 0 of the outer run and of every
    nested one, whichever agent is current.

    Pass an existing ``RunConfig`` as ``base`` to keep its settings; it is
    updated in place (like :func:`guard_tools`) and returned. Idempotent.
    """
    agents_sdk = _import_agents()
    config = agents_sdk.RunConfig() if base is None else base
    existing = list(config.input_guardrails or [])
    if not any(g.get_name() == _GUARDRAIL_NAME for g in existing):
        config.input_guardrails = existing + [straza_input_guardrail()]
    return config


def _response_text(response: Any) -> str:
    """The assistant text of one model response (empty for a pure tool-call
    turn, which therefore captures no reply)."""
    parts: list[str] = []
    for item in getattr(response, "output", None) or []:
        if getattr(item, "type", "") != "message":
            continue
        for chunk in getattr(item, "content", None) or []:
            text = getattr(chunk, "text", None)
            if isinstance(text, str) and text:
                parts.append(text)
    return "\n".join(parts)


def _output_text(output: Any) -> str:
    """An agent's final output as capture text (structured outputs stringify)."""
    if isinstance(output, str):
        return output
    return "" if output is None else str(output)


_hooks_type = None


def _build_hooks_type(agents_sdk):
    """Define the ``RunHooks`` subclass on first use, so the SDK stays a lazy
    import, and the type is stable across calls (so ``isinstance`` works)."""
    global _hooks_type
    if _hooks_type is not None:
        return _hooks_type

    class StrazaRunHooks(agents_sdk.RunHooks):
        """Straza conversation/audit capture for ``Runner.run(hooks=...)``.

        CAPTURE ONLY. Every ``RunHooks`` method returns ``None`` and the SDK
        ignores anything else, so these hooks cannot deny, delay or alter a
        call; enforcement is the tool-input guardrail's job
        (:func:`guard_agent`). Nothing here raises, either: a broken or absent
        straza degrades to silence rather than breaking the agent.

        The instance persists across the whole run loop as the current agent
        changes, so handoffs are covered end to end. Nested ``as_tool()`` runs
        are not: pass hooks to ``as_tool(hooks=...)`` for those. Use one
        instance per run (it holds per-run reply state).
        """

        def __init__(self, tool_map=None, default_tool="other"):
            self._map = dict(tool_map or {})
            self._default = default_tool
            self._last_reply = ""

        def _capture_reply(self, text: str) -> None:
            """Close one turn with the model's reply, once. Consecutive
            duplicates are dropped: the SDK reports the last turn's text twice
            (``on_llm_end`` then ``on_agent_end``) for a plain text answer."""
            try:
                if text and text != self._last_reply:
                    self._last_reply = text
                    straza.end_turn(reply=text)
            except Exception:  # noqa: BLE001 - capture must never break the run
                pass

        async def on_agent_start(self, context, agent) -> None:
            """Check the session in as early as possible (idempotent) and start
            a fresh reply window for the agent taking over."""
            try:
                straza.start_session()
                self._last_reply = ""
            except Exception:  # noqa: BLE001
                pass

        async def on_llm_end(self, context, agent, response) -> None:
            """The reply lane: one ``session.end`` per turn that carries text.
            Per turn, not per process: a reply captured only at shutdown is
            lost whenever the process is killed."""
            self._capture_reply(_response_text(response))

        async def on_agent_end(self, context, agent, output) -> None:
            """Backstop for the final output (structured outputs, and any turn
            whose text ``on_llm_end`` could not read)."""
            self._capture_reply(_output_text(output))

        async def on_handoff(self, context, from_agent, to_agent) -> None:
            """Delegation lineage, recorded as a ``task.spawn`` tool.post: the
            trail reads as a spawn that completed, carrying ``handoff: <from>
            -> <to>``. After the fact by construction: the handoff call bypasses
            the function-tool pipeline, so it can be observed but never gated."""
            try:
                source = getattr(from_agent, "name", "?")
                target = getattr(to_agent, "name", "?")
                straza.tool_result("task.spawn", command=f"handoff: {source} -> {target}")
            except Exception:  # noqa: BLE001
                pass

        async def on_tool_end(self, context, agent, tool, result) -> None:
            """Close the ``tool.pre``/``tool.post`` pair the guardrail opened.
            (There is deliberately no ``on_tool_start`` counterpart: a second
            pre-event with no decision authority would misrepresent the trail.)
            """
            try:
                name = getattr(context, "tool_name", "") or getattr(tool, "name", "") or ""
                canonical = self._map.get(name, self._default)
                args, err = _parse_arguments(getattr(context, "tool_arguments", ""))
                straza.tool_result(canonical, **({} if err else _check_kwargs(canonical, args)))
            except Exception:  # noqa: BLE001
                pass

    _hooks_type = StrazaRunHooks
    return _hooks_type


def straza_run_hooks(
    tool_map: Optional[Mapping[str, str]] = None,
    default_tool: str = "other",
):
    """Build the Straza capture hooks: a ``RunHooks`` subclass instance for
    ``Runner.run(hooks=...)`` (and for ``as_tool(hooks=...)``, which is the
    only way to see inside a nested agent-as-tool run).

    Captures the model reply per turn (``end_turn``), the delegation lineage of
    every handoff, and a ``tool.post`` for every tool call, using the same
    ``tool_map`` / ``default_tool`` taxonomy as :func:`guard_agent`, so the
    pre/post pair agrees. Observational: it cannot deny and never raises.
    Use one instance per run.
    """
    return _build_hooks_type(_import_agents())(tool_map=tool_map, default_tool=default_tool)
