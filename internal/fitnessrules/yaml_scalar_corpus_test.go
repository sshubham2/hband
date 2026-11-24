package fitnessrules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// yamlRow is one row of testdata/yaml_scalars.json.
type yamlRow struct {
	ID      string `json:"id"`
	Carrier string `json:"carrier"`
	Who     string `json:"who"`
	Expect  string `json:"expect"`
	Line    int    `json:"line"`
	YAML    string `json:"yaml"`
	Why     string `json:"why"`
}

type yamlCorpus struct {
	Carriers []string  `json:"carriers"`
	Rows     []yamlRow `json:"rows"`
}

func loadYAMLCorpus(t *testing.T) yamlCorpus {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "yaml_scalars.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c yamlCorpus
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("yaml_scalars.json: %v", err)
	}
	if len(c.Rows) == 0 {
		t.Fatal("yaml_scalars.json has no rows")
	}
	return c
}

// firstLine is the smallest line number among findings ("N: what").
func firstLine(findings []string) int {
	first := 0
	for _, f := range findings {
		n, err := strconv.Atoi(f[:strings.Index(f, ":")])
		if err == nil && (first == 0 || n < first) {
			first = n
		}
	}
	return first
}

// TestFitnessYAMLScalarCorpus runs every row through the scan: a refuse row must be
// refused, at the line it names; an accept row must pass untouched.
func TestFitnessYAMLScalarCorpus(t *testing.T) {
	c := loadYAMLCorpus(t)
	seen := map[string]bool{}
	for _, r := range c.Rows {
		if seen[r.ID] {
			t.Errorf("the corpus names %q twice", r.ID)
		}
		seen[r.ID] = true
		got := yamlScalarRefusals(r.YAML)
		switch r.Expect {
		case "refuse":
			if len(got) == 0 {
				t.Errorf("%s: not refused (%s):\n%s", r.ID, r.Why, r.YAML)
			} else if n := firstLine(got); n != r.Line {
				t.Errorf("%s: first refused at line %d, want %d: %v", r.ID, n, r.Line, got)
			}
		case "accept":
			if len(got) != 0 {
				t.Errorf("%s: refused though it is fine (%s): %v", r.ID, r.Why, got)
			}
		default:
			t.Errorf("%s: expect %q is neither refuse nor accept", r.ID, r.Expect)
		}
	}
}

// TestFitnessYAMLCorpusCoversEveryCarrier is the census. Every key of the real
// fitness.yaml that holds a scalar is a carrier of the class, and each carrier must
// have a row that is refused and a row that is accepted, so a new kind of free text
// cannot join the file with nobody having asked what a YAML parser makes of it.
func TestFitnessYAMLCorpusCoversEveryCarrier(t *testing.T) {
	c := loadYAMLCorpus(t)
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".taad", "fitness.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`^\s*(?:- )?([a-z_]+):`)
	inFile := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if m := key.FindStringSubmatch(line); m != nil && m[1] != "rules" {
			inFile[m[1]] = true
		}
	}
	named := map[string]bool{}
	for _, k := range c.Carriers {
		named[k] = true
	}
	for k := range inFile {
		if !named[k] {
			t.Errorf("fitness.yaml uses the key %q, which the corpus does not name as a carrier: add rows for it", k)
		}
	}
	refused, accepted := map[string]bool{}, map[string]bool{}
	for _, r := range c.Rows {
		if !named[r.Carrier] {
			t.Errorf("%s: carrier %q is not in the carriers list", r.ID, r.Carrier)
		}
		if r.Expect == "refuse" {
			refused[r.Carrier] = true
		} else {
			accepted[r.Carrier] = true
		}
	}
	for k := range named {
		if !refused[k] || !accepted[k] {
			t.Errorf("carrier %q: refuse rows %v, accept rows %v: it needs both", k, refused[k], accepted[k])
		}
	}
}

// TestFitnessYAMLMisindentedKeyCensus is the census of the column class: a key of a
// rule written at a column other than the one its siblings use. The rows are not
// written by hand: every carrier key but name (which opens the rule) is planted at
// each column from 1 to 8 beside a sibling at 4, and the scan must refuse it at
// that line, while the same text with the key at 4 must pass. Columns below 4 end
// the rule's mapping for a YAML parser; columns above 4 after a scalar are read as
// more of that scalar, which then holds ": " or ends with ":". The line loader in
// rules_test.go reads only column 4 and drops either without a word.
func TestFitnessYAMLMisindentedKeyCensus(t *testing.T) {
	c := loadYAMLCorpus(t)
	value := map[string]string{
		"files":           "files: a/**",
		"because":         "because: Plain text, no colon-space.",
		"must_not_match":  "must_not_match:\n%s- x",
		"must_not_import": "must_not_import:\n%s- net/http",
		"except_files":    "except_files:\n%s- b/**",
	}
	planted := 0
	for _, k := range c.Carriers {
		if k == "name" {
			continue // the first key of a rule: the rows of its own cover a rule item at the wrong column
		}
		shape, ok := value[k]
		if !ok {
			t.Errorf("carrier %q has no planted shape in this census: add one", k)
			continue
		}
		sibling := "    files: s\n"
		if k == "files" {
			sibling = "    because: s\n"
		}
		for col := 1; col <= 8; col++ {
			pad := strings.Repeat(" ", col)
			key := pad + strings.ReplaceAll(shape, "%s", pad)
			src := "rules:\n  - name: r\n" + sibling + key + "\n"
			got := yamlScalarRefusals(src)
			if col == 4 {
				if len(got) != 0 {
					t.Errorf("%s at column 4, beside its siblings, is refused: %v\n%s", k, got, src)
				}
				continue
			}
			planted++
			if len(got) == 0 {
				t.Errorf("%s at column %d beside siblings at 4 is not refused:\n%s", k, col, src)
			} else if n := firstLine(got); n != 4 {
				t.Errorf("%s at column %d: first refused at line %d, want 4: %v", k, col, n, got)
			}
		}
	}
	if planted == 0 {
		t.Fatal("the census planted nothing")
	}
}
