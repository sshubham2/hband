package headless

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// recorder stands in for the calling test. Skip on a real *testing.T ends the
// calling goroutine, so a test could not look at the message or at what Browser
// returned; here Skip, Skipf and SkipNow are only written down and Browser
// returns. Anything other than those (Helper) goes to the real test. Browser
// never fails a test: every refusal is a skip.
type recorder struct {
	testing.TB
	skipped bool
	msg     string
}

func (r *recorder) Skip(args ...any) {
	r.skipped = true
	r.msg = fmt.Sprint(args...)
}

func (r *recorder) Skipf(format string, args ...any) {
	r.skipped = true
	r.msg = fmt.Sprintf(format, args...)
}

func (r *recorder) SkipNow() { r.skipped = true }

// fileIn makes an empty file called name in a fresh folder and returns its path.
// Browser only looks the path up: nothing here is ever started.
func fileIn(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// setEnv sets name for this test, or removes it altogether when unset is true.
func setEnv(t *testing.T, name, value string, unset bool) {
	t.Helper()
	t.Setenv(name, value) // registers the restore of whatever the machine had
	if unset {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

// CHROME_BIN naming an existing file is the browser, exactly as written,
// whatever HBAND_TEST_BROWSER says.
func TestBrowserIsExactlyChromeBin(t *testing.T) {
	want := fileIn(t, "my-chrome.exe")
	t.Setenv("CHROME_BIN", want)
	t.Setenv("HBAND_TEST_BROWSER", fileIn(t, "other.exe"))

	r := &recorder{TB: t}
	got := Browser(r)
	if r.skipped {
		t.Fatalf("CHROME_BIN names an existing file, yet Browser skipped: %s", r.msg)
	}
	if got != want {
		t.Fatalf("Browser = %q, want exactly CHROME_BIN %q", got, want)
	}
}

// Every way CHROME_BIN can fail to name a usable browser skips the calling test
// with a message that names CHROME_BIN, and returns no path. That includes a
// machine with Chrome installed in its standard place: nothing is looked up
// except CHROME_BIN. HBAND_TEST_BROWSER is no fallback, whether it names an
// existing file or not.
func TestBrowserSkipsUnlessChromeBinNamesAFile(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		bin   string // CHROME_BIN
		unset bool   // remove CHROME_BIN altogether
		old   string // HBAND_TEST_BROWSER, "" for not set
	}{
		{name: "unset", unset: true},
		{name: "empty", bin: ""},
		{name: "missing path", bin: filepath.Join(dir, "no-such-chrome.exe")},
		{name: "directory", bin: dir},
		{name: "unset, HBAND_TEST_BROWSER names an existing file", unset: true, old: fileIn(t, "old.exe")},
		{name: "empty, HBAND_TEST_BROWSER names an existing file", bin: "", old: fileIn(t, "old2.exe")},
		{name: "missing path, HBAND_TEST_BROWSER names an existing file", bin: filepath.Join(dir, "gone.exe"), old: fileIn(t, "old3.exe")},
		{name: "unset, HBAND_TEST_BROWSER names a missing path", unset: true, old: filepath.Join(dir, "nothing.exe")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setEnv(t, "CHROME_BIN", c.bin, c.unset)
			setEnv(t, "HBAND_TEST_BROWSER", c.old, c.old == "")

			r := &recorder{TB: t}
			got := Browser(r)
			if !r.skipped {
				t.Fatalf("Browser did not skip; it returned %q", got)
			}
			if got != "" {
				t.Fatalf("Browser skipped but still returned %q", got)
			}
			if !strings.Contains(r.msg, "CHROME_BIN") {
				t.Fatalf("the skip message does not name CHROME_BIN: %q", r.msg)
			}
		})
	}
}

// There is no default browser: with CHROME_BIN unset Browser picks none, Edge
// least of all, even when this machine has Edge or Chrome in its standard
// place. With CHROME_BIN unset Browser looks no path up, and nothing is started.
func TestDefaultBrowserIsNeverEdge(t *testing.T) {
	setEnv(t, "CHROME_BIN", "", true)
	setEnv(t, "HBAND_TEST_BROWSER", "", true)

	r := &recorder{TB: t}
	got := Browser(r)
	if got != "" {
		t.Fatalf("CHROME_BIN unset, and Browser chose a default browser anyway: %s", got)
	}
	if !r.skipped {
		t.Fatal("CHROME_BIN unset, and Browser did not skip")
	}
}

// Skip must really end the calling test: a real *testing.T, not the recorder.
// That is shown in a child of this test binary, so that no skip is reported by the
// run that is checking it (a whole-suite run with CHROME_BIN naming Chrome must
// show no SKIP that names CHROME_BIN). The child, when HBAND_TEST_SKIP_CALLER is 1,
// is the caller: it asks Browser with CHROME_BIN unset and logs if it gets past.
func TestBrowserSkipEndsTheCallingTest(t *testing.T) {
	if os.Getenv("HBAND_TEST_SKIP_CALLER") == "1" {
		Browser(t)
		t.Log("reached past Browser")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestBrowserSkipEndsTheCallingTest$", "-test.v", "-test.count=1")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "CHROME_BIN=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "HBAND_TEST_SKIP_CALLER=1")
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("the caller exited with an error: %v\n%s", err, text)
	}
	if !strings.Contains(text, "--- SKIP: TestBrowserSkipEndsTheCallingTest") {
		t.Fatalf("the caller was not reported as skipped:\n%s", text)
	}
	if !strings.Contains(text, "CHROME_BIN") {
		t.Fatalf("the skip does not say CHROME_BIN:\n%s", text)
	}
	if strings.Contains(text, "reached past Browser") {
		t.Fatalf("the caller ran on past Browser: it was not stopped:\n%s", text)
	}
}

// A CHROME_BIN whose name does not say Edge but whose symbolic links resolve to
// Edge is Edge. The resolver is a seam, so this runs on every machine and needs no
// privilege to create a link.
func TestBrowserRefusesWhatSymlinksResolveToEdge(t *testing.T) {
	p := fileIn(t, "chrome.exe")
	old := evalSymlinks
	defer func() { evalSymlinks = old }()
	evalSymlinks = func(string) (string, error) { return `C:\Program Files\Elsewhere\msedge.exe`, nil }
	t.Setenv("CHROME_BIN", p)

	r := &recorder{TB: t}
	got := Browser(r)
	if got != "" || !r.skipped || !strings.Contains(r.msg, "Edge") {
		t.Fatalf("a path resolving to msedge.exe was not refused: got %q skipped=%v msg=%q", got, r.skipped, r.msg)
	}
}

// The same with a real symbolic link, where this machine lets the test make one.
func TestBrowserRefusesARealSymlinkToEdge(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "msedge.exe")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "chrome.exe")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("a symbolic link cannot be made here (%v), so only the resolver seam is exercised", err)
	}
	t.Setenv("CHROME_BIN", link)

	r := &recorder{TB: t}
	got := Browser(r)
	if got != "" || !r.skipped || !strings.Contains(r.msg, "Edge") {
		t.Fatalf("a symbolic link to msedge.exe was not refused: got %q skipped=%v msg=%q", got, r.skipped, r.msg)
	}
}
