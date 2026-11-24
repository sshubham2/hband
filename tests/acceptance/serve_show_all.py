"""Start the program on a port with a populated scratch data folder, to look at its pages.

    python -I tests/acceptance/serve_show_all.py PORT

The program needs HBAND_* settings a bare command line cannot carry, so this builds
./cmd/hband into a scratch folder, starts it on 127.0.0.1:PORT with dummy GitHub
credentials (sign-in is not exercised) and HBAND_TRUSTED_PROXIES=127.0.0.1, then
writes a few published reports with likes, comments and opens straight into its
database, so the show-all page at /reports has rows of every kind to draw: counts of
0, 1 and many, a long title, a title that ends in a digit. It keeps running until
it is stopped. On Windows the program is put in a job object that is killed with
this process, so stopping the launcher stops the program too. Nothing is written
outside the scratch folder.
"""
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

REPORTS = [
    # slug, title, likes, comments, views, published (unix)
    ("harbour-survey", "Harbour Survey 2", 12, 3, 120, 1795000000),
    ("lantern-survey", "Lantern Survey", 1, 1, 1, 1794000000),
    ("quiet-survey", "A report with a deliberately long title that has to wrap on a narrow phone screen without being clipped or pushing the numbers off it", 0, 0, 0, 1793000000),
    ("big-numbers", "Big Numbers", 1000, 25, 1000, 1792000000),
]


def kill_with_me(proc):
    """Windows: a job object that kills the program when this process ends."""
    if os.name != "nt":
        return
    import ctypes
    from ctypes import wintypes

    k32 = ctypes.WinDLL("kernel32", use_last_error=True)
    job = k32.CreateJobObjectW(None, None)

    class Basic(ctypes.Structure):
        _fields_ = [("PerProcessUserTimeLimit", ctypes.c_int64), ("PerJobUserTimeLimit", ctypes.c_int64),
                    ("LimitFlags", wintypes.DWORD), ("MinimumWorkingSetSize", ctypes.c_size_t),
                    ("MaximumWorkingSetSize", ctypes.c_size_t), ("ActiveProcessLimit", wintypes.DWORD),
                    ("Affinity", ctypes.c_size_t), ("PriorityClass", wintypes.DWORD), ("SchedulingClass", wintypes.DWORD)]

    class IoCounters(ctypes.Structure):
        _fields_ = [(n, ctypes.c_uint64) for n in ("a", "b", "c", "d", "e", "f")]

    class Extended(ctypes.Structure):
        _fields_ = [("Basic", Basic), ("Io", IoCounters), ("ProcessMemoryLimit", ctypes.c_size_t),
                    ("JobMemoryLimit", ctypes.c_size_t), ("PeakProcessMemoryUsed", ctypes.c_size_t),
                    ("PeakJobMemoryUsed", ctypes.c_size_t)]

    info = Extended()
    info.Basic.LimitFlags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    k32.SetInformationJobObject(job, 9, ctypes.byref(info), ctypes.sizeof(info))
    k32.AssignProcessToJobObject(job, wintypes.HANDLE(int(proc._handle)))
    return job  # kept alive by the caller: closing it (or dying) kills the program


def main():
    port = sys.argv[1]
    scratch = tempfile.mkdtemp(prefix="hband-screens-")
    exe = os.path.join(scratch, "hband.exe" if os.name == "nt" else "hband")
    data = os.path.join(scratch, "data")
    try:
        build = subprocess.run(["go", "build", "-o", exe, "./cmd/hband"], cwd=ROOT, capture_output=True, text=True)
        if build.returncode != 0:
            print(build.stdout + build.stderr)
            return 1
        env = {k: v for k, v in os.environ.items() if not k.startswith("HBAND_")}
        env.update(HBAND_ADDR="127.0.0.1:" + port, HBAND_DATA_DIR=data, HBAND_BASE_URL="http://127.0.0.1:" + port,
                   HBAND_OWNER_GITHUB_ID="4242", HBAND_GITHUB_CLIENT_ID="screens", HBAND_GITHUB_CLIENT_SECRET="screens-secret",
                   HBAND_TRUSTED_PROXIES="127.0.0.1")
        proc = subprocess.Popen([exe], cwd=scratch, env=env)
        job = kill_with_me(proc)
        for _ in range(100):
            try:
                if urllib.request.urlopen("http://127.0.0.1:%s/healthz" % port, timeout=2).status == 200:
                    break
            except OSError:
                time.sleep(0.2)
        else:
            print("the program did not come up")
            return 1
        con = sqlite3.connect(os.path.join(data, "hband.db"))
        con.execute("PRAGMA foreign_keys = ON")
        for slug, title, likes, comments, views, published in REPORTS:
            html = "<!doctype html><title>%s</title><p>%s</p>" % (title, title)
            rid = con.execute("INSERT INTO reports (slug, title, status, published_at, size_bytes, sha256, created_at, updated_at)"
                              " VALUES (?, ?, 'published', ?, ?, ?, ?, ?)", (slug, title, published, len(html), "0" * 64, published, published)).lastrowid
            with open(os.path.join(data, "reports", "%d.html" % rid), "w", encoding="utf-8") as f:
                f.write(html)
            for i in range(max(likes, comments, 1)):
                uid = 100000 + i
                con.execute("INSERT OR IGNORE INTO users (github_id, login, avatar_url, created_at, updated_at) VALUES (?, ?, '', 1, 1)", (uid, "reader%d" % uid))
            for i in range(likes):
                con.execute("INSERT INTO votes (report_id, github_id, kind, created_at, updated_at) VALUES (?, ?, 'up', 1, 1)", (rid, 100000 + i))
            for i in range(comments):
                con.execute("INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (?, NULL, ?, ?, ?)",
                            (rid, 100000 + i, "a comment from reader %d" % i, published + i))
            for i in range(views):
                con.execute("INSERT INTO report_opens (report_id, day, visitor) VALUES (?, 20000, ?)", (rid, ("screens-%07d" % i).encode()[:16].ljust(16, b"_")))
        con.commit()
        con.close()
        print("serving on http://127.0.0.1:%s/ (reports at /reports)" % port, flush=True)
        proc.wait()
        del job
        return 0
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
