"""Run E-004's finish line 2 (and, with `look`, a signed-in browser look) against a worktree, safely.

Why this exists: the approved finish body (epics.finish in the engine ledger, epics id 42, item 2) picks its browser from
a hard-coded list that has msedge.exe in it and never reads CHROME_BIN. This machine's rule is that Edge is never
started (lessons #946, #947). So build, code-review and validate all run the body through THIS one scripted step, which
never edits the approved body in the ledger:

  1. reads the body read-only (file:<engine.db>?mode=ro, a SELECT, closed at once);
  2. points ROOT at the worktree (the replacement must match exactly once);
  3. replaces the browser list with the one browser CHROME_BIN names (the replacement must match exactly once);
  4. asserts the patched text no longer mentions Edge anywhere (case-insensitive);
  5. refuses a browser whose file name is Edge's: CHROME_BIN is the only source of the browser path;
  6. exits 3 when CHROME_BIN is unset, empty, not a file, or names Edge (never 0, never the test's own 1), 4 when the
     patch does not apply;
  7. runs the patched copy from a scratch folder with `python -I`, and exits with its exit code.

Exit codes: 0 the test passed, 1 it failed (the test's own), 2 bad usage or no ledger, 3 no Chrome, 4 patch mismatch.

    python run_finish_e004_2.py [--root <worktree>] [--db <engine.db>] [--scratch <dir>]      finish line 2
    python run_finish_e004_2.py look [--root ..] [--db ..] [--scratch ..] [--shots <dir>]     signed-in look (see look())
    python run_finish_e004_2.py serve --port N [--root ..] [--db ..]    the product on N, seeded, until killed (see serve())

`look` drives the finish test's own Product, Browser and Chrome classes (a built binary, a stand-in GitHub, a headless
Chrome over DevTools on a free port), signed in, with Emulation.setDeviceMetricsOverride (mobile) for 360 and 400 px and a
desktop override for 1000 px, not --window-size (which has a floor near 492 px).
"""
import importlib.util, json, os, re, shutil, sqlite3, subprocess, sys, tempfile, threading, time

EPIC_ID = 42  # E-004
EDGE = "ms" + "edge"  # spelled in two pieces: this file is read by the same search that asserts the patched body has none


def arg(name, default=None):
    return sys.argv[sys.argv.index(name) + 1] if name in sys.argv else default


def chrome():
    """The one browser this run may start: the file CHROME_BIN names, and only that. Never Edge, and no fallback to a
    path written here. Exits 3 (stderr says why) when CHROME_BIN is unset, empty, not a file, or Edge's."""
    p = os.environ.get("CHROME_BIN")
    if not p:
        print("no Chrome: CHROME_BIN is unset or empty", file=sys.stderr)
        sys.exit(3)
    if os.path.basename(p).lower().startswith(EDGE):
        print(f"refusing CHROME_BIN={p}: that is Edge, which is never started on this machine", file=sys.stderr)
        sys.exit(3)
    if not os.path.isfile(p):
        print(f"no Chrome: CHROME_BIN={p} is not a file", file=sys.stderr)
        sys.exit(3)
    return p


def patched_body(root, db, fixed_port=False):
    """The finish body with ROOT and the browser list replaced, each exactly once, and no mention of Edge left.
    fixed_port (serve mode only): the product listens on $SL008_PORT, so the engine can choose the port."""
    con = sqlite3.connect("file:" + db.replace("\\", "/") + "?mode=ro", uri=True)
    try:
        row = con.execute("SELECT finish FROM epics WHERE id = ?", (EPIC_ID,)).fetchone()
    finally:
        con.close()
    if not row:
        print(f"no epic {EPIC_ID} in {db}", file=sys.stderr)
        sys.exit(2)
    items = [i for i in json.loads(row[0]) if "test_e004_2" in i.get("path", "")]
    if len(items) != 1:
        print(f"expected exactly one finish line for test_e004_2, found {len(items)}", file=sys.stderr)
        sys.exit(4)
    body = items[0]["body"]
    exe = chrome()
    for name, pattern, new in (
        ("ROOT", r"^ROOT = os\.path\.dirname\(os\.path\.dirname\(os\.path\.dirname\(os\.path\.abspath\(__file__\)\)\)\)$",
         "ROOT = " + repr(os.path.abspath(root))),
        ("browser list", r"exe = \[p for p in \(.*?\) if p and os\.path\.isfile\(p\)\]", "exe = [" + repr(exe) + "]"),
        *((("port", r"self\.port = s\.getsockname\(\)\[1\]", "self.port = int(os.environ[\"SL008_PORT\"])"),) if fixed_port else ()),
    ):
        body, n = re.subn(pattern, lambda m: new, body, flags=re.S | re.M)
        if n != 1:
            print(f"the patch for the {name} matched {n} times, not once: the approved body is not what this script expects", file=sys.stderr)
            sys.exit(4)
    if re.search(EDGE, body, re.I):
        print("the patched body still mentions Edge", file=sys.stderr)
        sys.exit(4)
    return body


def write_scratch(root, db, scratch, fixed_port=False):
    scratch = os.path.abspath(scratch or tempfile.mkdtemp(prefix="sl008-finish-"))
    folder = os.path.join(scratch, "tests", "acceptance")
    os.makedirs(folder, exist_ok=True)
    path = os.path.join(folder, "test_e004_2_deep_threads_and_toggle.py")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(patched_body(root, db, fixed_port))
    return path


def finish(root, db, scratch):
    path = write_scratch(root, db, scratch)
    return subprocess.run([sys.executable, "-I", "-X", "utf8", path], cwd=os.path.dirname(path)).returncode


def seed(mod, app, step):
    """The look's thread, posted through the real handlers; returns the three sign-ins, the slug, the ids and the branch path."""
    owner = app.sign_in(app.owner)
    slug = "e004-look"
    app.publish(owner, slug, "Look Notes")
    step("report published")
    long_login = "w" * 39  # 39 characters, the most GitHub allows, with no hyphen to break at and the widest letter
    assert len(long_login) == 39
    who = app.sign_in(app.account(8801, long_login))
    reader = app.sign_in(app.account(8802, "plain-reader"))
    step("accounts signed in")
    ids, parent, branch = [], None, None
    for level in range(1, 10):
        # level 5 is the owner (the Author badge); levels 4 and 9 are by the 39-character login with an unbroken body,
        # and level 9 is the cut comment (relative level 5) of the branch page rooted at level 5
        poster = owner if level == 5 else who if level in (4, 9) else reader
        text = "UNBROKEN" * 15 if level in (4, 9) else f"level {level} text"
        page = poster.go(f"/reports/{slug}" if level <= 5 else branch)
        got = poster.post(f"/reports/{slug}/comments", {"body": text, **({"parent": parent} if parent else {})}, page)
        m = re.search(r"#comment-(\d+)$", got.url)
        assert m, f"level {level}: landed on {got.url}"
        parent = m.group(1)
        ids.append(parent)
        if level == 5:
            branch = f"/reports/{slug}/comments/{parent}"
        if level > 5:
            assert got.url.split("#")[0].endswith(branch), f"level {level} landed on {got.url}, not on {branch}"
        step(f"level {level} posted, landed on {got.url}")
    return owner, who, reader, slug, ids, branch


def kill_with_me(proc):
    """Windows: put the child in a job that dies with this process, so a killed server run leaves no hband.exe behind."""
    import ctypes
    from ctypes import wintypes
    k32 = ctypes.windll.kernel32
    k32.CreateJobObjectW.restype = wintypes.HANDLE
    job = k32.CreateJobObjectW(None, None)

    class Limits(ctypes.Structure):
        _fields_ = [("a", ctypes.c_longlong), ("b", ctypes.c_longlong), ("flags", wintypes.DWORD), ("c", ctypes.c_size_t),
                    ("d", ctypes.c_size_t), ("e", wintypes.DWORD), ("f", ctypes.c_size_t), ("g", wintypes.DWORD),
                    ("h", wintypes.DWORD)] + [("io%d" % i, ctypes.c_ulonglong) for i in range(6)] + [
                    ("p", ctypes.c_size_t), ("q", ctypes.c_size_t), ("r", ctypes.c_size_t), ("s", ctypes.c_size_t)]
    info = Limits()
    info.flags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    k32.SetInformationJobObject(wintypes.HANDLE(job), 9, ctypes.byref(info), ctypes.sizeof(info))
    k32.AssignProcessToJobObject(wintypes.HANDLE(job), wintypes.HANDLE(int(proc._handle)))
    return job


def serve(root, db, scratch, port):
    """Start the product the way the finish test does (built binary, stand-in GitHub, fresh data folder) on `port`,
    seeded with the look's thread, and keep it running until killed. Anonymous pages: / , /reports/e004-look and
    /reports/e004-look/comments/5 (the branch page). This is what the engine's `app` command runs."""
    os.environ["SL008_PORT"] = str(port)
    path = write_scratch(root, db, scratch, fixed_port=True)
    spec = importlib.util.spec_from_file_location("finish_e004_2", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    app = mod.Product()
    job = kill_with_me(app.proc)  # held for the life of this process
    try:
        seed(mod, app, lambda name: print("step:", name, flush=True))
        print(f"serving http://127.0.0.1:{port}/", flush=True)
        while app.proc.poll() is None:
            time.sleep(1)
    finally:
        app.stop()
    return 1


def look(root, db, scratch, shots):
    """A signed-in look at a depth-5 branch page, on a built binary, in the finish test's own Chrome.

    Seeded through the real handlers: a chain of nine comments (so the branch page rooted at level 5 has a cut comment
    at its own level 5), the owner (Author badge), a reader whose login is 39 characters and a comment body of 120
    unbroken characters, and the comment at the cap with its reply form opened. For each viewport (360 and 400 mobile,
    1000 desktop) it asserts document.documentElement.scrollWidth <= the width asked for (not innerWidth: an overflowing mobile page widens that too) on the branch page and on the report
    page, then, signed in, that the toggle sets hidden on replies-<id> and aria-expanded=false and a second click
    restores both. Each step is timed; a step that takes over STEP seconds, or any failure, says the last step done.
    """
    STEP = 90
    path = write_scratch(root, db, scratch)
    spec = importlib.util.spec_from_file_location("finish_e004_2", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    done, stamp = ["nothing yet"], [time.time()]
    results, app, tab = [], None, None

    def step(name):
        done.append(name)
        stamp[0] = time.time()
        print("step:", name, flush=True)

    def watchdog():  # a step that takes over STEP seconds ends the run, naming the last step done, children killed
        while True:
            time.sleep(2)
            if time.time() - stamp[0] > STEP:
                print(f"TIMEOUT: no step finished for {STEP}s; last step done: {done[-1]}", file=sys.stderr, flush=True)
                for p in (getattr(tab, "proc", None), getattr(app, "proc", None)):
                    if p is not None and p.poll() is None:
                        p.kill()
                os._exit(5)
    threading.Thread(target=watchdog, daemon=True).start()
    try:
        app = mod.Product()
        step("product started")
        owner, who, reader, slug, ids, branch = seed(mod, app, step)
        tab = mod.Chrome(app.port)
        tab.b = reader  # the DevTools Fetch bridge forwards the page's requests with this visitor's cookies
        step("chrome started")
        for width, height, mobile in ((360, 780, True), (400, 860, True), (1000, 900, False)):
            tab.call("Emulation.setDeviceMetricsOverride", width=width, height=height, deviceScaleFactor=2 if mobile else 1, mobile=mobile)
            for where in (branch, f"/reports/{slug}"):
                tab.open(mod.BASE + where)
                tab.js("document.querySelectorAll('details.reply').forEach(d => { d.open = true; })")
                wide = tab.js("[document.documentElement.scrollWidth, window.innerWidth]")
                step(f"{width}px {where}: scrollWidth {wide[0]}, innerWidth {wide[1]}")
                # against the width asked for, not innerWidth: a mobile page that overflows widens its own layout viewport
                results.append((width, where, wide[0] <= width, wide))
                if shots and where == branch:
                    os.makedirs(shots, exist_ok=True)
                    shot = tab.call("Page.captureScreenshot", format="png")["data"]
                    with open(os.path.join(shots, f"branch-{width}.png"), "wb") as fh:
                        fh.write(__import__("base64").b64decode(shot))
        tab.call("Emulation.setDeviceMetricsOverride", width=400, height=860, deviceScaleFactor=2, mobile=True)
        tab.open(mod.BASE + branch)
        state = tab.js("""(async () => {
          const out = {};
          const root = document.getElementById("comment-%s");
          const btn = [...root.children].find(e => e.matches("button.toggle"));
          const replies = document.getElementById(btn.getAttribute("aria-controls"));
          const pause = () => new Promise(r => setTimeout(r, 300));
          out.shown = !btn.hidden; out.before = [replies.hidden, btn.getAttribute("aria-expanded"), btn.textContent];
          btn.click(); await pause();
          out.folded = [replies.hidden, btn.getAttribute("aria-expanded"), btn.textContent, replies.getClientRects().length];
          btn.click(); await pause();
          out.after = [replies.hidden, btn.getAttribute("aria-expanded"), btn.textContent, replies.getClientRects().length > 0];
          out.replyOpen = [...document.querySelectorAll("details.reply")].length;
          return JSON.stringify(out);
        })()""" % ids[4])
        step("toggle clicked twice: " + state)
        s = json.loads(state)
        assert s["shown"] and s["before"] == [False, "true", "Hide replies"], s
        assert s["folded"] == [True, "false", "Show replies", 0], s
        assert s["after"] == [False, "true", "Hide replies", True], s
    except BaseException as e:
        print(f"FAILED after step {done[-1]!r}: {type(e).__name__}: {e}", file=sys.stderr)
        for r in results:
            print("  ", r, file=sys.stderr)
        return 1
    finally:
        if tab:
            tab.close()
        if app:
            app.stop()
    for width, where, ok, wide in results:
        print(("PASS" if ok else "FAIL"), width, where, wide)
    return 0 if all(r[2] for r in results) else 1


if __name__ == "__main__":
    mode = sys.argv[1] if sys.argv[1:2] in (["look"], ["serve"]) else "finish"
    root = arg("--root", os.getcwd())
    db = arg("--db", os.path.join(os.environ.get("APPDATA", ""), "taad3", "engine.db"))
    if not os.path.isfile(db):
        print(f"no ledger at {db}", file=sys.stderr)
        sys.exit(2)
    if mode == "serve":
        sys.exit(serve(root, db, arg("--scratch"), int(arg("--port"))))
    if mode == "look":
        sys.exit(look(root, db, arg("--scratch"), arg("--shots")))
    sys.exit(finish(root, db, arg("--scratch")))
