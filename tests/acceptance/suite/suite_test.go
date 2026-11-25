//go:build sc042suite

// Package suite checks the person's one command, go test -count=1 ./..., as a whole:
// without a browser, only the browser tests skip and every acceptance test runs and
// passes; with CHROME_BIN naming Chrome, the browser tests run and pass and nothing
// skips for want of CHROME_BIN. It reads go test's -json events, so a skip is known
// by the test that reported it, not guessed from the text around it. The build tag
// keeps it out of the suite it runs:
//
//	go test -count=1 -tags sc042suite -timeout 30m -run ^TestSuiteWithoutABrowser$ ./tests/acceptance/suite
//	go test -count=1 -tags sc042suite -timeout 30m -run ^TestSuiteInChrome$ ./tests/acceptance/suite
package suite

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// browserTests are the tests that need a real browser: without one they must skip,
// with Chrome they must pass.
var browserTests = []string{"TestE004FoldAndFitInBrowser", "TestTabEmulatesNarrowViewport", "TestReportScriptsNeverRun"}

// mayAlsoSkip are top-level tests that skip on some machines for a reason that is not
// the browser, each measured: a symbolic link needs a privilege an ordinary Windows
// token may lack, and a GOFLAGS -overlay cannot carry a temporary folder with a space.
var mayAlsoSkip = map[string]string{
	"TestBrowserRefusesARealSymlinkToEdge":    "os.Symlink is not permitted without Developer Mode or elevation",
	"TestBuildTakesGOFLAGSFromTheEnvironment": "the temporary folder's path has a space",
}

type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

type result struct {
	action map[string]string   // top-level test (package/name) -> its last pass, fail or skip
	output map[string][]string // test (package/name, subtests too) -> its own output
	exit   int
	text   string
}

func suite(t *testing.T, env []string) result {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go tool on PATH: %v", err)
	}
	gomod, err := exec.Command(goTool, "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	cmd := exec.Command(goTool, "test", "-count=1", "-json", "./...")
	cmd.Dir, cmd.Env = filepath.Dir(strings.TrimSpace(string(gomod))), env
	out, err := cmd.Output()
	r := result{action: map[string]string{}, output: map[string][]string{}, text: string(out)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.exit = ee.ExitCode()
		r.text += string(ee.Stderr)
	} else if err != nil {
		t.Fatalf("go test ./...: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Test == "" {
			continue
		}
		key := e.Package + "/" + e.Test
		switch e.Action {
		case "output":
			r.output[key] = append(r.output[key], e.Output)
		case "pass", "fail", "skip":
			if !strings.Contains(e.Test, "/") {
				r.action[key] = e.Action
			}
		}
	}
	return r
}

func name(key string) string { return key[strings.LastIndex(key, "/")+1:] }

func envWithout(names ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		drop := false
		for _, n := range names {
			drop = drop || strings.HasPrefix(strings.ToUpper(kv), n+"=")
		}
		if !drop {
			env = append(env, kv)
		}
	}
	return env
}

func tail(s string) string {
	if len(s) > 4000 {
		return "..." + s[len(s)-4000:]
	}
	return s
}

// With CHROME_BIN unset: the suite passes; the browser tests skip with a message
// naming CHROME_BIN; no other top-level test skips but those of mayAlsoSkip; and
// every acceptance test of the six epics that is not a browser test passes.
func TestSuiteWithoutABrowser(t *testing.T) {
	r := suite(t, envWithout("CHROME_BIN"))
	if r.exit != 0 {
		t.Fatalf("go test -count=1 ./... exited %d with CHROME_BIN unset:\n%s", r.exit, tail(r.text))
	}
	skipped := map[string]bool{}
	var keys []string
	for k := range r.action {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	acceptance := 0
	for _, k := range keys {
		n, a := name(k), r.action[k]
		if a == "skip" {
			skipped[n] = true
			if _, ok := mayAlsoSkip[n]; !ok && !contains(browserTests, n) {
				t.Errorf("%s skipped, and it is not a browser test: %s", k, strings.Join(r.output[k], ""))
			}
		}
		if strings.HasPrefix(n, "TestE00") && n[7] >= '1' && n[7] <= '6' && !contains(browserTests, n) {
			acceptance++
			if a != "pass" {
				t.Errorf("the acceptance test %s did not pass (%s)", k, a)
			}
		}
	}
	for _, n := range browserTests {
		if !skipped[n] {
			t.Errorf("%s did not skip with CHROME_BIN unset", n)
			continue
		}
		said := ""
		for k, out := range r.output {
			if name(k) == n {
				said += strings.Join(out, "")
			}
		}
		if !strings.Contains(said, "CHROME_BIN is not set") {
			t.Errorf("%s skipped without saying CHROME_BIN is not set: %q", n, said)
		}
	}
	if acceptance < 16 {
		t.Errorf("only %d acceptance tests ran; the six epics have 17, one of them the browser test", acceptance)
	}
	for n := range skipped {
		if why, ok := mayAlsoSkip[n]; ok {
			t.Logf("%s skipped here: %s", n, why)
		}
	}
	t.Logf("%d top-level tests, %d acceptance tests passed, skipped: %v", len(r.action), acceptance, sortedKeys(skipped))
}

// With CHROME_BIN naming a browser: the suite passes, the browser tests pass, and
// no test that skipped said CHROME_BIN.
func TestSuiteInChrome(t *testing.T) {
	if os.Getenv("CHROME_BIN") == "" {
		t.Skip("CHROME_BIN is not set: set it to the path of a Chrome executable (not Edge) to run this check")
	}
	r := suite(t, os.Environ())
	if r.exit != 0 {
		t.Fatalf("go test -count=1 ./... exited %d with CHROME_BIN set:\n%s", r.exit, tail(r.text))
	}
	for _, n := range browserTests {
		passed := false
		for k, a := range r.action {
			passed = passed || (name(k) == n && a == "pass")
		}
		if !passed {
			t.Errorf("%s did not pass in the browser CHROME_BIN names", n)
		}
	}
	var skipped []string
	for k, a := range r.action {
		if a != "skip" {
			continue
		}
		skipped = append(skipped, name(k))
		for o, out := range r.output {
			if (o == k || strings.HasPrefix(o, k+"/")) && strings.Contains(strings.Join(out, ""), "CHROME_BIN") {
				t.Errorf("%s skipped saying CHROME_BIN with CHROME_BIN set: %s", k, strings.Join(out, ""))
			}
		}
	}
	sort.Strings(skipped)
	t.Logf("%d top-level tests, skipped: %v", len(r.action), skipped)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
