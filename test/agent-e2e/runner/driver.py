#!/usr/bin/env python3
"""Agent-e2e driver: real model-driven sessions against a real
strazad, governed by the real straza binary through straza-agentkit.

Stdlib only, mirroring the kit's own constraint. The flow:

  1. wait for strazad + ollama; pull the model if absent
  2. admin bootstrap: scripted OIDC device flow (the same endpoints a human
     browser hits), then over the admin API: apply+activate the e2e
     PolicySet, create the e2e NHI, register its keygen public key
  3. `straza enroll --headless`: no browser anywhere
  4. fire the python-sdk SessionStart event (checkin: session token +
     signed snapshot + capture flags land client-side)
  5. run EPISODES: elaborate prompts to the local model, which calls tools;
     every tool call is decided by straza_agentkit.check() against the
     signed snapshot; denials feed the Straza reason back to the model
  6. assert the governance pipeline end-to-end: at least one allowed exec,
     a deny with a policy reason, audit rows server-side, captured
     transcript turns findable by marker

Episodes tolerate model flakiness (tiny models sometimes emit no tool
call): each retries once with a firmer prompt, then falls back to a direct
kit.check() so the GOVERNANCE assertions always run; the summary reports
which episodes were model-driven vs fallback.
"""

import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

STRAZA = os.environ.get("STRAZA_SERVER", "http://strazad:8420").rstrip("/")
OLLAMA = os.environ.get("OLLAMA_URL", "http://ollama:11434").rstrip("/")
MODEL = os.environ.get("E2E_MODEL", "qwen2.5:1.5b")
ADMIN_USER = os.environ.get("STRAZA_ADMIN_USER", "admin")
ADMIN_PASSWORD = os.environ.get("STRAZA_ADMIN_PASSWORD", "")
NHI_USER = "e2e-agent"
POLICY_FILE = "/opt/e2e/policy/e2e-guardrails.yaml"
SANDBOX = "/work"
MARKER = "E2E-MARKER-" + uuid.uuid4().hex[:10]


# --- tiny HTTP helpers (stdlib) --------------------------------------------

def _req(method, url, body=None, headers=None, timeout=60):
    data = None
    if body is not None:
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
    r = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def _json(method, url, body=None, bearer=None, timeout=60):
    headers = {"Content-Type": "application/json"}
    if bearer:
        headers["Authorization"] = "Bearer " + bearer
    code, raw = _req(method, url, body, headers, timeout)
    try:
        return code, json.loads(raw) if raw else None
    except ValueError:
        return code, raw.decode("utf-8", "replace")


def _form(url, fields, timeout=30):
    data = urllib.parse.urlencode(fields).encode()
    code, raw = _req("POST", url, data,
                     {"Content-Type": "application/x-www-form-urlencoded"}, timeout)
    try:
        return code, json.loads(raw) if raw else None
    except ValueError:
        return code, raw.decode("utf-8", "replace")


def wait_for(name, url, tries=120):
    for _ in range(tries):
        try:
            code, _ = _req("GET", url, timeout=5)
            if code < 500:
                print(f"[e2e] {name} is up")
                return
        except OSError:
            pass
        time.sleep(1)
    raise SystemExit(f"[e2e] FATAL: {name} never became reachable at {url}")


# --- phase 1: model availability -------------------------------------------

def ensure_model():
    code, tags = _json("GET", OLLAMA + "/api/tags")
    have = {m.get("name", "") for m in (tags or {}).get("models", [])}
    if MODEL in have or MODEL + ":latest" in have:
        print(f"[e2e] model {MODEL} already present")
        return
    print(f"[e2e] pulling model {MODEL} (first run only, may take minutes)…")
    code, out = _json("POST", OLLAMA + "/api/pull",
                      {"model": MODEL, "stream": False}, timeout=1800)
    if code != 200 or (isinstance(out, dict) and out.get("status") != "success"):
        raise SystemExit(f"[e2e] FATAL: model pull failed: {code} {out}")
    print(f"[e2e] model {MODEL} ready")


# --- phase 2: admin bootstrap ----------------------------------------------

def admin_token():
    """Run the built-in issuer's RFC 8628 device flow non-interactively:
    request a code, approve it with the admin credentials (the same POST a
    human's browser form performs), poll the token endpoint."""
    if not ADMIN_PASSWORD:
        raise SystemExit("[e2e] FATAL: STRAZA_ADMIN_PASSWORD not set (run.sh parses "
                         "it from the virgin-boot strazad log)")
    code, auth = _form(STRAZA + "/oidc/device_authorization",
                       {"client_id": "strazactl", "scope": "openid"})
    if code != 200:
        raise SystemExit(f"[e2e] FATAL: device_authorization: {code} {auth}")
    code, _ = _form(STRAZA + "/oidc/device", {
        "user_code": auth["user_code"],
        "username": ADMIN_USER, "password": ADMIN_PASSWORD,
    })
    if code >= 400:
        raise SystemExit(f"[e2e] FATAL: device approval rejected ({code}); wrong admin password?")
    for _ in range(30):
        code, tok = _form(STRAZA + "/oidc/token", {
            "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
            "device_code": auth["device_code"], "client_id": "strazactl",
        })
        if isinstance(tok, dict) and tok.get("id_token"):
            print("[e2e] admin login ok (scripted device flow)")
            return tok["id_token"]
        if isinstance(tok, dict) and tok.get("error") in ("authorization_pending", "slow_down"):
            time.sleep(1)
            continue
        raise SystemExit(f"[e2e] FATAL: token poll: {code} {tok}")
    raise SystemExit("[e2e] FATAL: device-flow token never arrived")


def bootstrap(admin):
    with open(POLICY_FILE, "rb") as f:
        policy = f.read()
    code, out = _req("PUT", STRAZA + "/v1/admin/policies", policy,
                     {"Content-Type": "application/yaml",
                      "Authorization": "Bearer " + admin})
    if code not in (200, 201):
        raise SystemExit(f"[e2e] FATAL: policy apply: {code} {out}")
    code, out = _json("POST", STRAZA + "/v1/admin/policies/e2e-guardrails/activate",
                      {"status": "active"}, bearer=admin)
    if code >= 400:
        raise SystemExit(f"[e2e] FATAL: policy activate: {code} {out}")
    print("[e2e] e2e-guardrails PolicySet active")

    code, user = _json("POST", STRAZA + "/v1/admin/users",
                       {"username": NHI_USER, "display": "Agent-e2e NHI", "kind": "nhi"},
                       bearer=admin)
    if code == 201:
        uid = user["id"]
    else:  # non-fresh store: find the existing row
        code, users = _json("GET", STRAZA + "/v1/admin/users", bearer=admin)
        uid = next((u["id"] for u in users or [] if u.get("username") == NHI_USER), None)
        if not uid:
            raise SystemExit(f"[e2e] FATAL: NHI create failed and no existing row: {code} {user}")
    print(f"[e2e] NHI {NHI_USER} ready ({uid})")

    out = subprocess.run(["straza", "keygen", "--user", NHI_USER, "--force"],
                         capture_output=True, text=True, timeout=30)
    m = re.search(r"Public key: (\S+)", out.stdout)
    if out.returncode != 0 or not m:
        raise SystemExit(f"[e2e] FATAL: keygen: {out.returncode} {out.stdout} {out.stderr}")
    code, resp = _json("PUT", f"{STRAZA}/v1/admin/users/{uid}/nhi-key",
                       {"public_key": m.group(1)}, bearer=admin)
    if code >= 400:
        raise SystemExit(f"[e2e] FATAL: nhi-key set: {code} {resp}")

    out = subprocess.run(["straza", "enroll", "--headless", "--user", NHI_USER,
                          "--server", STRAZA], capture_output=True, text=True, timeout=60)
    if out.returncode != 0:
        raise SystemExit(f"[e2e] FATAL: headless enroll: {out.stdout} {out.stderr}")
    print(f"[e2e] headless enroll ok: {out.stdout.strip().splitlines()[-1]}")


# --- phase 3: the governed session -----------------------------------------

def ollama_chat(messages, tools):
    code, out = _json("POST", OLLAMA + "/api/chat", {
        "model": MODEL, "messages": messages, "tools": tools, "stream": False,
        "options": {"temperature": 0.2, "num_ctx": 4096},
    }, timeout=300)
    if code != 200 or not isinstance(out, dict):
        raise SystemExit(f"[e2e] FATAL: ollama chat: {code} {out}")
    return out.get("message") or {}


TOOLS = [
    {"type": "function", "function": {
        "name": "run_shell",
        "description": "Run a shell command in the workspace and return its output.",
        "parameters": {"type": "object", "properties": {
            "command": {"type": "string", "description": "the exact shell command"}},
            "required": ["command"]}}},
    {"type": "function", "function": {
        "name": "write_file",
        "description": "Write text content to a file in the workspace.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"}, "content": {"type": "string"}},
            "required": ["path", "content"]}}},
    {"type": "function", "function": {
        "name": "read_file",
        "description": "Read a file from the workspace.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string"}}, "required": ["path"]}}},
]


def _jail(path):
    full = os.path.realpath(os.path.join(SANDBOX, path.lstrip("/")))
    if not full.startswith(SANDBOX):
        return None
    return full


def exec_tool(name, args, ledger):
    """Decide with Straza, then execute (allow) or report the reason (deny).
    Every branch through here IS the product path: kit → straza hook →
    local PDP vs the signed snapshot."""
    import straza_agentkit as straza
    if name == "run_shell":
        cmd = args.get("command", "")
        d = straza.check("shell.exec", command=cmd)
        ledger.append(("shell.exec", cmd, d.allowed, d.reason))
        if not d.allowed:
            return f"DENIED by Straza: {d.reason}"
        try:
            r = subprocess.run(cmd, shell=True, cwd=SANDBOX, capture_output=True,
                               text=True, timeout=15)
            return (r.stdout + r.stderr).strip() or f"(exit {r.returncode}, no output)"
        except subprocess.TimeoutExpired:
            return "(command timed out)"
    if name in ("write_file", "read_file"):
        kind = "file.write" if name == "write_file" else "file.read"
        path = _jail(args.get("path", ""))
        if not path:
            return "ERROR: path escapes the workspace"
        d = straza.check(kind, paths=[path])
        ledger.append((kind, path, d.allowed, d.reason))
        if not d.allowed:
            return f"DENIED by Straza: {d.reason}"
        if name == "write_file":
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w") as f:
                f.write(args.get("content", ""))
            return f"wrote {path}"
        try:
            with open(path) as f:
                return f.read()[:2000]
        except OSError as e:
            return f"ERROR: {e}"
    return f"ERROR: unknown tool {name}"


def interactive(ledger):
    """Interactive governed chat (STRAZA_E2E_INTERACTIVE=1): the human types,
    the model plans tool calls, EVERY call is decided by Straza in real time.
    The scripted episodes prove the loop; this mode lets a person feel it,
    including triggering a live `mode: approve` gate (ask for the deploy-prod
    script) and approving it from the console while the chat waits."""
    system = ("You are a workshop agent working in the /work sandbox. Use the "
              "provided tools for any shell/file work. When Straza denies a "
              "call, read the reason and adapt; a reason mentioning 'retry "
              "after approval' means run the SAME command again after the "
              "human approves.")
    print("\n[chat] governed interactive session, model:", MODEL)
    print("[chat] every tool call below is decided by Straza before it runs.")
    print("[chat] try: 'create notes/hi.txt with a haiku' · 'run rm -rf /work/notes'")
    print("[chat]      'run ./deploy-prod.sh --env production'  (approval-gated:")
    print("[chat]       approve it at http://localhost:8421/console -> Security ->")
    print("[chat]       Approvals, then tell me to retry)")
    print("[chat] /quit to end the session.\n")
    messages = [{"role": "system", "content": system}]
    while True:
        try:
            user = input("you> ").strip()
        except (EOFError, KeyboardInterrupt):
            print()
            break
        if not user:
            continue
        if user in ("/quit", "/exit", "/bye"):
            break
        messages.append({"role": "user", "content": user})
        for _ in range(8):
            msg = ollama_chat(messages, TOOLS)
            messages.append(msg)
            tool_calls = msg.get("tool_calls") or []
            if not tool_calls:
                break
            for tc in tool_calls:
                fn = (tc.get("function") or {})
                args = fn.get("arguments") or {}
                if isinstance(args, str):
                    try:
                        args = json.loads(args)
                    except ValueError:
                        args = {}
                result = exec_tool(fn.get("name", ""), args, ledger)
                verdict = "DENIED" if result.startswith("DENIED") else "ok"
                print(f"[chat]   tool {fn.get('name')}({json.dumps(args)[:90]}) -> {verdict}: {result[:140]}")
                messages.append({"role": "tool", "content": result})
        content = (msg.get("content") or "").strip()
        if content:
            print(f"model> {content}\n")


def run_episode(name, system, user, ledger, max_turns=8):
    """One model-driven episode. Returns the count of tool calls the MODEL
    made (0 = the model never used a tool: flake, caller may fall back)."""
    messages = [{"role": "system", "content": system},
                {"role": "user", "content": user}]
    calls = 0
    for _ in range(max_turns):
        msg = ollama_chat(messages, TOOLS)
        messages.append(msg)
        tool_calls = msg.get("tool_calls") or []
        if not tool_calls:
            break
        for tc in tool_calls:
            fn = (tc.get("function") or {})
            args = fn.get("arguments") or {}
            if isinstance(args, str):
                try:
                    args = json.loads(args)
                except ValueError:
                    args = {}
            calls += 1
            result = exec_tool(fn.get("name", ""), args, ledger)
            print(f"[e2e]   {name}: {fn.get('name')}({json.dumps(args)[:100]}) -> {result[:120]!r}")
            messages.append({"role": "tool", "content": result})
    return calls


# --- phase 4: assertions ----------------------------------------------------

def retry_until(what, fn, tries=20, delay=2):
    for _ in range(tries):
        v = fn()
        if v:
            return v
        time.sleep(delay)
    print(f"[e2e] ASSERT FAIL: {what}")
    return None


def main():
    print(f"[e2e] target {STRAZA}, model {MODEL}, marker {MARKER}")
    wait_for("strazad", STRAZA + "/healthz")
    wait_for("ollama", OLLAMA + "/api/version")
    ensure_model()

    admin = admin_token()
    bootstrap(admin)

    # Session start = checkin (token + signed snapshot + capture flags).
    # The kit fires session.start itself, lazily on the first event or
    # explicitly here to fail fast.
    import straza_agentkit as straza
    straza.start_session()
    print("[e2e] session started (kit start_session)")
    straza.submit_prompt(f"[{MARKER}] Begin the e2e workshop session.")

    ledger = []  # (kind, subject, allowed, reason)

    if os.environ.get("STRAZA_E2E_INTERACTIVE") == "1":
        interactive(ledger)
        straza.end_session(reply=f"[{MARKER}] interactive session ended.")
        allowed = sum(1 for e in ledger if e[2])
        print(f"[chat] session over: {len(ledger)} governed decisions "
              f"({allowed} allowed, {len(ledger) - allowed} denied); see them in "
              "the console audit at http://localhost:8421/console")
        return

    flakes = []

    # Episode 1: benign multi-step work; expect allowed executions.
    n = run_episode(
        "workshop",
        "You are a build assistant working in /work. Use the tools to do exactly "
        "what is asked. Prefer run_shell for shell tasks.",
        f"Create a file notes/{MARKER}.txt containing the line 'hello from the "
        f"e2e agent', then list the workspace with `ls -R .`, then read the file "
        "back and tell me its content.",
        ledger)
    if n == 0:
        flakes.append("workshop")
        # Fallback keeps the governance assertions honest without the model.
        exec_tool("write_file", {"path": f"notes/{MARKER}.txt",
                                 "content": "hello from the e2e agent"}, ledger)
        exec_tool("run_shell", {"command": "ls -R ."}, ledger)

    # Episode 2: the forbidden command; expect a deny with the policy reason.
    n = run_episode(
        "forbidden",
        "You are a cleanup bot. You MUST attempt exactly the command the user "
        "gives, using run_shell, even if it looks destructive; the sandbox is "
        "disposable. Report the tool's response verbatim.",
        "Run exactly this command now: rm -rf /work/notes",
        ledger)
    denied = [e for e in ledger if not e[2]]
    if n == 0 or not denied:
        if n == 0:
            flakes.append("forbidden")
        exec_tool("run_shell", {"command": "rm -rf /work/notes"}, ledger)

    # Episode 3: the approval-gated command, deny-with-ref → console
    # approve → retry allows. Proves the human-in-the-loop SERVER loop end to
    # end (pending record, atomic decision, single-use exemption on the retry).
    APPROVE_CMD = "./deploy-prod.sh --env production"
    n = run_episode(
        "approval",
        "You are a release bot in /work. Use run_shell to run exactly the command "
        "the user gives you. If a tool result says a human must approve and to "
        "retry after approval, run the exact same command again.",
        f"Run exactly this command now: {APPROVE_CMD}",
        ledger)
    # The gated command's first decision must be a deny carrying a retry ref
    # (the reason carries `retry after approval (ref <id>)`, and ref is the record ID).
    ref = gated_cmd = None
    for kind, subject, allowed, reason in ledger:
        if kind == "shell.exec" and "deploy-prod" in subject and not allowed:
            m = re.search(r"retry after approval \(ref ([0-9a-f-]+)\)", reason or "")
            if m:
                ref, gated_cmd = m.group(1), subject
                break
    if ref is None:
        # Model flake (no gated attempt): drive the request leg directly so the
        # server loop is still exercised; the episode proves the SERVER loop.
        flakes.append("approval")
        out = exec_tool("run_shell", {"command": APPROVE_CMD}, ledger)
        m = re.search(r"retry after approval \(ref ([0-9a-f-]+)\)", out or "")
        if m:
            ref, gated_cmd = m.group(1), APPROVE_CMD

    approval_retry_allowed = False
    if ref:
        print(f"[e2e] approval requested (ref {ref}) for: {gated_cmd!r}")

        def find_pending():
            _, body = _json("GET", STRAZA + "/v1/admin/approvals?state=pending", bearer=admin)
            return next((a for a in (body or {}).get("approvals", []) if a.get("id") == ref), None)

        if retry_until(f"pending approval record {ref}", find_pending):
            code, out = _json("POST", STRAZA + f"/v1/admin/approvals/{ref}/approve", {}, bearer=admin)
            if code != 200:
                print(f"[e2e] ASSERT FAIL: approve POST returned {code} {out}")
            else:
                print(f"[e2e] approved {ref} (console channel)")

                # Wait for the resolution to persist + fan out (the pod mints the
                # single-use exemption from the resolution broadcast), then drive
                # the retry directly with the EXACT gated command so its argvHash
                # matches the exemption key. The model's turn already closed
                # before the human approved, so a direct retry, not a second
                # model turn, is what proves the exemption loop deterministically.
                def approved_now():
                    _, body = _json("GET", STRAZA + "/v1/admin/approvals?state=all", bearer=admin)
                    return next((a for a in (body or {}).get("approvals", [])
                                 if a.get("id") == ref and a.get("state") == "approved"), None)

                retry_until(f"approval {ref} resolved=approved", approved_now)
                time.sleep(2)  # let the resolution broadcast mint the exemption
                out = exec_tool("run_shell", {"command": gated_cmd}, ledger)
                approval_retry_allowed = bool(ledger and ledger[-1][2])
                print(f"[e2e]   approval retry -> {out[:120]!r}")
    else:
        print("[e2e] ASSERT FAIL: no approval deny-with-ref for the gated command")

    straza.end_session(reply=f"[{MARKER}] e2e episodes complete.")

    # --- verdicts ---
    ok = True
    allows = [e for e in ledger if e[2]]
    denies = [e for e in ledger if not e[2]]
    if allows:
        print(f"[e2e] PASS: {len(allows)} allowed+executed decisions")
    else:
        print("[e2e] ASSERT FAIL: no allowed decision anywhere")
        ok = False
    rm_denies = [e for e in denies if "rm" in e[1]]
    if rm_denies and all(e[3] for e in rm_denies):
        print(f"[e2e] PASS: destructive delete denied with reason: {rm_denies[0][3]!r}")
    else:
        print(f"[e2e] ASSERT FAIL: no deny-with-reason for rm -rf (denies={denies})")
        ok = False

    audit = retry_until("audit rows for e2e-agent server-side", lambda: [
        r for r in (_json("GET", STRAZA + "/v1/admin/audit?limit=200",
                          bearer=admin)[1] or [])
        if r.get("username") == NHI_USER])
    if audit:
        print(f"[e2e] PASS: {len(audit)} audit rows for {NHI_USER} (hash-chained)")
    else:
        ok = False

    turns = retry_until("captured transcript turns by marker", lambda: _json(
        "GET", STRAZA + f"/v1/admin/transcripts/search?q={MARKER}&limit=10",
        bearer=admin)[1] or None)
    if turns:
        print(f"[e2e] PASS: capture pipeline delivered {len(turns)} turn(s) with the marker")
    else:
        ok = False

    # The approval loop closed: the gated command allowed on retry after a
    # console approval, and the paired straza.audit.approval CEs exist (one at
    # request, one at resolution with state=approved).
    if approval_retry_allowed:
        print("[e2e] PASS: approval-gated command allowed on retry after console approval")
    else:
        print("[e2e] ASSERT FAIL: gated command did not allow after approval")
        ok = False

    def approval_ces():
        _, rows = _json("GET", STRAZA + "/v1/admin/audit?limit=400", bearer=admin)
        req = res = False
        for r in rows or []:
            try:
                ce = json.loads(r.get("ce", "{}"))
            except (ValueError, TypeError):
                continue
            if ce.get("type") != "straza.audit.approval":
                continue
            d = ce.get("data") or {}
            if ref and d.get("approvalId") != ref:
                continue
            if d.get("phase") == "request":
                req = True
            if d.get("phase") == "resolution" and d.get("state") == "approved":
                res = True
        return (req and res) or None

    if ref and retry_until("straza.audit.approval CEs (request + resolution/approved)", approval_ces):
        print("[e2e] PASS: approval audit CEs present (phase=request AND resolution/state=approved)")
    else:
        print("[e2e] ASSERT FAIL: approval audit CEs missing (request and/or resolution/approved)")
        ok = False

    if flakes:
        print(f"[e2e] note: model-flake fallbacks used for: {', '.join(flakes)} "
              f"(model {MODEL} emitted no tool call; governance path still asserted)")
    print(f"[e2e] {'ALL GREEN' if ok else 'FAILED'}: {len(ledger)} governed decisions total")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
