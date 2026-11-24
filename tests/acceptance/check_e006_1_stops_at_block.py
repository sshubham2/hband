"""SL-010 case C-013: E-006 finish line 1 against a tree that has removal (SC-020) and
not yet blocking (SC-021).

Line 1 (tests/acceptance/test_e006_1_removal_and_placeholders.py, the finish line of
epic 44, kept in the engine's ledger and planted in the tree by the engine) posts
comments, removes three of them through POST /admin/comments/<n>/remove and, after
each removal, reads /reports/<slug> and / as three viewers and checks the visible
comment count, the 'comment removed' placeholders, their nesting and the vanished
text. Only then does it press POST /admin/users/<id>/block, which SC-021 builds.

So on this tree the planted body must fail, and fail exactly there: exit 1, one test
run, one failure, and that failure is the press helper's status assertion on the
block POST answering 404. Any other stop (a failed removal press, a wrong count, a
placeholder drawn wrong) is a different message and fails this check. A body that
passes outright (SC-021 built) also satisfies it, so the check does not have to be
deleted when SC-021 lands.

If the planted body is not in the tree, it is read from the ledger the way the design
names: one read-only connection, SELECT only, closed at once; the tree is copied to a
scratch folder, the body is written into it and run from there. Exit 0: the property
holds. Exit 1: it does not. Exit 2: there is no body to run.
"""
import json
import os
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
NAME = "test_e006_1_removal_and_placeholders.py"
STOP = re.compile(r"^AssertionError: 404 not less than 400 : the owner's POST /admin/users/\d+/block answered 404$", re.M)


def ledger_body():
    path = os.environ.get("TAAD3_LEDGER") or os.path.join(os.environ.get("APPDATA", ""), "taad3", "engine.db")
    if not os.path.isfile(path):
        return None
    con = sqlite3.connect("file:" + path.replace("\\", "/") + "?mode=ro", uri=True)
    try:
        row = con.execute("SELECT finish FROM epics WHERE finish LIKE '%/admin/comments/%' AND finish LIKE '%" + NAME + "%'").fetchone()
    finally:
        con.close()
    if not row:
        return None
    for line in json.loads(row[0]):
        if line.get("path", "").endswith(NAME):
            return line.get("body")
    return None


def main():
    scratch = tempfile.mkdtemp(prefix="hband-line1-")
    try:
        planted = os.path.join(ROOT, "tests", "acceptance", NAME)
        if os.path.isfile(planted):
            where, script = ROOT, planted
        else:
            body = ledger_body()
            if not body:
                print("no planted %s in the tree and none in the ledger" % NAME)
                return 2
            where = os.path.join(scratch, "tree")
            shutil.copytree(ROOT, where, ignore=shutil.ignore_patterns(".git", ".taad", "tests"))
            script = os.path.join(where, "tests", "acceptance", NAME)
            os.makedirs(os.path.dirname(script), exist_ok=True)
            with open(script, "w", encoding="utf-8", newline="\n") as f:
                f.write(body)
        env = dict(os.environ, TAAD3_HOME=os.path.join(scratch, "home"))
        os.makedirs(env["TAAD3_HOME"], exist_ok=True)
        run = subprocess.run([sys.executable, "-I", script], cwd=where, env=env, capture_output=True, text=True,
                             errors="replace", timeout=900)
        out = run.stdout + run.stderr
        if run.returncode == 0:
            print("finish line 1 passes outright")
            return 0
        stops = STOP.findall(out)
        if (run.returncode == 1 and len(stops) == 1 and re.search(r"^Ran 1 test ", out, re.M)
                and "FAILED (failures=1)" in out):
            print("finish line 1 stops at the block press, after posting and the three removals passed: " + stops[0])
            return 0
        print("finish line 1 failed somewhere other than the block press (exit %d):\n%s" % (run.returncode, out[-3000:]))
        return 1
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
