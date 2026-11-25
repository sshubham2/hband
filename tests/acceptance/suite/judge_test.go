package suite

// This file reads go test's -json events and judges the two whole-suite runs. It has
// no build tag, so its corpus test (corpus_test.go) runs under go test -count=1 ./...;
// the runs themselves (suite_test.go) stay behind the sc042suite tag.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// browserTests are the tests that need a real browser: without one they must skip,
// with Chrome they must pass.
var browserTests = []string{"TestE004FoldAndFitInBrowser", "TestTabEmulatesNarrowViewport", "TestReportScriptsNeverRun"}

// mayAlsoSkip are tests that skip on some machines for a reason that is not the
// browser, each measured: a symbolic link needs a privilege an ordinary Windows token
// may lack, and a GOFLAGS -overlay cannot carry a temporary folder with a space. A
// subtest is listed by its whole name (TestX/sub).
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

// verdict is the last pass, fail or skip go test reported for one test, a subtest
// (TestX/sub, TestX/sub/deeper) as much as a top-level test.
type verdict struct {
	Package, Test, Action string
}

func (v verdict) key() string { return v.Package + "/" + v.Test }

// sub says whether v is a subtest.
func (v verdict) sub() bool { return strings.Contains(v.Test, "/") }

// top is the top-level test v is, or is under.
func (v verdict) top() string {
	if i := strings.Index(v.Test, "/"); i >= 0 {
		return v.Test[:i]
	}
	return v.Test
}

type result struct {
	verdicts map[string]verdict  // package/test, subtests too -> its last verdict
	output   map[string][]string // package/test, subtests too -> what it printed
	exit     int
	text     string
}

// parse reads a go test -json stream. Every test's verdict is kept, at every level:
// a subtest that skips under a passing parent is a skip go test reported.
func parse(out []byte) result {
	r := result{verdicts: map[string]verdict{}, output: map[string][]string{}, text: string(out)}
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
			r.verdicts[key] = verdict{e.Package, e.Test, e.Action}
		}
	}
	return r
}

// sorted is every verdict, in the order of its key.
func (r result) sorted() []verdict {
	out := make([]verdict, 0, len(r.verdicts))
	for _, v := range r.verdicts {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// said is what the test at key printed, and what its own subtests printed.
func (r result) said(key string) string {
	var keys []string
	for k := range r.output {
		if k == key || strings.HasPrefix(k, key+"/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(strings.Join(r.output[k], ""))
	}
	return b.String()
}

func (r result) topLevel() int {
	n := 0
	for _, v := range r.verdicts {
		if !v.sub() {
			n++
		}
	}
	return n
}

// isAcceptance says whether a top-level test name is an acceptance test of E-001..E-006.
func isAcceptance(name string) bool {
	return len(name) > 7 && strings.HasPrefix(name, "TestE00") && name[7] >= '1' && name[7] <= '6'
}

// judgeWithout judges a whole-suite run with CHROME_BIN unset: the browser tests skip
// saying CHROME_BIN is not set; no other top-level test skips unless allow lists it;
// no subtest of an acceptance test skips unless allow lists it; and every acceptance
// test that is not a browser test passes.
func judgeWithout(r result, allow map[string]string) (problems, notes []string) {
	skipped := map[string]bool{}
	var subSkips []string
	acceptance := 0
	for _, v := range r.sorted() {
		browser := contains(browserTests, v.top())
		why, allowed := allow[v.Test]
		if v.Action == "skip" {
			switch {
			case allowed:
				notes = append(notes, fmt.Sprintf("%s skipped here: %s", v.Test, why))
			case !v.sub() && !browser:
				problems = append(problems, fmt.Sprintf("%s skipped, and it is not a browser test: %s", v.key(), r.said(v.key())))
			case v.sub() && isAcceptance(v.top()) && !browser:
				problems = append(problems, fmt.Sprintf("%s skipped, a subtest of the acceptance test %s: %s", v.key(), v.top(), r.said(v.key())))
			}
			if !v.sub() {
				skipped[v.Test] = true
			} else {
				subSkips = append(subSkips, v.Test)
			}
		}
		if !v.sub() && isAcceptance(v.Test) && !browser {
			acceptance++
			if v.Action != "pass" {
				problems = append(problems, fmt.Sprintf("the acceptance test %s did not pass (%s)", v.key(), v.Action))
			}
		}
	}
	for _, n := range browserTests {
		if !skipped[n] {
			problems = append(problems, fmt.Sprintf("%s did not skip with CHROME_BIN unset", n))
			continue
		}
		said := ""
		for _, v := range r.sorted() {
			if !v.sub() && v.Test == n {
				said += r.said(v.key())
			}
		}
		if !strings.Contains(said, "CHROME_BIN is not set") {
			problems = append(problems, fmt.Sprintf("%s skipped without saying CHROME_BIN is not set: %q", n, said))
		}
	}
	if acceptance < 16 {
		problems = append(problems, fmt.Sprintf("only %d acceptance tests ran; the six epics have 17, one of them the browser test", acceptance))
	}
	notes = append(notes, fmt.Sprintf("%d top-level tests, %d acceptance tests passed, skipped: %v, subtests skipped: %v", r.topLevel(), acceptance, sortedKeys(skipped), subSkips))
	return problems, notes
}

// judgeChrome judges a whole-suite run with CHROME_BIN naming Chrome: every browser
// test passes with nothing under it skipped, and no test that skipped, a subtest as
// much as a top-level test, said CHROME_BIN.
func judgeChrome(r result) (problems, notes []string) {
	vs := r.sorted()
	for _, n := range browserTests {
		passed := false
		for _, v := range vs {
			if !v.sub() && v.Test == n && v.Action == "pass" {
				passed = true
			}
			if v.sub() && v.top() == n && v.Action == "skip" {
				problems = append(problems, fmt.Sprintf("%s passed with a subtest that skipped, %s: %s", n, v.key(), r.said(v.key())))
			}
		}
		if !passed {
			problems = append(problems, fmt.Sprintf("%s did not pass in the browser CHROME_BIN names", n))
		}
	}
	var skipped []string
	for _, v := range vs {
		if v.Action != "skip" {
			continue
		}
		skipped = append(skipped, v.Test)
		if said := r.said(v.key()); strings.Contains(said, "CHROME_BIN") {
			problems = append(problems, fmt.Sprintf("%s skipped saying CHROME_BIN with CHROME_BIN set: %s", v.key(), said))
		}
	}
	notes = append(notes, fmt.Sprintf("%d top-level tests, skipped: %v", r.topLevel(), skipped))
	return problems, notes
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
