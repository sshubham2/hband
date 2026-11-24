"""Repro: with CHROME_BIN unset, run_finish.py must not run a finish body that
reaches for Edge. The browser comes from CHROME_BIN only; where it is unset the
body is skipped or the run fails, and no install path written into the script
stands in for it."""
import importlib.util
import os
import shutil
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("run_finish", os.path.join(HERE, "run_finish.py"))
rf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rf)

os.environ.pop("CHROME_BIN", None)
scratch = tempfile.mkdtemp(prefix="rf-chrome-bin-")
try:
    tree = os.path.join(scratch, "tree")
    home = os.path.join(scratch, "home")
    os.makedirs(tree)
    os.makedirs(home)
    line = {
        "path": "tests/acceptance/edge_body/body.txt",
        "body": "browser := `C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe`\n",
        "command": "python -c \"open('ran.txt','w').close()\"",
    }
    try:
        rf.run_body(tree, home, "E-004", line)
    except BaseException:
        pass
    if os.path.exists(os.path.join(tree, "ran.txt")):
        print("FAIL: CHROME_BIN is unset, yet run_finish.py ran a body that names msedge: it stood a browser path written into the script in for CHROME_BIN")
        sys.exit(1)
    print("ok: the body was not run")
finally:
    shutil.rmtree(scratch, ignore_errors=True)
