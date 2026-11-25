//go:build sc042suite

package suite

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// subtestSkipSource is a test, added to internal/testutil/fakegithub only through a
// Go overlay, whose subtest skips saying CHROME_BIN while the test itself passes.
const subtestSkipSource = "package fakegithub\n\nimport \"testing\"\n\nfunc TestSC042SubtestSkipsForWantOfChromeBin(t *testing.T) {\n\tt.Run(\"caller\", func(t *testing.T) {\n\t\tt.Skip(\"CHROME_BIN is not set: set it to the path of a Chrome executable (not Edge) to run this check\")\n\t})\n}\n"

func lastOf(b []byte) string {
	if len(b) > 4000 {
		return "..." + string(b[len(b)-4000:])
	}
	return string(b)
}

// With CHROME_BIN naming Chrome, a whole-suite run in which any test, a subtest
// included, skips saying CHROME_BIN must fail TestSuiteInChrome: E-009 line 2 refuses
// every such skip, and a real t.Run subtest that skips under a passing parent is the
// shape TestBrowserSkipEndsTheCallingTest had before it moved into a child process.
func TestSuiteInChromeRefusesASubtestSkipNamingChromeBin(t *testing.T) {
	if os.Getenv("CHROME_BIN") == "" {
		t.Skip("TestSuiteInChrome runs only with CHROME_BIN set")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go tool on PATH: %v", err)
	}
	gomod, err := exec.Command(goTool, "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	root := filepath.Dir(strings.TrimSpace(string(gomod)))
	dir, err := os.MkdirTemp("", "sc042-subskip-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if strings.ContainsAny(dir, " \t") {
		t.Skip("the temporary folder's path has a space, which a GOFLAGS -overlay cannot carry")
	}
	src := filepath.Join(dir, "skip_test.go")
	if err := os.WriteFile(src, []byte(subtestSkipSource), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay, _ := json.Marshal(map[string]map[string]string{"Replace": {
		filepath.Join(root, "internal", "testutil", "fakegithub", "sc042_subtest_skip_test.go"): src,
	}})
	jsonPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(jsonPath, overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "GOFLAGS=") {
			env = append(env, kv)
		}
	}
	env = append(env, "GOFLAGS="+strings.TrimSpace(os.Getenv("GOFLAGS")+" -overlay="+jsonPath))
	cmd := exec.Command(goTool, "test", "-count=1", "-tags", "sc042suite", "-timeout", "30m", "-v", "-run", "^TestSuiteInChrome$", "./tests/acceptance/suite")
	cmd.Dir, cmd.Env = root, env
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--- FAIL: TestSuiteInChrome") {
		t.Fatalf("TestSuiteInChrome accepted a whole-suite run, with CHROME_BIN naming a browser, in which TestSC042SubtestSkipsForWantOfChromeBin/caller skipped saying CHROME_BIN (err %v):\n%s", err, lastOf(out))
	}
}
