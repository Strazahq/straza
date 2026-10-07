#!/usr/local/bin/python3
"""Straza Tier-3 sandbox profile entrypoint.

Runs as the agent user (uid 65532) before the agent command starts:

1. Verifies the enrollment material is mounted (read-only volume at
   $STRAZA_ENROLL_DIR, default /straza/enroll) and fails LOUDLY with the
   remedy if it is absent: an unenrolled shim can only fail closed, so
   surface the misconfiguration at start, not on the first command.
2. Copies config.yaml + state/identity.json into the writable straza
   home ($STRAZA_HOME, default /run/straza, a tmpfs). The
   mount itself stays read-only (tamper-proof from inside the container);
   the copy exists because the shim writes session/snapshot/audit-spool
   state next to its config.
3. exec()s the agent command with PATH already pinned to /straza/bin.

Why Python and not shell: this image de-executes coreutils (mkdir/cp/chmod
are root-only after the hardening sweep), and the agent's interpreter is the
one binary guaranteed executable. A Python entrypoint therefore needs ZERO
extra entries on the executable allowlist. Deriving this profile for a
non-Python base means porting this file to that runtime.
"""

import os
import shutil
import sys

EX_USAGE, EX_CANTCREAT, EX_CONFIG = 64, 73, 78

ENROLL_DIR = os.environ.get("STRAZA_ENROLL_DIR", "/straza/enroll")
AG_HOME = os.environ.get("STRAZA_HOME", "/run/straza")


def die(code: int, *lines: str) -> None:
    print("\n".join("Straza sandbox: " + l for l in lines), file=sys.stderr)
    sys.exit(code)


def enrolled_server_url(config_path: str) -> str:
    """Naive read of serverUrl from config.yaml (no YAML lib in the image;
    the file is written by `straza enroll`, a flat two-key mapping)."""
    with open(config_path, encoding="utf-8") as f:
        for line in f:
            if line.startswith("serverUrl:"):
                return line.split(":", 1)[1].strip().strip("'\"")
    return ""


def main() -> None:
    config_src = os.path.join(ENROLL_DIR, "config.yaml")
    identity_src = os.path.join(ENROLL_DIR, "state", "identity.json")

    missing = [p for p in (config_src, identity_src) if not os.path.isfile(p)]
    if missing:
        die(
            EX_CONFIG,
            "enrollment material is missing: " + ", ".join(missing),
            "",
            "The shim fails closed without it: nothing will be allowed to run.",
            "Remedy (see https://docs.straza.ai/guides/govern-an-agent/hookless-processes/):",
            "  1. on an operator workstation: straza enroll --server <strazad-url>",
            "     (headless NHI, no browser: straza keygen + strazactl users nhi-key set",
            "      + straza enroll --headless --user <nhi>)",
            "  2. copy ~/.straza/config.yaml and ~/.straza/state/identity.json",
            "     (+ state/nhi-key.json for headless) into an enroll dir (keep the state/",
            "     subdirectory), readable by uid 65532",
            "  3. mount it read-only at " + ENROLL_DIR + " (compose: STRAZA_ENROLL_DIR)",
        )

    # The enrolled serverUrl is the trust anchor (snapshot keys are pinned to
    # it at enroll time) and is NEVER rewritten here. STRAZA_SERVER_URL is a
    # cross-check only: a mismatch means the operator pointed the container at
    # one strazad and mounted enrollment for another; ambiguous trust anchors
    # stop the boat rather than silently preferring one.
    enrolled = enrolled_server_url(config_src)
    declared = os.environ.get("STRAZA_SERVER_URL", "")
    if declared and enrolled and declared.rstrip("/") != enrolled.rstrip("/"):
        die(
            EX_CONFIG,
            "STRAZA_SERVER_URL (%s) != enrolled serverUrl (%s)" % (declared, enrolled),
            "the enrolled config is the trust anchor and is never rewritten;",
            "re-enroll against the server you mean, or fix/unset STRAZA_SERVER_URL.",
        )

    try:
        os.makedirs(os.path.join(AG_HOME, "state"), mode=0o700, exist_ok=True)
    except OSError as err:
        die(
            EX_CANTCREAT,
            "cannot create straza home %s: %s" % (AG_HOME, err),
            "mount a tmpfs there owned by uid 65532, e.g. compose:",
            "  tmpfs: [\"/run/straza:mode=0700,uid=65532,gid=65532,noexec,nosuid,nodev\"]",
        )
    # nhi-key.json is the headless credential, optional: present only
    # for NHI enrollments, required there because every session start signs a
    # fresh assertion with it.
    nhi_key_src = os.path.join(ENROLL_DIR, "state", "nhi-key.json")
    stage = [
        (config_src, os.path.join(AG_HOME, "config.yaml")),
        (identity_src, os.path.join(AG_HOME, "state", "identity.json")),
    ]
    if os.path.isfile(nhi_key_src):
        stage.append((nhi_key_src, os.path.join(AG_HOME, "state", "nhi-key.json")))
    for src, dst in stage:
        try:
            shutil.copyfile(src, dst)
            os.chmod(dst, 0o600)
        except OSError as err:
            die(EX_CONFIG, "cannot stage %s -> %s: %s" % (src, dst, err),
                "the enroll mount must be readable by uid 65532 (chown/chmod on the host)")

    if len(sys.argv) < 2:
        die(EX_USAGE, "no agent command given",
            "usage: <image> <agent-command> [args...]  (e.g. python3 /app/agent.py)")

    print("Straza sandbox: governed profile ready (server %s, home %s)" % (enrolled, AG_HOME),
          file=sys.stderr)
    try:
        os.execvp(sys.argv[1], sys.argv[1:])  # resolves via PATH=/straza/bin only
    except OSError as err:
        die(127, "cannot exec %r: %s" % (sys.argv[1], err),
            "only /straza/bin is on PATH; run external commands via `wexec`/`sh -c`")


if __name__ == "__main__":
    main()
