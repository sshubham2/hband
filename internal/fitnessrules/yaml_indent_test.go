package fitnessrules

import "testing"

// TestYAMLScanRefusesMisindentedRuleKey: a rule key indented less than the keys
// beside it (three spaces where the rule's mapping starts at four) is refused by a
// YAML parser ("expected <block end>, but found '<block mapping start>'"), and the
// line loader in rules_test.go, which only reads keys at exactly four spaces, skips
// it without a word, so the rule silently loses that key. The scan must refuse it.
func TestYAMLScanRefusesMisindentedRuleKey(t *testing.T) {
	src := "rules:\n  - name: r\n    must_not_match:\n    - x\n   files: internal/comments/**/*.go\n"
	got := yamlScalarRefusals(src)
	if len(got) == 0 {
		t.Fatalf("a rule key at three spaces under a mapping at four is not refused")
	}
	if n := firstLine(got); n != 5 {
		t.Errorf("first refused at line %d, want 5: %v", n, got)
	}
}
