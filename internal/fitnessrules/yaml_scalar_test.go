package fitnessrules

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestFitnessYAMLPlainScalarsParse checks that every unquoted (plain) YAML scalar
// in .taad/fitness.yaml is one a YAML parser accepts. A plain scalar may not hold
// ": " or end a line with ":" (YAML reads either as a mapping key), nor " #" (a
// comment). The engine reads this file with a real YAML parser: one bad scalar
// makes the whole file unreadable and no rule in it is enforced.
func TestFitnessYAMLPlainScalarsParse(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".taad", "fitness.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	key := regexp.MustCompile(`^(\s*)(?:- )?([a-z_]+):\s+(.*)$`)
	item := regexp.MustCompile(`^\s*- `)
	bare := regexp.MustCompile(`^\s*[a-z_]+:(\s|$)`)
	indent := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	for i := 0; i < len(lines); {
		m := key.FindStringSubmatch(lines[i])
		if m == nil || m[3] == "" || strings.ContainsRune(`'"|>[{&*!`, rune(m[3][0])) {
			i++
			continue
		}
		type piece struct {
			n int
			v string
		}
		vals := []piece{{i + 1, m[3]}}
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) != "" && indent(lines[j]) > len(m[1]) &&
			!item.MatchString(lines[j]) && !bare.MatchString(lines[j]) {
			vals = append(vals, piece{j + 1, strings.TrimSpace(lines[j])})
			j++
		}
		for _, p := range vals {
			if strings.Contains(p.v, ": ") || strings.HasSuffix(p.v, ":") || strings.Contains(p.v, " #") {
				t.Errorf("fitness.yaml:%d: a plain scalar a YAML parser refuses: %q", p.n, p.v)
			}
		}
		i = j
	}
}
