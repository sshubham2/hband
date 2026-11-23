// Package fitnessrules holds no production code. Its test runs the
// architecture rules in .taad/fitness.yaml over a committed corpus of
// spellings (testdata/rule_spellings.json) and over the repository's own
// files, so a rule is pinned by a violating and a clean spelling and not by
// the author's reading of its regular expression.
//
// The engine that enforces fitness.yaml matches each rule's patterns against
// whole-file text with no MULTILINE flag: '^' and '$' anchor only at the
// start and end of the file and '\s' crosses newlines. Go's regexp behaves the
// same for every pattern the corpus covers. A rule whose pattern Go's regexp
// cannot compile (a lookahead) is skipped here and noted in the log.
package fitnessrules

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type rule struct {
	Name          string
	Files         string
	MustNotMatch  []string
	MustNotImport []string
	ExceptFiles   []string
}

var (
	ruleStart  = regexp.MustCompile(`^  - name: (.*)$`)
	ruleKey    = regexp.MustCompile(`^    ([a-z_]+):\s*(.*)$`)
	ruleItem   = regexp.MustCompile(`^    - (.*)$`)
	yamlFolded = regexp.MustCompile(`^      `)
)

// scalar decodes the YAML scalar forms fitness.yaml uses.
func scalar(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'':
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	case len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"':
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root not found at %s: %v", root, err)
	}
	return root
}

func loadRules(t *testing.T, root string) map[string]*rule {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".taad", "fitness.yaml"))
	if err != nil {
		t.Fatalf("read fitness.yaml: %v", err)
	}
	rules := map[string]*rule{}
	var cur *rule
	var list *[]string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if m := ruleStart.FindStringSubmatch(line); m != nil {
			cur = &rule{Name: scalar(m[1])}
			if _, dup := rules[cur.Name]; dup {
				t.Fatalf("fitness.yaml names the rule %q twice", cur.Name)
			}
			rules[cur.Name] = cur
			list = nil
			continue
		}
		if cur == nil {
			continue
		}
		if m := ruleKey.FindStringSubmatch(line); m != nil {
			list = nil
			switch m[1] {
			case "files":
				cur.Files = scalar(m[2])
			case "must_not_match":
				list = &cur.MustNotMatch
			case "must_not_import":
				list = &cur.MustNotImport
			case "except_files":
				list = &cur.ExceptFiles
			}
			continue
		}
		if m := ruleItem.FindStringSubmatch(line); m != nil && list != nil {
			*list = append(*list, scalar(m[1]))
			continue
		}
		if yamlFolded.MatchString(line) || strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
	}
	return rules
}

func globRegexp(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case g[i] == '*':
			b.WriteString("[^/]*")
		case g[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(g[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func (r *rule) applies(path string) bool {
	if !globRegexp(r.Files).MatchString(path) {
		return false
	}
	for _, e := range r.ExceptFiles {
		if globRegexp(e).MatchString(path) {
			return false
		}
	}
	return true
}

// fires reports whether the rule is violated by text at path. skipped lists
// patterns Go's regexp cannot compile.
func (r *rule) fires(path, text string) (fired bool, skipped []string) {
	if !r.applies(path) {
		return false, nil
	}
	for _, p := range r.MustNotMatch {
		re, err := regexp.Compile(p)
		if err != nil {
			skipped = append(skipped, p)
			continue
		}
		if re.MatchString(text) {
			fired = true
		}
	}
	if len(r.MustNotImport) > 0 && strings.HasPrefix(strings.TrimSpace(text), "package ") {
		f, err := parser.ParseFile(token.NewFileSet(), path, text, parser.ImportsOnly)
		if err == nil {
			for _, imp := range f.Imports {
				ip, _ := strconv.Unquote(imp.Path.Value)
				for _, banned := range r.MustNotImport {
					if ip == banned || (strings.HasSuffix(banned, "*") && strings.HasPrefix(ip, strings.TrimSuffix(banned, "*"))) {
						fired = true
					}
				}
			}
		}
	}
	return fired, skipped
}

type row struct {
	ID     string `json:"id"`
	Rule   string `json:"rule"`
	Path   string `json:"path"`
	Expect string `json:"expect"`
	Text   string `json:"text"`
	Why    string `json:"why"`
}

type corpus struct {
	BoundedBy map[string]string `json:"bounded_by"`
	Rules     []string          `json:"rules_under_test"`
	Retired   []string          `json:"retired_rules"`
	Rows      []row             `json:"rows"`
}

func loadCorpus(t *testing.T) corpus {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "rule_spellings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("corpus: %v", err)
	}
	return c
}

func TestRuleSpellingsCorpus(t *testing.T) {
	root := repoRoot(t)
	rules := loadRules(t, root)
	c := loadCorpus(t)

	for _, name := range c.Retired {
		if _, ok := rules[name]; ok {
			t.Errorf("fitness.yaml still holds the retired rule %q", name)
		}
	}

	fire, quiet := map[string]int{}, map[string]int{}
	ids := map[string]bool{}
	for _, rw := range c.Rows {
		if ids[rw.ID] {
			t.Fatalf("corpus row id %q is used twice", rw.ID)
		}
		ids[rw.ID] = true
		r, ok := rules[rw.Rule]
		if !ok {
			t.Errorf("%s: fitness.yaml has no rule named %q", rw.ID, rw.Rule)
			continue
		}
		path := rw.Path
		if path == "" {
			path = "internal/server/x.go"
		}
		got, _ := r.fires(path, rw.Text)
		switch rw.Expect {
		case "fire":
			fire[rw.Rule]++
			if !got {
				t.Errorf("%s [%s] must fire but stays quiet: %q (%s)", rw.ID, rw.Rule, rw.Text, rw.Why)
			}
		case "quiet":
			quiet[rw.Rule]++
			if got {
				t.Errorf("%s [%s] must stay quiet but fires: %q (%s)", rw.ID, rw.Rule, rw.Text, rw.Why)
			}
		default:
			t.Fatalf("%s: expect must be fire or quiet, got %q", rw.ID, rw.Expect)
		}
	}
	for _, name := range c.Rules {
		if fire[name] == 0 || quiet[name] == 0 {
			t.Errorf("rule %q has %d fire and %d quiet rows: a rule needs both to be pinned", name, fire[name], quiet[name])
		}
	}
	if len(c.Rows) < 100 {
		t.Errorf("the corpus holds %d rows; it has been trimmed", len(c.Rows))
	}
}

// TestRulesHoldOnTheTree runs every rule Go can evaluate over the
// repository's own files.
func TestRulesHoldOnTheTree(t *testing.T) {
	root := repoRoot(t)
	rules := loadRules(t, root)
	if len(rules) < 10 {
		t.Fatalf("only %d rules loaded from fitness.yaml", len(rules))
	}
	checked := 0
	skipped := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".taad", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		for _, r := range rules {
			if !r.applies(rel) {
				continue
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				t.Errorf("read %s: %v", rel, rerr)
				continue
			}
			checked++
			fired, skip := r.fires(rel, string(data))
			for _, s := range skip {
				skipped[r.Name+": "+s] = true
			}
			if fired {
				t.Errorf("%s violates the rule %q", rel, r.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no file was checked against any rule: the walk is vacuous")
	}
	for s := range skipped {
		t.Logf("skipped (not valid in Go's regexp, enforced by the engine): %s", s)
	}
}
