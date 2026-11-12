"""straza-agentkit: govern Python agents with Straza.

The package is a deliberately thin shim (stdlib only, no policy logic): every
decision spawns the ``straza`` binary (the same engine, signed policy
snapshot, fail-closed semantics, and audit spool the harness hooks use) with
a ``python-sdk`` dialect payload on stdin (adapters/python-sdk.yaml in the
Straza repo). The Python agents page of the docs gives the architecture
and the honest trust statement: in-process interception is advisory on an
open machine; boundary-grade enforcement is the MCP gateway or a sandbox
profile where straza is the only exec surface.

Usage::

    import straza_agentkit as straza

    # Optional fail-fast checkin at boot; otherwise fired lazily before the
    # first event (SDK agents have no harness to fire session.start):
    straza.start_session()

    # Explicit check (any framework, any tool loop):
    d = straza.check("shell.exec", command="rm -rf /tmp/x")
    if not d.allowed:
        raise straza.StrazaDenied(d.reason)

    # Or guard a tool function:
    @straza.guard("net.fetch", paths_from=lambda url, **kw: [url])
    def fetch(url): ...

    # Conversation capture (policy-gated server-side, observational here).
    # end_turn goes after EVERY turn; a reply captured only at process exit
    # is lost whenever the process is killed:
    straza.submit_prompt(user_text)
    straza.end_turn(reply=answer)          # ... per turn
    straza.end_session(reply=final_answer)  # ... and the last one

Fail-closed contract: if straza is missing, unenrolled, or the decision
cannot be obtained for ANY reason, ``check`` returns a deny, never a silent
allow. Capture calls are observational and never raise.
"""

from __future__ import annotations

import json
import os
import shlex
import shutil
import subprocess  # nosec B404 - spawning straza IS the design
import threading
import uuid
from dataclasses import dataclass
from typing import Any, Callable, Iterable, Mapping, Optional

__all__ = [
    "Decision",
    "StrazaDenied",
    "check",
    "guard",
    "start_session",
    "submit_prompt",
    "tool_result",
    "end_turn",
    "end_session",
]

_HARNESS = "python-sdk"
_DENY_EXIT = 2
_TIMEOUT_SECS = 30  # a hung binary must not hang the agent forever; timeout = deny

# One session id per process by default: threads the whole agent run through
# a single governed conversation, mirroring how a harness session behaves.
_session_id = os.environ.get("STRAZA_SESSION_ID") or ("py-" + uuid.uuid4().hex[:12])

# SDK agents have no harness to fire session.start, and enforced decisions
# fail closed without a checked-in session, so the kit fires it itself:
# lazily before the first event of any kind, or explicitly via
# start_session() at agent boot (fail-fast, earlier capture).
_session_started = False
_session_lock = threading.Lock()


class StrazaDenied(RuntimeError):
    """Raised by @guard when policy denies the call."""

    def __init__(self, reason: str, rule_id: str = ""):
        super().__init__(reason)
        self.reason = reason
        self.rule_id = rule_id


@dataclass(frozen=True)
class Decision:
    allowed: bool
    reason: str = ""
    raw: Optional[dict] = None


def _straza_argv() -> list[str]:
    """Locate straza: $STRAZA_BIN (may include args; split on
    whitespace with double-quoted segments honored, so Windows paths keep their
    backslashes) or PATH lookup. Empty result = fail closed at the call site."""
    override = os.environ.get("STRAZA_BIN", "")
    if override:
        parts = shlex.split(override, posix=False)
        return [p[1:-1] if len(p) >= 2 and p[0] == p[-1] and p[0] in "\"'" else p for p in parts]
    found = shutil.which("straza")
    return [found] if found else []


def start_session() -> None:
    """Fire the session.start event: straza checks in with strazad (session
    token, signed policy snapshot, capture flags). Idempotent per process, and
    called automatically before the first event the kit sends. Calling it
    explicitly at agent boot just moves the checkin earlier (fail-fast).
    Observational: never raises."""
    global _session_started
    with _session_lock:
        if _session_started:
            return
        _session_started = True
    _invoke(_payload("SessionStart"), enforce=False)


def _invoke(payload: dict, enforce: bool) -> Decision:
    """Spawn `straza hook` with the python-sdk dialect payload. The exit
    code is the decision (0 allow, 2 deny, per the hook-profile contract); the
    stdout JSON carries the deny reason. Every failure mode of an ENFORCED
    call is a deny; observational calls degrade to allow-and-move-on."""
    if payload.get("hook_event_name") != "SessionStart":
        start_session()
    argv = _straza_argv()
    fallback = Decision(False, "Straza: straza binary not found. Install it and enroll "
                               "(https://github.com/strazahq/straza), or set STRAZA_BIN")
    if not argv:
        return fallback if enforce else Decision(True)
    env = dict(os.environ, STRAZA_HARNESS=_HARNESS)
    try:
        proc = subprocess.run(  # nosec B603 - fixed binary, no shell
            argv + ["hook"],
            input=json.dumps(payload, default=str).encode("utf-8"),
            capture_output=True,
            timeout=_TIMEOUT_SECS,
            env=env,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        if not enforce:
            return Decision(True)
        return Decision(False, f"Straza: decision unavailable ({exc.__class__.__name__}), failing closed")

    if proc.returncode == 0:
        return Decision(True, raw=_stdout_json(proc.stdout))
    reason = ""
    out = _stdout_json(proc.stdout)
    if out:
        hso = out.get("hookSpecificOutput") or {}
        reason = hso.get("permissionDecisionReason", "") or out.get("reason", "")
    if not reason:
        reason = (proc.stderr or b"").decode("utf-8", "replace").strip() or "Straza: denied by policy"
    if proc.returncode != _DENY_EXIT and not enforce:
        return Decision(True)
    return Decision(False, reason, raw=out)


def _stdout_json(raw: bytes) -> Optional[dict]:
    try:
        v = json.loads(raw.decode("utf-8"))
        return v if isinstance(v, dict) else None
    except (ValueError, UnicodeDecodeError):
        return None


def _payload(event: str, **extra: Any) -> dict:
    p = {
        "hook_event_name": event,
        "session_id": _session_id,
        "cwd": os.getcwd(),
        "harness_version": __version__,
    }
    p.update({k: v for k, v in extra.items() if v not in (None, "", [], {})})
    return p


__version__ = "0.4.0"


def check(
    tool: str,
    *,
    command: Optional[str] = None,
    paths: Optional[Iterable[str]] = None,
    url: Optional[str] = None,
    app: Optional[str] = None,
    tool_name: Optional[str] = None,
    args: Optional[Mapping[str, Any]] = None,
) -> Decision:
    """Decide one tool call before running it. `tool` is the canonical
    taxonomy: shell.exec, file.write, file.edit, file.read, net.fetch,
    task.spawn, other; or pass app= + tool_name= for an MCP-style call
    (sent as the mcp__<app>__<tool> form the dialect parses). tool_name
    without app is the framework's own name of the tool, which policy can
    allow, hold or deny by name (sent as native_tool_name). args is the
    call's own arguments, sent for a tool of kind other, which has no
    command, path or url to describe it: Straza records them and an
    approval binds them. An empty mapping is a call with no arguments, and
    None is arguments the caller never saw. A value JSON cannot carry is
    sent as its text. Any other kind ignores args."""
    return _invoke(_tool_payload("PreToolUse", tool, command, paths, url, app, tool_name, args), enforce=True)


def _tool_payload(
    event: str,
    tool: str,
    command: Optional[str],
    paths: Optional[Iterable[str]],
    url: Optional[str],
    app: Optional[str],
    tool_name: Optional[str],
    args: Optional[Mapping[str, Any]] = None,
) -> dict:
    """The payload of one tool event, shared by check() and tool_result()."""
    native, own_name = tool, tool_name
    if app and tool_name:
        native, own_name = f"mcp__{app}__{tool_name}", None
    tool_input: dict[str, Any] = {}
    if command is not None:
        tool_input["command"] = command
    if paths is not None:
        tool_input["paths"] = list(paths)
    if url is not None:
        tool_input["url"] = url
    payload = _payload(event, tool_name=native, native_tool_name=own_name, tool_input=tool_input)
    if args is not None and native == "other":
        # Set after _payload, which drops an empty value: an empty mapping
        # is the observed call with no arguments and must still be sent.
        payload["tool_input"] = dict(args)
    return payload


def guard(
    tool: str,
    *,
    name: Optional[str] = None,
    command_from: Optional[Callable[..., str]] = None,
    paths_from: Optional[Callable[..., Iterable[str]]] = None,
    args_from: Optional[Callable[..., Mapping[str, Any]]] = None,
) -> Callable:
    """Decorator: decide before every call of the wrapped tool function.
    name is the tool's own name, which policy can allow, hold or deny by
    name. command_from/paths_from derive the policy-relevant fields from the
    call's own arguments; without them the call is decided on the tool kind
    alone. args_from answers the arguments of a call to a tool of kind
    other as a mapping, which check() sends as args. Deny raises
    StrazaDenied (frameworks surface the message to the model)."""

    def wrap(fn: Callable) -> Callable:
        def guarded(*args: Any, **kwargs: Any) -> Any:
            cmd = command_from(*args, **kwargs) if command_from else None
            pth = paths_from(*args, **kwargs) if paths_from else None
            own = args_from(*args, **kwargs) if args_from else None
            d = check(tool, command=cmd, paths=pth, tool_name=name, args=own)
            if not d.allowed:
                raise StrazaDenied(d.reason)
            return fn(*args, **kwargs)

        guarded.__name__ = getattr(fn, "__name__", "guarded")
        guarded.__doc__ = fn.__doc__
        guarded.__wrapped__ = fn
        return guarded

    return wrap


def submit_prompt(text: str) -> None:
    """Report a user prompt (conversation capture; recorded only when policy
    enables capture for this session). Observational: never raises."""
    if text:
        _invoke(_payload("UserPromptSubmit", prompt=text), enforce=False)


def tool_result(
    tool: str,
    *,
    command: Optional[str] = None,
    paths: Optional[Iterable[str]] = None,
    url: Optional[str] = None,
    app: Optional[str] = None,
    tool_name: Optional[str] = None,
) -> None:
    """Report that a tool call COMPLETED (canonical `tool.post`). Purely
    observational, since the decision was already made at `check()`; this closes
    the pair in the audit trail the way a harness's PostToolUse does.
    Arguments mirror `check()` without `args`: a completed call is not
    decided again, so its arguments are not sent again. Never raises."""
    _invoke(_tool_payload("PostToolUse", tool, command, paths, url, app, tool_name), enforce=False)


def end_turn(reply: Optional[str] = None) -> None:
    """Close ONE agent turn, carrying the model's reply for capture
    (payload-borne, since SDK agents have no transcript file). Also drains the
    local audit spool.

    Call this after every turn, not just at shutdown. A harness fires the
    same canonical `session.end` per turn (claude-code's `Stop`), and for
    exactly the reason that matters here: a reply captured only at process
    exit is a reply LOST whenever the process is killed or long-lived.

    `end_session` is the identical call under its original name, kept for the
    final turn and for compatibility. Observational: never raises."""
    _invoke(_payload("SessionEnd", prompt_response=reply), enforce=False)


def end_session(reply: Optional[str] = None) -> None:
    """Close the run's final turn (see `end_turn`, which this delegates to).

    `end_session` is the original name of `end_turn`, kept for the last turn
    and for compatibility. Prefer `end_turn` after every turn. Observational:
    never raises."""
    end_turn(reply)
