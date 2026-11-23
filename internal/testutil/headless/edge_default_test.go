package headless

import (
	"strings"
	"testing"
)

// Microsoft Edge must never be started by default on this machine: its first
// run signs the profile in with the Windows Microsoft account and imports the
// person's old browsing history. With HBAND_TEST_BROWSER unset, Browser must
// skip or pick another Chromium-family browser, never Edge.
func TestBrowserNeverDefaultsToEdge(t *testing.T) {
	t.Setenv("HBAND_TEST_BROWSER", "")
	got := Browser(t)
	if strings.Contains(strings.ToLower(got), "msedge") {
		t.Fatalf("with HBAND_TEST_BROWSER unset, Browser chose %q: Edge must not be started by default", got)
	}
}
