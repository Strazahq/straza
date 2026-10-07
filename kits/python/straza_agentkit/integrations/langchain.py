"""LangChain 1.x glue: govern ``create_agent`` tool calls with Straza.

``StrazaMiddleware`` plugs the straza-agentkit core into LangChain's official
agent-middleware seam:

- ``wrap_tool_call`` builds a canonical Straza event from the tool call and
  asks ``straza_agentkit.check()``. A deny SHORT-CIRCUITS: the wrapped handler
  is never invoked and the model receives a ``ToolMessage`` (``status="error"``)
  whose content is the ``Straza: <reason>`` string, so it can react. An allow
  runs the wrapped handler unchanged. The kit is fail-closed: a missing or
  broken straza binary is a deny, never a silent allow.
- ``before_model`` emits prompt capture (``straza_agentkit.submit_prompt``
  with the latest human message; deduplicated so the agent's model/tool loop
  does not resubmit the same prompt every iteration).
- ``after_model`` emits reply capture (``straza_agentkit.end_turn`` with the
  model's reply text; in the python-sdk dialect ``SessionEnd`` is the
  per-turn reply-capture point). Pure tool-call steps carry no reply text and
  emit nothing. Capture is observational and never raises (core guarantee).
  Pass ``capture=False`` to hand this lane to ``StrazaCallbackHandler``.

Tool-name mapping is the constructor's ``tool_map``: LangChain tool name ->
canonical taxonomy (``shell.exec`` / ``file.write`` / ``file.edit`` /
``file.read`` / ``net.fetch`` / ``task.spawn`` / ``other``); unmapped tools
fall back to ``default_tool``. ``task``/``Task`` map to ``task.spawn`` out of
the box (see below) and can be remapped like any other name. The glue is
deliberately canonical-taxonomy-only: MCP tools should reach Straza through
the ``/mcp`` gateway (Tier 2, e.g. via ``langchain-mcp-adapters`` pointed at
strazad), not through local glue: glue never smuggles ungoverned MCP.

Usage::

    from langchain.agents import create_agent
    from straza_agentkit.integrations.langchain import StrazaMiddleware

    agent = create_agent(
        model,
        tools=[run_shell, write_file],
        middleware=[StrazaMiddleware(tool_map={
            "run_shell": "shell.exec",
            "write_file": "file.write",
        })],
    )

Use one ``StrazaMiddleware`` instance per agent (it keeps per-run prompt
capture state). Requires langchain >= 1.0; the straza_agentkit core itself
stays stdlib-only, and this module is the only place langchain is imported.

SUBAGENTS: THE ENFORCEMENT BOUNDARY
-----------------------------------
**Middleware is GRAPH-scoped, so a subagent is NOT governed by its parent's
StrazaMiddleware.** ``create_agent`` composes the middleware list and hands it
to *that graph's* ``ToolNode``; deepagents' ``create_sub_agent`` builds each
subagent from ``spec.get("middleware", [])`` alone and never propagates the
parent's. Left alone, the parent sees one opaque ``task`` call and its summary
while **every tool the subagent runs bypasses policy entirely** (verified
against langchain 1.3.14 / deepagents 0.6.12 and pinned by
``SubagentBoundaryTest.test_bare_subagent_middleware_leaves_subagent_tools_ungoverned``).

Three things close as much of that hole as the framework allows:

1. ``govern_subagents(specs, ...)`` is the enforcement fix. It returns copies of
   your ``SubAgent`` specs with a ``StrazaMiddleware`` injected, so a subagent
   cannot be declared without one. This is the ONLY way to enforce policy
   inside a subagent; there is no hook that lets us do it for you, so if you
   build subagent specs by hand you must remember. A pre-compiled subagent
   (``CompiledSubAgent``, i.e. a spec carrying ``runnable``) is refused
   outright: its graph was built before we could reach it.
2. ``task``/``Task`` -> ``task.spawn`` by default, so delegation is at least
   policy-VISIBLE and a rule can deny it outright (``deny tools:
   [task.spawn]``) when ungoverned subagents are unacceptable. The subagent
   type travels as ``command``, which the python-sdk adapter reads, so a
   rule can also target delegation to one named agent.
3. ``StrazaCallbackHandler`` is the observational lane. Callbacks passed in the
   RUN CONFIG (``agent.invoke(x, {"callbacks": [handler]})``, or an equivalent
   ``with_config``) ARE inherited by every child run, subgraphs and deepagents
   subagents included, because
   ``langchain_core.runnables.config.ensure_config`` seeds each run from the
   ambient parent config. It gives prompt/reply/``tool.post`` visibility where
   middleware cannot reach. **It can never deny**: langchain swallows whatever
   a handler raises unless ``raise_error`` is set, and this lane deliberately
   leaves it False. It therefore emits ``tool.post`` only, never ``tool.pre``:
   a decision event whose verdict cannot be honored would be a lie in the audit
   trail. A ``tool.post`` with no matching ``tool.pre`` is the honest signal
   for "this ran, undecided".

The governed-and-observed setup::

    from straza_agentkit.integrations.langchain import (
        StrazaCallbackHandler, StrazaMiddleware, govern_subagents)

    tool_map = {"run_shell": "shell.exec"}
    agent = create_agent(model, tools=[run_shell], middleware=[
        StrazaMiddleware(tool_map=tool_map, capture=False),
        SubAgentMiddleware(backend=backend,
                           subagents=govern_subagents(specs, tool_map=tool_map)),
    ])
    agent.invoke(state, {"callbacks": [StrazaCallbackHandler(tool_map=tool_map)]})

``capture=False`` on the middleware makes the callback handler the single
capture lane: it sees the parent AND the subagents, so leaving both on would
duplicate every prompt and reply. Enforcement is unaffected by the flag.
"""

from __future__ import annotations

from typing import Any, Callable, Iterable, Optional

try:
    from langchain.agents.middleware import AgentMiddleware
    from langchain_core.callbacks import BaseCallbackHandler
    from langchain_core.messages import AIMessage, HumanMessage, ToolMessage
except ImportError as exc:  # pragma: no cover - exercised only without the extra
    raise ImportError(
        "straza_agentkit's LangChain integration requires langchain >= 1.0: "
        "pip install straza-agentkit[langchain]"
    ) from exc

from straza_agentkit import check, end_turn, submit_prompt, tool_result

__all__ = ["StrazaMiddleware", "StrazaCallbackHandler", "govern_subagents"]

_CANONICAL = frozenset(
    ["shell.exec", "file.write", "file.edit", "file.read", "net.fetch", "task.spawn", "other"]
)
# Delegation is a governed tool call. deepagents names its subagent spawner
# `task`; the claude-code family spells it `Task`. Callers can remap either.
_DEFAULT_TOOL_MAP = {"task": "task.spawn", "Task": "task.spawn"}
# Arg-extraction conventions per canonical kind (first matching key wins).
_PATH_KEYS = ("path", "file_path", "paths")
_SUBAGENT_KEYS = ("subagent_type", "agent_type", "subagent")


def _resolve_tool_map(tool_map: Optional[dict]) -> dict:
    """Merge the caller's map over the built-in defaults, rejecting anything
    outside the canonical taxonomy."""
    supplied = dict(tool_map or {})
    bad = {name: kind for name, kind in supplied.items() if kind not in _CANONICAL}
    if bad:
        raise ValueError(
            f"tool_map values must be canonical Straza kinds {sorted(_CANONICAL)}; got {bad}. "
            "MCP tools belong behind the Straza gateway (Tier 2), not in local glue."
        )
    return {**_DEFAULT_TOOL_MAP, **supplied}


def _resolve_default(default_tool: str) -> str:
    if default_tool not in _CANONICAL:
        raise ValueError(f"default_tool must be one of {sorted(_CANONICAL)}, got {default_tool!r}")
    return default_tool


def _extract(kind: str, args: dict) -> tuple[Optional[str], Optional[list], Optional[str]]:
    """Pull the policy-relevant fields (command, paths, url) out of a LangChain
    tool call's args by convention for the canonical kind. Unknown shapes yield
    nothing, so the call is then decided on the tool kind alone."""
    command: Optional[str] = None
    paths: Optional[list] = None
    url: Optional[str] = None
    if kind == "shell.exec":
        raw = args.get("command")
        if isinstance(raw, str):
            command = raw
        elif isinstance(raw, (list, tuple)):  # e.g. ShellTool-style command lists
            command = "\n".join(str(c) for c in raw)
    elif kind.startswith("file."):
        for key in _PATH_KEYS:
            raw = args.get(key)
            if isinstance(raw, str) and raw:
                paths = [raw]
            elif isinstance(raw, (list, tuple)) and raw:
                paths = [str(p) for p in raw]
            if paths:
                break
    elif kind == "net.fetch":
        raw = args.get("url")
        if isinstance(raw, str):
            url = raw
    elif kind == "task.spawn":
        # Which subagent is being spawned is the policy-relevant discriminator;
        # `command` is the dialect's only free-text field.
        for key in _SUBAGENT_KEYS:
            raw = args.get(key)
            if isinstance(raw, str) and raw:
                command = raw
                break
    return command, paths, url


def _messages(state: Any) -> list:
    if isinstance(state, dict):
        return state.get("messages") or []
    return getattr(state, "messages", None) or []


def _latest_human(messages: Iterable) -> Optional[Any]:
    for message in reversed(list(messages)):
        if isinstance(message, HumanMessage):
            return message
    return None


class StrazaMiddleware(AgentMiddleware):
    """Agent middleware that makes a LangChain ``create_agent`` loop a Straza
    policy enforcement point: every tool call is decided by the straza
    engine before it runs, and prompts/replies are captured (policy-gated
    server-side). See the module docstring for the full contract, including
    why this lane does NOT reach subagents, and what to do about it.

    Args:
        tool_map: LangChain tool name -> canonical Straza taxonomy
            (shell.exec, file.write, file.edit, file.read, net.fetch,
            task.spawn, other). Merged over the built-in ``task``/``Task`` ->
            ``task.spawn`` defaults, which it may override. Unmapped tools use
            ``default_tool``.
        default_tool: canonical kind for tools absent from ``tool_map``
            (default ``"other"``, still policy-decided, just without
            kind-specific argument extraction).
        capture: emit prompt/reply capture from ``before_model``/
            ``after_model``. Set False when a ``StrazaCallbackHandler`` is
            attached at request time, so capture happens exactly once in the
            lane that also sees subagents. Enforcement is unaffected.
    """

    def __init__(
        self,
        tool_map: Optional[dict] = None,
        default_tool: str = "other",
        capture: bool = True,
    ) -> None:
        super().__init__()
        self.tools = []  # no extra tools registered by this middleware
        self.tool_map = _resolve_tool_map(tool_map)
        self.default_tool = _resolve_default(default_tool)
        self.capture = capture
        self._captured_prompts: set = set()

    # ---- enforcement -----------------------------------------------------

    def wrap_tool_call(self, request: Any, handler: Callable) -> Any:
        """Decide the tool call with Straza before executing it. Deny returns
        a ``ToolMessage`` carrying the ``Straza: <reason>`` string (the handler
        is provably not called); allow defers to the wrapped handler."""
        tool_call = request.tool_call or {}
        name = tool_call.get("name") or ""
        args = tool_call.get("args")
        kind = self.tool_map.get(name, self.default_tool)
        command, paths, url = _extract(kind, args if isinstance(args, dict) else {})
        decision = check(kind, command=command, paths=paths, url=url)
        if decision.allowed:
            return handler(request)
        reason = decision.reason or "denied by policy"
        if not reason.startswith("Straza"):
            reason = "Straza: " + reason
        return ToolMessage(
            content=reason,
            tool_call_id=tool_call.get("id") or "",
            name=name or None,
            status="error",
        )

    # ---- capture (observational; the kit guarantees these never raise) ----

    def before_model(self, state: Any, runtime: Any) -> None:
        """Capture the latest human prompt (once per distinct message; the
        agent loop re-enters this hook after every tool round)."""
        if not self.capture:
            return None
        latest = _latest_human(_messages(state))
        if latest is None:
            return None
        text = str(latest.text)
        key = latest.id or f"text:{text}"
        if text and key not in self._captured_prompts:
            self._captured_prompts.add(key)
            submit_prompt(text)
        return None

    def after_model(self, state: Any, runtime: Any) -> None:
        """Capture the model reply. ``end_turn`` (SessionEnd) is the python-sdk
        dialect's per-turn reply-capture point (payload-borne, like gemini's
        AfterAgent) and also drains the audit spool; a pure tool-call step has
        no reply text and emits nothing."""
        if not self.capture:
            return None
        messages = _messages(state)
        last = messages[-1] if messages else None
        if isinstance(last, AIMessage):
            text = str(last.text)
            if text:
                end_turn(reply=text)
        return None


def _generation_text(response: Any) -> str:
    """First non-empty text in an ``LLMResult``. Defensive by design: this runs
    on the capture lane, where a surprising response shape must cost nothing."""
    try:
        for batch in response.generations or []:
            for generation in batch:
                message = getattr(generation, "message", None)
                text = message.text if message is not None else getattr(generation, "text", "")
                if text:
                    return str(text)
    except Exception:  # noqa: BLE001 - capture must never break the agent
        return ""
    return ""


class StrazaCallbackHandler(BaseCallbackHandler):
    """Observational capture that follows the run into subagents.

    Attach it through the RUN CONFIG, ``agent.invoke(state, {"callbacks":
    [handler]})`` or the equivalent ``agent.with_config(...)``, because only
    config callbacks are inherited by child runs. A handler handed to a
    component's constructor (``ChatModel(callbacks=[handler])``) is scoped to
    that component's own runs and sees no tool calls at all, in the parent or
    anywhere else. This is the only lane that sees inside a deepagents
    subagent (see the module docstring's "SUBAGENTS" section).

    Sync by design: under ``ainvoke`` langchain dispatches sync handlers to a
    thread executor, so the blocking ``straza`` spawn never sits on the event
    loop. ``run_inline`` is therefore left False as well.

    **This lane cannot deny.** ``BaseCallbackHandler.raise_error`` stays False,
    so langchain swallows anything a handler raises. Accordingly it emits
    ``tool.post`` (after the tool ran), prompt capture and reply capture, and
    never ``tool.pre``, which would record a verdict nothing could honor.
    Enforcement inside a subagent needs ``govern_subagents``.

    Args:
        tool_map: same shape and defaults as ``StrazaMiddleware``'s.
        default_tool: canonical kind for unmapped tools.
    """

    raise_error = False  # explicit: observational lane, never an enforcement point

    def __init__(self, tool_map: Optional[dict] = None, default_tool: str = "other") -> None:
        super().__init__()
        self.tool_map = _resolve_tool_map(tool_map)
        self.default_tool = _resolve_default(default_tool)
        self._captured_prompts: set = set()
        self._in_flight: dict = {}  # run_id -> (canonical kind, args)

    # ---- prompts / replies ----------------------------------------------

    def on_chat_model_start(self, serialized: Any, messages: Any, **kwargs: Any) -> None:
        """Capture the prompt driving this model call: the user's for the
        root agent, the delegation instruction for a subagent."""
        batch = messages[0] if messages else []
        latest = _latest_human(batch)
        if latest is None:
            return
        text = str(latest.text)
        key = latest.id or f"text:{text}"
        if text and key not in self._captured_prompts:
            self._captured_prompts.add(key)
            submit_prompt(text)

    def on_llm_end(self, response: Any, **kwargs: Any) -> None:
        """Capture the reply this model call produced. A pure tool-call step
        has no text and emits nothing."""
        text = _generation_text(response)
        if text:
            end_turn(reply=text)

    # ---- tool.post -------------------------------------------------------

    def on_tool_start(
        self,
        serialized: Any,
        input_str: str,
        *,
        run_id: Any = None,
        inputs: Optional[dict] = None,
        **kwargs: Any,
    ) -> None:
        """Remember the call so ``on_tool_end`` can close the pair. Nothing is
        emitted here: a tool.pre from this lane would claim a decision."""
        name = (serialized or {}).get("name") or kwargs.get("name") or ""
        kind = self.tool_map.get(name, self.default_tool)
        self._in_flight[run_id] = (kind, inputs if isinstance(inputs, dict) else {})

    def on_tool_end(self, output: Any, *, run_id: Any = None, **kwargs: Any) -> None:
        """Emit canonical ``tool.post``: this tool call completed."""
        self._emit_post(run_id)

    def on_tool_error(self, error: BaseException, *, run_id: Any = None, **kwargs: Any) -> None:
        """A failed call still ran, so close the pair (and free the slot)."""
        self._emit_post(run_id)

    def _emit_post(self, run_id: Any) -> None:
        pending = self._in_flight.pop(run_id, None)
        if pending is None:
            return  # not a call we saw start; nothing honest to report
        kind, args = pending
        command, paths, url = _extract(kind, args)
        tool_result(kind, command=command, paths=paths, url=url)


def govern_subagents(
    subagents: Iterable[dict],
    *,
    tool_map: Optional[dict] = None,
    default_tool: str = "other",
    capture: bool = False,
) -> list[dict]:
    """Return copies of ``SubAgent`` specs with a ``StrazaMiddleware`` injected.

    Subagents run in their own graph and do NOT inherit the parent's
    middleware, so without this their tool calls reach no policy engine at all
    (module docstring, "SUBAGENTS"). Build every subagent list through this
    function and the omission becomes impossible to make silently::

        SubAgentMiddleware(backend=backend,
                           subagents=govern_subagents(specs, tool_map=tool_map))

    The middleware is appended, i.e. INNERMOST: langchain composes
    ``wrap_tool_call`` first-is-outermost, so the last wrapper is the one
    closest to execution and therefore the one that sees the arguments the
    tool will actually run with. Your own subagent middleware keeps its
    relative order in front of it.

    Args:
        subagents: raw ``SubAgent`` specs (mappings). Never mutated.
        tool_map / default_tool: passed to each injected ``StrazaMiddleware``.
        capture: prompt/reply capture inside subagents, default OFF. A
            subagent's driving instruction is a delegation, not a user prompt,
            and recording it as one forges the conversation record. Use
            ``StrazaCallbackHandler`` to capture subagent turns honestly.

    Raises:
        ValueError: for a ``CompiledSubAgent`` (a spec carrying ``runnable``),
            whose graph was built before Straza could reach it, or for a
            non-canonical ``tool_map``; both fail at wiring time rather than
            producing an agent that looks governed and is not.
    """
    _resolve_tool_map(tool_map)  # up front, so an empty spec list still fails loudly
    _resolve_default(default_tool)
    governed: list[dict] = []
    for spec in subagents:
        if not isinstance(spec, dict):
            raise TypeError(f"subagent specs must be mappings, got {type(spec).__name__}")
        if "runnable" in spec:
            raise ValueError(
                f"subagent {spec.get('name', '?')!r} is a CompiledSubAgent: its graph was built "
                "before Straza could reach it, so its tool calls CANNOT be governed. Pass a raw "
                "SubAgent spec (name/description/system_prompt/model/tools) instead, or accept "
                "observational-only coverage via a request-time StrazaCallbackHandler and deny "
                "task.spawn in policy if that is not enough."
            )
        governed.append({
            **spec,
            "middleware": [
                *(spec.get("middleware") or []),
                StrazaMiddleware(tool_map=tool_map, default_tool=default_tool, capture=capture),
            ],
        })
    return governed
