package headless

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// dotRecorder stands in for the calling test so the skip can be observed.
type dotRecorder struct {
	testing.TB
	skipped bool
	msg     string
}

func (r *dotRecorder) Skip(args ...any) { r.skipped = true; r.msg = fmt.Sprint(args...) }
func (r *dotRecorder) Skipf(format string, args ...any) {
	r.skipped = true
	r.msg = fmt.Sprintf(format, args...)
}
func (r *dotRecorder) SkipNow() { r.skipped = true }

// Windows resolves "." and ".." in a path by its text, so C:\...\msedge.exe\.
// names msedge.exe itself: os.Stat finds it and exec starts it. Browser must
// refuse it as Edge, as it refuses ...\msedge.exe. Nothing is started here.
func TestBrowserRefusesEdgeBehindDotSegments(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows resolves msedge.exe\\. to the file itself")
	}
	edge := filepath.Join(t.TempDir(), "msedge.exe")
	if err := os.WriteFile(edge, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	shapes := map[string]string{
		"trailing dot":       edge + `\.`,
		"two trailing dots":  edge + `\.\.`,
		"up and back":        edge + `\x\..`,
		"slash dot":          edge + `/.`,
		"space before slash": edge + ` \.`,
	}
	for name, p := range shapes {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CHROME_BIN", p)
			r := &dotRecorder{TB: t}
			got := Browser(r)
			if !r.skipped || got != "" {
				t.Fatalf("CHROME_BIN=%q names msedge.exe, yet Browser returned %q without skipping", p, got)
			}
			if !strings.Contains(r.msg, "CHROME_BIN") {
				t.Fatalf("the skip message does not name CHROME_BIN: %q", r.msg)
			}
		})
	}
}
