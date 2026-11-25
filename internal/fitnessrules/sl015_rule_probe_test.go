package fitnessrules

import "testing"

// A launch flag that is not the first thing in its string literal, and an
// environment read through exec.Cmd's Environ method, are spellings the SL-015
// browser rules must still refuse.
func TestSL015BrowserRulesSeeFlagsInsideALongerStringAndCmdEnviron(t *testing.T) {
	rules := loadRules(t, repoRoot(t))
	cases := []struct{ rule, path, text string }{
		{"acceptance tests neither choose nor start a browser", "tests/acceptance/e004_2_deep_threads_and_toggle/e004_2_deep_threads_and_toggle_test.go",
			"package x\n\nvar args = strings.Fields(\"--no-first-run --headless=new --remote-debugging-port=0\")\n"},
		{"acceptance tests neither choose nor start a browser", "tests/acceptance/e004_2_deep_threads_and_toggle/e004_2_deep_threads_and_toggle_test.go",
			"package x\n\nvar line = fmt.Sprintf(\"%s --user-data-dir=%s about:blank\", bin, dir)\n"},
		{"the DevTools driver takes its browser from Browser and reads no environment", "internal/testutil/headless/devtools.go",
			"package headless\n\nfunc pick() string {\n\tfor _, kv := range (&exec.Cmd{}).Environ() {\n\t\t_ = kv\n\t}\n\treturn \"\"\n}\n"},
	}
	for _, c := range cases {
		r := rules[c.rule]
		if r == nil {
			t.Fatalf("rule %q is not in fitness.yaml", c.rule)
		}
		if fired, skipped := r.fires(c.path, c.text); !fired || len(skipped) > 0 {
			t.Errorf("%s: not refused (skipped %v): %q", c.rule, skipped, c.text)
		}
	}
}
