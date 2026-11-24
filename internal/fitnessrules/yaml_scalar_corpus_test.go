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

// TestFitnessYAMLMisindentedKeyCensus plants each carrier key but name (which opens a
// rule) at columns 1 to 8 beside a sibling at 4: the scan must refuse all but column 4.
func TestFitnessYAMLMisindentedKeyCensus(t *testing.T) {
	shape := map[string]string{"files": "files: a", "because": "because: b", "must_not_match": "must_not_match:\n%s- x",
		"must_not_import": "must_not_import:\n%s- x", "except_files": "except_files:\n%s- x"}
	for _, k := range loadYAMLCorpus(t).Carriers {
		if k == "name" {
			continue
		} else if shape[k] == "" {
			t.Errorf("carrier %q has no planted shape: add one", k)
		}
		for col := 1; col <= 8; col++ {
			pad := strings.Repeat(" ", col)
			src := "rules:\n  - name: r\n    files: s\n" + pad + strings.ReplaceAll(shape[k], "%s", pad) + "\n"
			got := yamlScalarRefusals(src)
			if (col == 4) != (len(got) == 0) || (col != 4 && firstLine(got) != 4) {
				t.Errorf("%s at column %d: refused %v\n%s", k, col, got, src)
			}
		}
	}
}
