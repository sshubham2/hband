package headless

import (
	"path/filepath"
	"strings"
	"testing"
)

// With HBAND_TEST_BROWSER unset the default must never be Microsoft Edge: on
// this machine a new Edge profile is signed in to the Windows Microsoft account
// and imports the person's old browsing history at first run. Browser only
// looks the path up here; nothing is started.
func TestDefaultBrowserIsNeverEdge(t *testing.T) {
	t.Setenv("HBAND_TEST_BROWSER", "")
	p := Browser(t)
	if strings.EqualFold(filepath.Base(p), "msedge.exe") {
		t.Fatalf("HBAND_TEST_BROWSER unset, and Browser chose Edge by default: %s", p)
	}
}
