//go:build sc042suite

// Package suite checks the person's one command, go test -count=1 ./..., as a whole:
// without a browser, only the browser tests skip and every acceptance test runs and
// passes; with CHROME_BIN naming Chrome, the browser tests run and pass and nothing
// skips for want of CHROME_BIN. It reads go test's -json events, every test's and
// every subtest's, so a skip is known by the test that reported it, not guessed from
// the text around it (judge_test.go; its corpus is testdata/skip_events.json). The
// build tag keeps the runs out of the suite they run:
//
//	go test -count=1 -tags sc042suite -timeout 30m -run ^TestSuiteWithoutABrowser$ ./tests/acceptance/suite
//	go test -count=1 -tags sc042suite -timeout 30m -run ^TestSuiteInChrome$ ./tests/acceptance/suite
package suite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	r := parse(out)
	if ee, ok := err.(*exec.ExitError); ok {
		r.exit = ee.ExitCode()
		r.text += string(ee.Stderr)
	} else if err != nil {
		t.Fatalf("go test ./...: %v", err)
	}
	return r
}

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
// naming CHROME_BIN; no other top-level test skips but those of mayAlsoSkip; no
// subtest of an acceptance test skips; and every acceptance test of the six epics that
// is not a browser test passes.
func TestSuiteWithoutABrowser(t *testing.T) {
	r := suite(t, envWithout("CHROME_BIN"))
	if r.exit != 0 {
		t.Fatalf("go test -count=1 ./... exited %d with CHROME_BIN unset:\n%s", r.exit, tail(r.text))
	}
	problems, notes := judgeWithout(r, mayAlsoSkip)
	for _, p := range problems {
		t.Error(p)
	}
	for _, n := range notes {
		t.Log(n)
	}
}

// With CHROME_BIN naming a browser: the suite passes, the browser tests pass with
// nothing under them skipped, and no test or subtest that skipped said CHROME_BIN.
func TestSuiteInChrome(t *testing.T) {
	if os.Getenv("CHROME_BIN") == "" {
		t.Skip("CHROME_BIN is not set: set it to the path of a Chrome executable (not Edge) to run this check")
	}
	r := suite(t, os.Environ())
	if r.exit != 0 {
		t.Fatalf("go test -count=1 ./... exited %d with CHROME_BIN set:\n%s", r.exit, tail(r.text))
	}
	problems, notes := judgeChrome(r)
	for _, p := range problems {
		t.Error(p)
	}
	for _, n := range notes {
		t.Log(n)
	}
}
