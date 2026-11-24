#!/usr/local/bin/python3
"""Straza Tier-3 sandbox profile entrypoint.

Runs as the agent user (uid 65532) before the agent command starts:

1. Asks the Straza background service, over its socket in the volume the
   compose file shares at /run/straza-service, whether this account is
   enrolled, and fails LOUDLY with the remedy if the service does not answer
   or holds no enrollment for it: an unenrolled shim can only fail closed, so
   surface the misconfiguration at start, not on the first command. The
   service runs in its own container under its own account and holds the
   key, the enrollment and every session, so nothing here stages a
   credential. With STRAZA_SANDBOX_WAIT_FOR_ENROLLMENT=1 it waits for the
   enrollment instead of failing, for a Kubernetes pod, whose container must
   run before anyone can enroll it. The agent command has not started while
   it waits, and a service that stops answering still ends the container.
2. exec()s the agent command with PATH already pinned to /straza/bin.

Why Python and not shell: this image de-executes coreutils (mkdir/cp/chmod
are root-only after the hardening sweep), and the agent's interpreter is the
one binary guaranteed executable. A Python entrypoint therefore needs ZERO
extra entries on the executable allowlist. Deriving this profile for a
non-Python base means porting this file to that runtime.
"""

import http.client
import json
import os
import signal
import socket
import sys
import time

EX_USAGE, EX_UNAVAILABLE, EX_CONFIG = 64, 69, 78

SOCKET = "/run/straza-service/service.sock"
STRAZA = "/opt/straza/bin/straza"
WAIT_ENV = "STRAZA_SANDBOX_WAIT_FOR_ENROLLMENT"
POLL_SECONDS = 2


def die(code: int, *lines: str) -> None:
    print("\n".join("Straza sandbox: " + l for l in lines), file=sys.stderr)
    sys.exit(code)


class ServiceConnection(http.client.HTTPConnection):
    """HTTP over the service's unix socket."""

    def __init__(self) -> None:
        super().__init__("straza-service", timeout=5)

    def connect(self) -> None:
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(SOCKET)


def service_status() -> dict:
    """The caller's status from the service's local API. The service names
    the caller by the uid the kernel reports, so it answers for this account
    only. Raises OSError when the service does not answer or refuses."""
    conn = ServiceConnection()
    try:
        conn.request("GET", "/local/v1/status")
        resp = conn.getresponse()
        body = resp.read()
    finally:
        conn.close()
    if resp.status >= 400:
        try:
            sentence = json.loads(body).get("error", "")
        except ValueError:
            sentence = ""
        raise OSError(sentence or "the service answered HTTP %d" % resp.status)
    return json.loads(body)


def service_enrollment() -> dict:
    """The caller's enrollment as the service reports it, empty when the
    account is not enrolled. Exits 69 when the service does not answer."""
    try:
        status = service_status()
    except (OSError, ValueError) as err:
        die(
            EX_UNAVAILABLE,
            "the Straza background service did not answer at %s: %s" % (SOCKET, err),
            "",
            "The shim fails closed without it: nothing will be allowed to run.",
            "Start the profile with its service, which mounts the shared socket volume:",
            "  docker compose -f deploy/sandbox/compose.yaml up",
            "and read why the service stopped with:",
            "  docker compose -f deploy/sandbox/compose.yaml logs straza-service",
        )
    return status.get("enrollment") or {}


def wait_for_enrollment() -> dict:
    """Polls the service until this account is enrolled and returns the
    enrollment. The container is process 1, which the kernel shields from a
    signal it does not handle, so SIGTERM gets a handler here and a pod that
    is deleted while it waits ends at once."""
    print("\n".join("Straza sandbox: " + l for l in (
        "this container's account (uid %d) is not enrolled with the Straza background service." % os.getuid(),
        "%s=1 is set, so the container waits, and the agent's command has not started." % WAIT_ENV,
        "Enroll the account through the service, with commands run in this container:",
        "  %s keygen --user <nhi>" % STRAZA,
        "  (an admin registers the printed public key: strazactl users nhi-key set <nhi> <publicKey>)",
        "  %s enroll --headless --user <nhi>" % STRAZA,
        "Add --server <strazad-url> to enroll when the service has no managed configuration.",
        "The agent's command starts when the enrollment is there.",
    )), file=sys.stderr, flush=True)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(128 + signal.SIGTERM))
    while True:
        time.sleep(POLL_SECONDS)
        enrollment = service_enrollment()
        if enrollment.get("server_url"):
            signal.signal(signal.SIGTERM, signal.SIG_DFL)
            return enrollment


def main() -> None:
    enrollment = service_enrollment()
    if not enrollment.get("server_url") and os.environ.get(WAIT_ENV) == "1":
        enrollment = wait_for_enrollment()
    enrolled = enrollment.get("server_url", "")
    if not enrolled:
        die(
            EX_CONFIG,
            "this container's account (uid %d) is not enrolled with the Straza background service." % os.getuid(),
            "",
            "The shim fails closed without an enrollment: nothing will be allowed to run.",
            "Enroll it once through the service (see https://docs.straza.ai/guides/govern-an-agent/hookless-processes/).",
            "For an AI agent with its own key, from deploy/sandbox:",
            "  docker compose run --rm --entrypoint %s agent keygen --user <nhi>" % STRAZA,
            "  (an admin registers the printed public key: strazactl users nhi-key set <nhi> <publicKey>)",
            "  docker compose run --rm --entrypoint %s agent enroll --headless --user <nhi> --server <strazad-url>" % STRAZA,
            "For a person, by a sign-in code, which an enterprise server accepts only when governance.minSignIn is code:",
            "  docker compose run --rm --entrypoint %s agent enroll --use-device-code --server <strazad-url>" % STRAZA,
            "The enrollment stays in the service's state volume across restarts.",
            "In a Kubernetes pod, whose container must run before anyone can enroll it,",
            "set %s=1 and the entrypoint waits for the enrollment instead." % WAIT_ENV,
        )

    # The enrolled server URL is the trust anchor (snapshot keys are pinned to
    # it at enroll time) and is NEVER rewritten here. STRAZA_SERVER_URL is a
    # cross-check only: a mismatch means the operator pointed the container at
    # one strazad and enrolled the service with another; ambiguous trust
    # anchors stop the boat rather than silently preferring one.
    declared = os.environ.get("STRAZA_SERVER_URL", "")
    if declared and declared.rstrip("/") != enrolled.rstrip("/"):
        die(
            EX_CONFIG,
            "STRAZA_SERVER_URL (%s) != enrolled serverUrl (%s)" % (declared, enrolled),
            "the enrollment is the trust anchor and is never rewritten;",
            "enroll again against the server you mean, or fix/unset STRAZA_SERVER_URL.",
        )

    if len(sys.argv) < 2:
        die(EX_USAGE, "no agent command given",
            "usage: <image> <agent-command> [args...]  (e.g. python3 /app/agent.py)")

    print("Straza sandbox: governed profile ready (server %s, enrolled as %s)"
          % (enrolled, enrollment.get("username", "?")), file=sys.stderr)
    try:
        os.execvp(sys.argv[1], sys.argv[1:])  # resolves via PATH=/straza/bin only
    except OSError as err:
        die(127, "cannot exec %r: %s" % (sys.argv[1], err),
            "only /straza/bin is on PATH; run external commands via `wexec`/`sh -c`")


if __name__ == "__main__":
    main()
