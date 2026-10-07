"""straza-agentkit core tests (stdlib unittest, no deps).

straza is stubbed with a small Python script driven by env vars: the
tests assert the payload SHAPE the kit emits (the python-sdk dialect
contract), the exit-code decision mapping, fail-closed behavior when the
binary is missing, and that capture calls never raise. The real-binary
integration lane is the Go side's normalize tests + the conformance runner.
"""

import json
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
import straza_agentkit as straza  # noqa: E402

STUB = r"""
import json, os, sys
payload = json.load(sys.stdin)
with open(os.environ["STUB_CAPTURE"], "a", encoding="utf-8") as f:
    f.write(json.dumps({"payload": payload, "harness": os.environ.get("STRAZA_HARNESS", "")}) + "\n")
mode = os.environ.get("STUB_MODE", "allow")
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


class KitTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.stub = os.path.join(self.dir, "stub.py")
        self.capture = os.path.join(self.dir, "capture.jsonl")
        with open(self.stub, "w", encoding="utf-8") as f:
            f.write(STUB)
        os.environ["STRAZA_BIN"] = f'"{sys.executable}" "{self.stub}"'
        os.environ["STUB_CAPTURE"] = self.capture
        os.environ["STUB_MODE"] = "allow"
        straza._session_started = False  # each test observes its own auto-start

    def tearDown(self):
        for k in ("STRAZA_BIN", "STUB_CAPTURE", "STUB_MODE"):
            os.environ.pop(k, None)

    def invocations(self, events=True):
        """Recorded stub invocations; events=False drops the auto-fired
        SessionStart so decision tests read like before the T15 fix."""
        if not os.path.exists(self.capture):
            return []
        with open(self.capture, encoding="utf-8") as f:
            out = [json.loads(line) for line in f if line.strip()]
        if not events:
            out = [i for i in out if i["payload"]["hook_event_name"] != "SessionStart"]
        return out

    def test_first_event_auto_starts_session(self):
        # T15: SDK agents have no harness to fire session.start, so the kit
        # does, exactly once, before its first event of any kind.
        straza.check("shell.exec", command="git status")
        straza.check("shell.exec", command="git diff")
        events = [i["payload"]["hook_event_name"] for i in self.invocations()]
        self.assertEqual(events, ["SessionStart", "PreToolUse", "PreToolUse"])
        self.assertEqual(self.invocations()[0]["harness"], "python-sdk")

    def test_start_session_explicit_and_idempotent(self):
        straza.start_session()
        straza.start_session()
        straza.submit_prompt("hi")
        events = [i["payload"]["hook_event_name"] for i in self.invocations()]
        self.assertEqual(events, ["SessionStart", "UserPromptSubmit"])

    def test_check_allow_payload_shape(self):
        d = straza.check("shell.exec", command="git status")
        self.assertTrue(d.allowed)
        (inv,) = self.invocations(events=False)
        self.assertEqual(inv["harness"], "python-sdk")
        p = inv["payload"]
        self.assertEqual(p["hook_event_name"], "PreToolUse")
        self.assertEqual(p["tool_name"], "shell.exec")
        self.assertEqual(p["tool_input"], {"command": "git status"})
        self.assertTrue(p["session_id"].startswith("py-"))
        self.assertIn("cwd", p)

    def test_check_deny_reason(self):
        os.environ["STUB_MODE"] = "deny"
        d = straza.check("shell.exec", command="rm -rf /")
        self.assertFalse(d.allowed)
        self.assertIn("destructive delete blocked", d.reason)

    def test_guard_raises_on_deny(self):
        os.environ["STUB_MODE"] = "deny"

        @straza.guard("shell.exec", command_from=lambda cmd: cmd)
        def run(cmd):
            return "ran"

        with self.assertRaises(straza.StrazaDenied) as ctx:
            run("rm -rf /")
        self.assertIn("destructive delete blocked", str(ctx.exception))

    def test_guard_runs_on_allow(self):
        @straza.guard("file.write", paths_from=lambda path, data: [path])
        def write(path, data):
            return len(data)

        self.assertEqual(write("/tmp/x", "abc"), 3)
        (inv,) = self.invocations(events=False)
        self.assertEqual(inv["payload"]["tool_input"], {"paths": ["/tmp/x"]})

    def test_mcp_form(self):
        straza.check("other", app="midpoint", tool_name="search_users")
        (inv,) = self.invocations(events=False)
        self.assertEqual(inv["payload"]["tool_name"], "mcp__midpoint__search_users")

    def test_missing_binary_fails_closed(self):
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "no-such-binary")
        d = straza.check("shell.exec", command="ls")
        self.assertFalse(d.allowed)
        self.assertIn("failing closed", d.reason)

    def test_capture_calls_never_raise(self):
        straza.submit_prompt("what users exist?")
        straza.end_session(reply="kim and bob")
        events = [i["payload"]["hook_event_name"] for i in self.invocations()]
        self.assertEqual(events, ["SessionStart", "UserPromptSubmit", "SessionEnd"])
        self.assertEqual(self.invocations()[2]["payload"]["prompt_response"], "kim and bob")
        # And with no binary at all: still silent.
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        straza.submit_prompt("still fine")
        straza.end_session()


class TurnParityTest(KitTest):
    """Turn parity with the harness path.

    A harness fires the canonical session.end PER TURN (claude-code's Stop),
    so end_turn does the same, and end_session stays an alias of it.
    """

    def payloads(self, event):
        return [i["payload"] for i in self.invocations() if i["payload"]["hook_event_name"] == event]

    def test_end_turn_carries_the_reply(self):
        straza.end_turn(reply="the answer")
        sent = self.payloads("SessionEnd")
        self.assertEqual(len(sent), 1)
        self.assertEqual(sent[0]["prompt_response"], "the answer")

    def test_every_turn_is_captured_not_just_the_last(self):
        """The regression that matters: a reply captured only at process exit
        is a reply lost whenever the process is killed."""
        for turn in ("first", "second", "third"):
            straza.submit_prompt(f"ask {turn}")
            straza.end_turn(reply=f"reply {turn}")
        self.assertEqual(
            [p["prompt_response"] for p in self.payloads("SessionEnd")],
            ["reply first", "reply second", "reply third"],
        )

    def test_end_session_is_end_turn_under_its_original_name(self):
        straza.end_turn(reply="x")
        straza.end_session(reply="x")
        a, b = self.payloads("SessionEnd")
        self.assertEqual(a, b)

    def test_end_turn_without_a_reply_still_drains(self):
        """No reply is a legitimate turn end, and it must still emit, because
        the same event drains the audit spool."""
        straza.end_turn()
        sent = self.payloads("SessionEnd")
        self.assertEqual(len(sent), 1)
        self.assertNotIn("prompt_response", sent[0])

    def test_tool_result_emits_post_tool_use(self):
        """tool.post closes the pair in the audit trail the way a harness's
        PostToolUse does. It was declared in adapters/python-sdk.yaml and the
        published mapping, but the kit never emitted it."""
        straza.tool_result("shell.exec", command="ls -la")
        sent = self.payloads("PostToolUse")
        self.assertEqual(len(sent), 1)
        self.assertEqual(sent[0]["tool_name"], "shell.exec")
        self.assertEqual(sent[0]["tool_input"], {"command": "ls -la"})

    def test_tool_result_uses_the_mcp_name_form(self):
        straza.tool_result("mcp.call", app="github", tool_name="create_issue")
        self.assertEqual(self.payloads("PostToolUse")[0]["tool_name"], "mcp__github__create_issue")

    def test_tool_result_is_observational(self):
        """Never enforced: the decision was already made at check(). A deny
        exit must not raise, and must not be mistaken for a verdict."""
        os.environ["STUB_MODE"] = "deny"
        straza.tool_result("shell.exec", command="rm -rf /")  # must not raise

    def test_parity_calls_are_silent_without_a_binary(self):
        os.environ["STRAZA_BIN"] = os.path.join(self.dir, "gone")
        straza.end_turn(reply="x")
        straza.tool_result("file.read", paths=["/etc/passwd"])


if __name__ == "__main__":
    unittest.main()
