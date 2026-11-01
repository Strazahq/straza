# straza-agentkit

Govern Python agents (LangChain, OpenAI Agents SDK, CrewAI, Google ADK,
Pydantic AI, plain SDK loops) with Straza. Every tool call is decided by
the local `straza` binary **before it runs**, against the same signed
policy snapshot, with the same fail-closed semantics and tamper-evident
audit trail as Straza's harness hooks. This package contains **no policy
logic**: it is a stdlib-only shim that shapes events and spawns straza
(the `python-sdk` dialect, `adapters/python-sdk.yaml`).

## Prerequisites

- The `straza` binary on PATH (or `STRAZA_BIN=<path>`), with the Straza
  background service, `straza service`, running on the machine and the
  machine enrolled against your Straza server: `straza enroll` for a
  person, or `straza enroll --headless` for an agent with its own identity,
  as https://docs.straza.ai/guides/govern-an-agent/headless-agents/ shows.
  When you started the service yourself with `STRAZA_HOME`, run the agent
  with the same value, because the `straza` the kit spawns finds the
  service through it.
- Python ≥ 3.9. No dependencies.

## Usage

```python
import straza_agentkit as straza

# Optional but recommended at agent boot: checkin now (session token, signed
# snapshot, capture flags) instead of lazily before the first event. The kit
# fires session.start itself either way; an SDK agent has no harness to.
straza.start_session()

# Explicit check (works in any tool loop):
d = straza.check("shell.exec", command="rm -rf /tmp/x")
if not d.allowed:
    raise straza.StrazaDenied(d.reason)   # surface d.reason to the model

# Decorator:
@straza.guard("net.fetch", paths_from=lambda url, **kw: [url])
def fetch(url: str) -> str: ...

# MCP-style calls:
straza.check("other", app="midpoint", tool_name="search_users")

# A tool of your own, named so policy can allow, hold or deny it by name:
straza.check("other", tool_name="refund_order")

@straza.guard("other", name="refund_order")
def refund(order_id: int) -> str: ...

# Conversation capture (recorded only when policy enables it; never raises):
straza.submit_prompt(user_text)
straza.end_session(reply=final_answer)
```

Canonical tools: `shell.exec`, `file.write`, `file.edit`, `file.read`,
`net.fetch`, `task.spawn`, `other`.

## Framework glue

Each extra pulls only its framework; the core stays dependency-free. A
`tool_map` maps your framework tool names onto the canonical taxonomy so
policy sees `shell.exec`/`file.write`/… instead of `other`; args are
extracted by convention (`command`; `path`/`file_path`/`paths`; `url`).
The glue also sends each tool's own name, so a tool left out of the
`tool_map` reaches policy as `other` with its name, and an admin allows,
holds or denies it by name with a rule of `tools: [other]` and
`toolNames`, with no change to your code and no Straza release.
MCP-style tools do NOT belong in a `tool_map`. Point the framework's MCP
client at Straza's `/mcp` gateway instead (Tier 2; the glue refuses
`app:tool` values so it never smuggles ungoverned MCP).

**LangChain 1.x** (`pip install "straza-agentkit[langchain]"`; written
against langchain 1.3):

```python
from langchain.agents import create_agent
from straza_agentkit.integrations.langchain import StrazaMiddleware

agent = create_agent(model, tools=[sh, save],
    middleware=[StrazaMiddleware(tool_map={"sh": "shell.exec", "save": "file.write"})])
```

`wrap_tool_call` decides every tool call; a deny short-circuits with a
`ToolMessage("Straza: <reason>", status="error")` so the model can react.
`before_model`/`after_model` emit prompt/reply capture (id-deduped).

**Subagents are the enforcement boundary.** Middleware is *graph-scoped*, and
a subagent is a separate graph built from its own `spec["middleware"]`, so a
bare `SubAgentMiddleware` leaves every tool the subagent runs **ungoverned**,
while the parent sees only an opaque `task` call and its summary:

```python
from straza_agentkit.integrations.langchain import (
    StrazaMiddleware, StrazaCallbackHandler, govern_subagents)

agent = create_agent(model, tools=[sh],
    middleware=[StrazaMiddleware(capture=False),
                SubAgentMiddleware(subagents=govern_subagents(specs))])

agent.invoke(state, {"callbacks": [StrazaCallbackHandler()]})  # RUN config
```

`govern_subagents` pushes `StrazaMiddleware` inside each child graph: real
enforcement, a deny blocks the subagent's tool. `StrazaCallbackHandler` must
go on the **run** config (`invoke(..., {"callbacks": [...]})` or
`with_config`): those propagate to every child, while *constructor* callbacks
do not: attached to the chat model, a handler sees no tool events at all.

That handler is observational by construction. It emits `tool.post` and never
`tool.pre`, because `raise_error` is False so its verdict could not be
honored, and recording a decision nothing enforces would be a lie in the audit
trail. A `tool.post` with no matching `tool.pre` is the honest "ran,
undecided" marker.

A `CompiledSubAgent` cannot be governed at all (its graph exists before we
see it), so `govern_subagents` raises rather than returning something that
merely looks governed. Fall back to the callback handler plus a rule denying
`task.spawn`, which `task` now maps to.

**OpenAI Agents SDK** (`pip install "straza-agentkit[openai-agents]"`;
PyPI `openai-agents` 0.18, imports as `agents`):

```python
from agents import Agent, Runner
from straza_agentkit.integrations.openai_agents import (
    guard_agent, straza_run_config, straza_run_hooks)

agent = guard_agent(Agent(name="ops", tools=[sh, save], handoffs=[specialist]))

await Runner.run(agent, prompt,
    run_config=straza_run_config(),   # prompt capture, inherited by nested runs
    hooks=straza_run_hooks())         # reply capture, handoff lineage, tool.post
```

Guardrails attach per `FunctionTool` (idempotent, skips hosted/MCP tools); a
deny uses `ToolGuardrailFunctionOutput.reject_content`: the model sees the
`Straza:` reason as the tool result, no exception. Malformed tool-argument
JSON fails closed without spawning straza.

**Use `guard_agent`, not `guard_tools`, whenever the agent can delegate.** The
SDK binds guardrails per *tool object*, so guarding only the entry agent
leaves a handoff target governed solely for tools it happens to share, and
anything it owns exclusively runs ungoverned. `guard_agent` walks the whole
delegation graph (handoffs, `handoff()` objects, `as_tool()` targets,
transitively, cycle-safe). Likewise put the prompt guardrail on the
`RunConfig`, not the agent: agent-level input guardrails run only at turn 0 of
the *starting* agent, so capture is lost after any handoff.

Two limits are inherent to the SDK and documented rather than papered over:
the **handoff call itself cannot be gated** (a `Handoff` is not a
`FunctionTool`, so there is no guardrail seam; Straza records it and governs
what the delegate *does*), and **`as_tool` nested runs are invisible to the
parent's hooks**, so capture inside one needs
`as_tool(..., hooks=straza_run_hooks())`. Enforcement inside needs nothing
extra: guardrails ride the tool objects.

**Claude Agent SDK** (`pip install "straza-agentkit[claude-agent-sdk]"`;
claude-agent-sdk 0.2): often **zero code**, since the SDK loads settings-file
hooks, so a machine with `straza install` is already governed. The
in-process variant for programmatic hook config:

```python
from claude_agent_sdk import ClaudeAgentOptions
from straza_agentkit.integrations.claude_agent import straza_hooks

options = ClaudeAgentOptions(hooks=straza_hooks())
```

The SDK's hook payloads ARE Claude Code hook payloads, so the callback
relays them verbatim under the claude-code dialect and returns the engine's
`permissionDecision` + reason untouched. `straza_hooks(enforce=False)` is
monitor mode: everything still relayed for audit, nothing ever blocked.

Still manual: **CrewAI** `@before_tool_call` returning `False` on deny;
**Google ADK** `before_tool_callback` returning `{"error": d.reason}`;
**Pydantic AI** `WrapperToolset.call_tool` override. A tool mapped onto a
canonical kind keeps its own name too: `check()` sends `tool_name` beside
the kind, so a by-name rule governs it as well, and a deny from the kind's
own rules still wins.

## The honest trust statement

In-process interception is **advisory** on an open machine: a hostile or
prompt-injected agent can execute code that never enters the framework's
tool loop. Boundary-grade enforcement comes from the MCP gateway (upstream
credentials never exist on the agent's machine) and from sandbox profiles
where straza is the only exec surface (`straza exec`). Policy can
require `managed` attestation for sensitive roles; `python-sdk` sessions
are identifiable and can be given a tighter lane.

## Fail-closed contract

`check`/`guard` deny when straza is missing, not enrolled, times out,
or errors, never a silent allow. `submit_prompt`/`end_session` are
observational and never raise.

## Diagnostics

The kit has no logger of its own on purpose: every decision is made by the
spawned `straza` binary, so its diagnostics are the kit's diagnostics. On the
machine, `straza trace show` prints the content-free decision journal (one line
per decision under the `python-sdk` harness: tool, effect, rule, snapshot,
server status + correlation id, duration), `straza trace on` opens a one-hour
debug window, `straza logs` holds the failures and `straza doctor` the health.
Nothing to configure in Python; see
https://docs.straza.ai/guides/operate/doctor-and-logs/.

Tests: `python -m unittest discover -s tests` (straza is stubbed; the
dialect contract is additionally pinned by Go-side tests in
`internal/agentguard`). The framework-glue suites skip automatically when
their framework is not installed, so the stdlib-only run stays green;
install the extras to exercise them for real.
