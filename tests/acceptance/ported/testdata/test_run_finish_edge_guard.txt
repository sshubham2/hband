"""run_finish.py must never run a finish body that would start Edge.

test_e004_2's body opens the first browser of its own list that exists: its own
Chrome path, then msedge. Whether CHROME_BIN names a Chrome somewhere else says
nothing about which of the two the body will take, so a body that names msedge
must not be run on the strength of CHROME_BIN alone.
"""
import os
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import run_finish  # noqa: E402

BODY = 'exe = [p for p in (OWN_CHROME, EDGE) if os.path.isfile(p)]  # falls back to msedge\n'


class EdgeGuard(unittest.TestCase):
    def test_body_that_would_fall_back_to_edge_is_not_run(self):
        scratch = tempfile.mkdtemp(prefix="edge-guard-")
        elsewhere = os.path.join(scratch, "portable-chrome.exe")
        with open(elsewhere, "w") as f:
            f.write("")
        real_isfile = os.path.isfile

        def only_elsewhere(p):
            # The one browser on this pretend machine is the one CHROME_BIN names; the
            # Chrome the body looks for is not there, so the body would start msedge.
            return os.path.normcase(os.path.abspath(p)) == os.path.normcase(elsewhere) and real_isfile(p)

        line = {"path": "tests/acceptance/x.py", "body": BODY, "command": "python tests/acceptance/x.py"}
        with mock.patch.dict(os.environ, {"CHROME_BIN": elsewhere}), \
                mock.patch("os.path.isfile", only_elsewhere), \
                mock.patch("subprocess.run") as run:
            code, _, _ = run_finish.run_body(os.path.join(scratch, "tree"), os.path.join(scratch, "home"), "E-004", line)
        self.assertFalse(run.called, "a body that would fall back to msedge was run")
        self.assertIsNone(code)


if __name__ == "__main__":
    unittest.main()
