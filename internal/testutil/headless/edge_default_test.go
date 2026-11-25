package headless

import (
	"path/filepath"
	"strings"
	"testing"
)

// Microsoft Edge must never be started on this machine: its first run signs the
// profile in with the Windows Microsoft account and imports the person's old
// browsing history. So Browser refuses a CHROME_BIN that names Edge, by name and
// whether or not the file exists, exactly as it refuses an unset CHROME_BIN:
// the test skips, with a message that names CHROME_BIN, and no path comes back.
// Browser only looks the path up here; nothing is started.
func TestBrowserNeverDefaultsToEdge(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "msedge.exe")
	cases := []struct {
		name string
		path string
	}{
		{"msedge.exe", fileIn(t, "msedge.exe")},
		{"upper case", fileIn(t, "MSEDGE.EXE")},
		{"no extension", fileIn(t, "msedge")},
		{"linux package name", fileIn(t, "microsoft-edge")},
		{"does not exist", missing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CHROME_BIN", c.path)
			t.Setenv("HBAND_TEST_BROWSER", "")

			r := &recorder{TB: t}
			got := Browser(r)
			if !r.skipped {
				t.Fatalf("CHROME_BIN=%q is Edge, and Browser did not skip: it returned %q", c.path, got)
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
