package headless

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder stands in for the calling test. Skip on a real *testing.T ends the
// calling goroutine, so a test could not look at the message or at what Browser
// returned; here Skip is only written down and Browser returns. Anything else
// Browser asks of the test (Helper, Fatalf) goes to the real test, so a Fatal
// still fails it.
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
// place. Browser only looks the path up here; nothing is started.
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
func TestBrowserSkipEndsTheCallingTest(t *testing.T) {
	setEnv(t, "CHROME_BIN", "", true)

	var sub *testing.T
	reached := false
	t.Run("caller", func(st *testing.T) {
		sub = st
		Browser(st)
		reached = true
	})
	if reached {
		t.Fatal("the caller ran on past Browser: it was not stopped")
	}
	if !sub.Skipped() {
		t.Fatal("the caller was not reported as skipped")
	}
}
