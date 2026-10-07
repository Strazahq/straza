"""Guard: a skipped integration suite must not read as a passing one.

Every integration test in this package guards its import with a skip, so on a
machine without the agent frameworks the suite reports OK while testing none
of the delegation-governance surface, and gaps there accumulate unseen.

The fix is not to forbid skipping (developers must still be able to run the
core suite bare) but to make the *intent* explicit: when
``STRAZA_KIT_REQUIRE_EXTRAS=1`` is set, the frameworks are supposed to be
installed, and a missing one is a FAILURE rather than a silent skip. CI sets
it after installing the extras.
"""

import importlib
import os
import unittest

# module to install: the extra that provides it
REQUIRED = {
    "langchain": "langchain",
    "agents": "openai-agents",
    "claude_agent_sdk": "claude-agent-sdk",
    "deepagents": "test",
}

REQUIRE = os.environ.get("STRAZA_KIT_REQUIRE_EXTRAS") == "1"


@unittest.skipUnless(REQUIRE, "STRAZA_KIT_REQUIRE_EXTRAS is not set (bare run)")
class ExtrasPresentTest(unittest.TestCase):
    def test_every_integration_framework_is_importable(self):
        missing = []
        for module, extra in sorted(REQUIRED.items()):
            try:
                importlib.import_module(module)
            except ImportError:
                missing.append(f"{module} (pip install -e '.[{extra}]')")
        self.assertFalse(
            missing,
            "STRAZA_KIT_REQUIRE_EXTRAS=1 promises the agent frameworks are installed, "
            "but these are missing so their suites just SKIPPED, which is how this "
            "job stayed green while never executing the integrations:\n  "
            + "\n  ".join(missing),
        )
