"""SL-013: the finish lines, run against a build of this tree.

The finish-line bodies are kept in the engine's ledger and are not in the tree.
This script reads the ones it is asked for, with one read-only connection (SELECT
only, closed at once), copies the tree into a scratch folder, writes each body to
the path its row names and runs the row's own command from there. Each body builds
`go build ./cmd/hband` from the copy and starts it on a free port with a fresh data
folder, so the product it tests is this tree's, not a stand-in.

    python -I tests/acceptance/run_finish.py --e002
        Epic E-002 (show-all, row numbers, opens counted once, proxy visitors
        hashed). Exit 0 only if every one of its bodies exits 0.

    python -I tests/acceptance/run_finish.py --others
        Epics E-001 and E-003..E-006, which read no show-all row but now meet a
        /reports page and an "All reports" link. Exit 0 only if every body in
        BASELINE (the bodies that exit 0 on the tree before SL-013, measured at
        1251104 with --rev) still exits 0. A body outside BASELINE is reported and
        does not fail the run: it did not pass before this slice either.

    --rev REV     run against `git archive REV` instead of the working tree (read-only;
                  how BASELINE was measured: --rev 1251104 --others --report-only).
    --only TEXT   only the bodies whose path contains TEXT.
    --report-only print the results and exit 0.
    --ledger PATH the ledger, default %APPDATA%\\taad3\\engine.db. TAAD3_HOME is never
                  consulted: a run of project code is made with it pointing at an empty
                  scratch folder, and the ledger is not there.

Exit 0: the property holds. Exit 1: a body that must pass did not. Exit 2: there is
nothing to run (no ledger, no such epic, no git for --rev).
"""
import argparse
import io
import json
import os
import re
import shlex
import shutil
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import time

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

EPIC_KEYS = {"e002": ["E-002"], "others": ["E-001", "E-003", "E-004", "E-005", "E-006"]}

# The bodies that exit 0 on the tree at 1251104, before SL-013 (measured once with
# --rev 1251104 --others --report-only). A body that is not here did not pass then.
BASELINE = {
    "tests/acceptance/e001_1_owner_only_admin/e001_1_owner_only_admin_test.go",
    "tests/acceptance/e001_2_publish_and_read/e001_2_publish_and_read_test.go",
    "tests/acceptance/e001_3_hidden_and_deleted/e001_3_hidden_and_deleted_test.go",
    "tests/acceptance/e001_4_replace_keeps_date/e001_4_replace_keeps_date_test.go",
    "tests/acceptance/e001_5_upload_refusals/e001_5_upload_refusals_test.go",
    "tests/acceptance/e001_6_report_isolation/e001_6_report_isolation_test.go",
    "tests/acceptance/test_e003_1_sign_in_and_vote.py",
    "tests/acceptance/test_e004_1_comments_and_replies.py",
    "tests/acceptance/test_e004_2_deep_threads_and_toggle.py",
    "tests/acceptance/test_e005_1_private_feedback.py",
    "tests/acceptance/test_e006_1_removal_and_placeholders.py",
    "tests/acceptance/test_e006_2_blocking_and_unblocking.py",
}

TIMEOUT = 14 * 60

# A browser executable a body names as a literal path, in the order the body names it.
BROWSER_PATH = re.compile(r"(?i)[A-Za-z]:[\\/](?:[^\"'`\r\n]*[\\/])?(?:chrome|chromium|msedge)\.exe")


def ledger_path(arg):
    return arg or os.path.join(os.environ.get("APPDATA", ""), "taad3", "engine.db")


def read_bodies(path, keys):
    """The finish lines of the hband epics with those keys, from one read-only connection."""
    con = sqlite3.connect("file:" + path.replace("\\", "/") + "?mode=ro", uri=True)
    try:
        lines = []
        for key in keys:
            rows = con.execute("SELECT id, finish FROM epics WHERE key = ? AND finish LIKE '%cmd/hband%'", (key,)).fetchall()
            if len(rows) != 1:
                raise SystemExit("epic %s: %d rows in the ledger that build ./cmd/hband, want exactly 1" % (key, len(rows)))
            for line in json.loads(rows[0][1]):
                lines.append((key, line))
        return lines
    finally:
        con.close()


def copy_tree(rev, dest):
    if rev:
        git = subprocess.run(["git", "archive", "--format=tar", rev], cwd=ROOT, capture_output=True)
        if git.returncode != 0:
            raise SystemExit("git archive %s failed: %s" % (rev, git.stderr.decode(errors="replace")))
        os.makedirs(dest)
        with tarfile.open(fileobj=io.BytesIO(git.stdout)) as tar:
            tar.extractall(dest, filter="data")
    else:
        shutil.copytree(ROOT, dest, ignore=shutil.ignore_patterns(".git", ".taad", "data", "__pycache__"))


def edge_risk(body):
    """Why a body must not be run because it could start Edge, or "" if it cannot.

    A body that names msedge opens the first browser of ITS OWN list that exists, and
    it ignores CHROME_BIN, so CHROME_BIN says nothing about which browser it takes.
    The body is run only when the first browser path it names that exists on this
    machine is not Edge. A body whose list cannot be read, or whose first existing
    path is Edge, is skipped. No browser path is written in this script."""
    if "msedge" not in body.lower():
        return ""
    for m in BROWSER_PATH.finditer(body):
        found = re.sub(r"\\{2,}", r"\\", m.group(0))
        if os.path.isfile(found):
            if "msedge" in os.path.basename(found.replace("\\", "/")).lower():
                return "skipped: the first browser it finds here is Edge, which is never started on this machine"
            return ""
    return "skipped: it names msedge and none of the browser paths it names is a Chrome that exists here"


def run_body(tree, home, key, line):
    path = line["path"]
    body = line["body"]
    # Edge is never started on this machine: a body that could reach for it is
    # skipped, not run -- and not even written.
    why = edge_risk(body)
    if why:
        return None, 0.0, why
    target = os.path.join(tree, *path.split("/"))
    os.makedirs(os.path.dirname(target), exist_ok=True)
    with open(target, "w", encoding="utf-8", newline="\n") as f:
        f.write(body)
    cmd = shlex.split(line["command"])
    if cmd[0] == "python":
        cmd[0] = sys.executable
    env = {k: v for k, v in os.environ.items() if not k.startswith("HBAND_")}
    env["TAAD3_HOME"] = home
    started = time.time()
    try:
        run = subprocess.run(cmd, cwd=tree, env=env, capture_output=True, text=True, errors="replace", timeout=TIMEOUT)
    except subprocess.TimeoutExpired:
        return 124, time.time() - started, "timed out after %d s" % TIMEOUT
    return run.returncode, time.time() - started, (run.stdout + run.stderr)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    which = ap.add_mutually_exclusive_group(required=True)
    which.add_argument("--e002", action="store_true")
    which.add_argument("--others", action="store_true")
    ap.add_argument("--rev")
    ap.add_argument("--only")
    ap.add_argument("--report-only", action="store_true")
    ap.add_argument("--ledger")
    args = ap.parse_args()

    path = ledger_path(args.ledger)
    if not os.path.isfile(path):
        print("no ledger at %s (pass --ledger)" % path)
        return 2
    mode = "e002" if args.e002 else "others"
    try:
        bodies = read_bodies(path, EPIC_KEYS[mode])
    except SystemExit as e:
        print(e)
        return 2
    if args.only:
        bodies = [(k, l) for k, l in bodies if args.only in l["path"]]
    if not bodies:
        print("no finish line to run")
        return 2

    scratch = tempfile.mkdtemp(prefix="hband-finish-")
    try:
        tree = os.path.join(scratch, "tree")
        home = os.path.join(scratch, "home")
        os.makedirs(home)
        copy_tree(args.rev, tree)
        results = []
        for key, line in bodies:
            code, secs, out = run_body(tree, home, key, line)
            results.append((key, line["path"], code, secs, out))
            verdict = "SKIPPED" if code is None else ("passes" if code == 0 else "FAILS (exit %s)" % code)
            print("%-6s %-70s %s in %.0f s" % (key, line["path"], verdict, secs), flush=True)
            if code not in (0, None):
                print("\n".join("    " + l for l in out.strip().splitlines()[-25:]), flush=True)
        passed = {p for _, p, c, _, _ in results if c == 0}
        print("passing: %d of %d" % (len(passed), len(results)))
        if args.report_only:
            print("baseline (the paths that exit 0):")
            for p in sorted(passed):
                print("    " + p)
            return 0
        if mode == "e002":
            failed = [p for _, p, c, _, _ in results if c != 0]
        else:
            failed = sorted(p for p in BASELINE if p not in passed and any(p == r[1] for r in results))
        if failed:
            print("MUST PASS and does not: %s" % ", ".join(failed))
            return 1
        print("every body that must pass exits 0")
        return 0
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
