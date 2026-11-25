// Package ported holds the record that lets the Python acceptance checks be
// deleted: for every assertion site of every retired source, the Go check that
// now makes it, or the reason none is needed.
//
// A source is a retired file: a finish-line body kept in the engine's ledger, or a
// Python file in the repository. Each has an Edition, the sha256 of its canonical
// text (no byte-order mark, LF line ends, exactly one trailing newline). The
// Edition of a ledger body is taken from the body as SELECTed, once, from the
// ledger; of a repository file, from the file. A Python source's text is kept in
// testdata/*.txt (not .py, so that no Python is left in the tree), and its
// assertion sites are found in that text by Sites, not listed by hand: a site that
// has no row is a problem, so a row count the builder chose cannot hide one. The ten
// Go finish bodies are committed verbatim under tests/acceptance, and one Verbatim
// row each says so; their Edition is checked against the committed file.
//
// Check reads only the repository's files. It never reads the ledger or the
// environment, and needs none of the retired files to exist. The package is test-only:
// go test -count=1 ./tests/acceptance/ported runs the check (TestPortedAssertionsHaveAGoTest).
package ported

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Kind is what became of an assertion site.
type Kind string

const (
	// Ported: a Go check makes it. By names the function, a ported:<ID> comment sits
	// in it on or just above the statement that checks, and a By that is not a test
	// has a Via naming a test that runs it.
	Ported Kind = "Ported"
	// Verbatim: a Go finish body, committed byte for byte (its sha256 is its Edition).
	Verbatim Kind = "Verbatim"
	// Helper: a check of the harness's own machinery (browser refusal, DevTools
	// driver, the stand-in GitHub), made by a test of the helper that By names.
	Helper Kind = "Helper"
	// Amended: made differently on purpose; Why says how and why. By, if given,
	// is held to the Ported rule.
	Amended Kind = "Amended"
	// Superseded: not made any more; Why says what replaced it. No By.
	Superseded Kind = "Superseded"
)

// Source is one retired file.
type Source struct {
	Name     string // "ledger:<epic id>/<file>", or the file's repository path
	Edition  string // sha256 of Canonical(text), in hex
	Snapshot string // Python: the text, under testdata (relative to this package)
	File     string // Go finish body: the committed test file, repository-relative
	FakeSide [2]int // Python: the lines of the stand-in GitHub class, {0, 0} for none
}

// Row says where one assertion site went.
type Row struct {
	ID     string
	Source string
	At     int // the line of the site in the source's canonical text; 0 for a whole Go body
	Says   string
	Kind   Kind
	By     string // "<repo-relative dir>.<Func>"
	Via    string // a test that runs By when By is not itself a test
	Why    string
}

// Canonical is the one form every Edition hashes: UTF-8 without a byte-order mark,
// LF line ends, exactly one trailing newline.
func Canonical(b []byte) string {
	s := strings.TrimPrefix(string(b), "\uFEFF")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, "\n") + "\n"
}

// EditionOf is the Edition of a text.
func EditionOf(b []byte) string {
	sum := sha256.Sum256([]byte(Canonical(b)))
	return hex.EncodeToString(sum[:])
}

// stripPython blanks the comments and the insides of the string literals of a
// Python text, keeping every newline and every position, so that a pattern can
// look for code only.
func stripPython(src string) string {
	rs := []rune(src)
	out := make([]rune, len(rs))
	copy(out, rs)
	blank := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	for i := 0; i < len(rs); {
		switch c := rs[i]; {
		case c == '#':
			for i < len(rs) && rs[i] != '\n' {
				blank(i)
				i++
			}
		case c == '"' || c == '\'':
			triple := i+2 < len(rs) && rs[i+1] == c && rs[i+2] == c
			start := i
			if triple {
				i += 3
			} else {
				i++
			}
			for i < len(rs) {
				if rs[i] == '\\' {
					i += 2
					continue
				}
				if triple && rs[i] == c && i+2 < len(rs) && rs[i+1] == c && rs[i+2] == c {
					i += 3
					break
				}
				if !triple && rs[i] == c {
					i++
					break
				}
				if !triple && rs[i] == '\n' {
					break
				}
				i++
			}
			if i > len(rs) {
				i = len(rs)
			}
			for k := start; k < i; k++ {
				blank(k)
			}
		default:
			i++
		}
	}
	return string(out)
}

var (
	sitePatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bassert\b`),
		regexp.MustCompile(`self\.(?:assert\w*|fail)\(`),
		regexp.MustCompile(`\braise\b`),
		regexp.MustCompile(`\b(?:sys|os)\._?exit\(`),
		regexp.MustCompile(`(?:^|[^.\w])(?:exit|quit)\(`),
		// A non-zero code of any number of digits, negative or in parentheses:
		// return 1, return 124, ..., return (2), return -1. testdata/site_spellings.json
		// holds every spelling these patterns are held to.
		regexp.MustCompile(`\breturn(?:\s+|\s*\()\s*(?:-\s*)?[1-9]\d*\b`),
		regexp.MustCompile(`\breturn\b.*\belse\s*\(?\s*(?:-\s*)?[1-9]\d*\b`),
	}
	defRE = regexp.MustCompile(`^(\s*)(?:async\s+)?def\s+(\w+)\s*\(`)
)

// Sites lists the lines of a Python text where something is asserted, or the
// program is made to fail or to exit: a line that matches one of the patterns
// (assert, self.assert*/self.fail, raise, sys/os exit, the exit and quit builtins,
// return of a non-zero code),
// after comments and strings are blanked; and, for every function whose body holds
// such a line, each line that calls it, by name, wherever it is.
func Sites(src string) []int {
	lines := strings.Split(stripPython(strings.ReplaceAll(src, "\r\n", "\n")), "\n")
	hit := map[int]bool{}
	for i, l := range lines {
		for _, p := range sitePatterns {
			if p.MatchString(l) {
				hit[i+1] = true
				break
			}
		}
	}
	type def struct {
		line, end int
		name      string
	}
	var defs []def
	for i, l := range lines {
		m := defRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		indent := len(m[1])
		end := len(lines)
		for k := i + 1; k < len(lines); k++ {
			t := strings.TrimLeft(lines[k], " \t")
			if t == "" || t[0] == ')' || t[0] == ']' {
				continue
			}
			if len(lines[k])-len(t) <= indent {
				end = k
				break
			}
		}
		defs = append(defs, def{i + 1, end, m[2]})
	}
	named := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, d := range defs {
			if named[d.name] {
				continue
			}
			for k := d.line; k <= d.end; k++ {
				if hit[k] {
					named[d.name] = true
					changed = true
					break
				}
			}
		}
		for name := range named {
			call := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\(`)
			for i, l := range lines {
				if hit[i+1] || !call.MatchString(l) {
					continue
				}
				if m := defRE.FindStringSubmatch(l); m != nil && m[2] == name {
					continue
				}
				hit[i+1] = true
				changed = true
			}
		}
	}
	out := make([]int, 0, len(hit))
	for l := range hit {
		out = append(out, l)
	}
	sort.Ints(out)
	return out
}

type funcSpan struct {
	file       string
	start, end int
}

type tag struct {
	id   string
	file string
	line int
	fn   string // "<dir>.<Func>" it sits in, "" when in none
}

var tagRE = regexp.MustCompile(`ported:([A-Za-z0-9_,-]+)`)

// stmtRE is a statement that checks: a t.Fatal/Error call, an Expect/check/must
// helper, a press/look closure, a call of a harness helper (program.X(...), or an
// exported helper of the harness's own package called with t), a Visitor's request,
// or a DevTools tab's verb.
var stmtRE = regexp.MustCompile(`\b(?:t\.(?:Fatalf?|Errorf?|Fail|FailNow)|program\.[A-Z]\w*|headless\.Open|[A-Za-z_.]*(?:[Ee]xpect|[Cc]heck|[Mm]ust|[Pp]ress|[Ll]ook)\w*)\(|\b[A-Z]\w*\(t\b|\.(?:SignIn|Owner|Publish|Approve|Anonymous|Get|GetFollow|Post|PostFollow|Upload|Do|Call|Navigate|Eval|Viewport)\(`)

// opensBlock is a line that only opens a block: the statement that checks is its
// first line.
var opensBlock = regexp.MustCompile(`^\s*(?:if\b|for\b|switch\b|select\b|case\b|default:|\}\s*else\b).*[{:]\s*$`)

// index is what Check learns from the Go files of the repository.
type index struct {
	funcs map[string]funcSpan // "<dir>.<Func>"
	tags  []tag
	lines map[string][]string
}

func scanGo(root string, dirs []string) (*index, []string) {
	ix := &index{funcs: map[string]funcSpan{}, lines: map[string][]string{}}
	var problems []string
	for _, dir := range dirs {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		var files []string
		filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") {
				files = append(files, p)
			}
			return nil
		})
		for _, f := range files {
			rel, _ := filepath.Rel(root, filepath.Dir(f))
			rel = filepath.ToSlash(rel)
			data, err := os.ReadFile(f)
			if err != nil {
				problems = append(problems, fmt.Sprintf("cannot read %s: %v", f, err))
				continue
			}
			ix.lines[f] = strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, data, parser.ParseComments)
			if err != nil {
				problems = append(problems, fmt.Sprintf("cannot parse %s: %v", f, err))
				continue
			}
			var spans []funcSpan
			var names []string
			for _, d := range af.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					s := funcSpan{f, fset.Position(fd.Pos()).Line, fset.Position(fd.End()).Line}
					ix.funcs[rel+"."+fd.Name.Name] = s
					spans = append(spans, s)
					names = append(names, rel+"."+fd.Name.Name)
				}
			}
			for _, cg := range af.Comments {
				for _, c := range cg.List {
					m := tagRE.FindStringSubmatch(c.Text)
					if m == nil {
						continue
					}
					line := fset.Position(c.Pos()).Line
					fn := ""
					for i, s := range spans {
						if line >= s.start && line <= s.end {
							fn = names[i]
						}
					}
					for _, id := range strings.Split(m[1], ",") {
						if id != "" {
							ix.tags = append(ix.tags, tag{id, f, line, fn})
						}
					}
				}
			}
		}
	}
	return ix, problems
}

// checks reports whether a ported tag sits on, or on the line just above, a
// statement that checks; when that line only opens a block (an if, a for, a case),
// the statement is the block's first line.
func (ix *index) checks(t tag) bool {
	ls := ix.lines[t.file]
	at := func(l int) string {
		if l < 1 || l > len(ls) {
			return ""
		}
		return ls[l-1]
	}
	if stmtRE.MatchString(strings.SplitN(at(t.line), "//", 2)[0]) {
		return true
	}
	next := t.line + 1
	for strings.HasPrefix(strings.TrimSpace(at(next)), "//") {
		next++
	}
	if stmtRE.MatchString(at(next)) {
		return true
	}
	return opensBlock.MatchString(at(next)) && stmtRE.MatchString(at(next+1))
}

func isTest(fn string) bool {
	return strings.HasPrefix(fn[strings.LastIndex(fn, ".")+1:], "Test")
}

// Check reports every way the rows fail to account for the sources. root is the
// repository root; testdata is the folder the Snapshot paths are relative to.
func Check(root, testdata string, sources []Source, rows []Row) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	ix, bad := scanGo(root, []string{"tests/acceptance", "internal/testutil"})
	problems = append(problems, bad...)

	src := map[string]Source{}
	for _, s := range sources {
		if _, dup := src[s.Name]; dup {
			add("source %s is declared twice", s.Name)
		}
		src[s.Name] = s
	}
	ids := map[string]Row{}
	at := map[string]bool{}
	byName := map[string][]Row{}
	for _, r := range rows {
		if _, dup := ids[r.ID]; dup {
			add("row id %s is used twice", r.ID)
		}
		ids[r.ID] = r
		key := fmt.Sprintf("%s@%d", r.Source, r.At)
		if at[key] {
			add("row %s: %s line %d has two rows", r.ID, r.Source, r.At)
		}
		at[key] = true
		if _, ok := src[r.Source]; !ok {
			add("row %s names the undeclared source %s", r.ID, r.Source)
		}
		byName[r.Source] = append(byName[r.Source], r)
	}

	// Editions, and the sites of each Python source.
	for _, s := range sources {
		path := s.File
		if s.Snapshot != "" {
			path = filepath.ToSlash(filepath.Join(testdata, s.Snapshot))
		}
		var data []byte
		var err error
		if s.Snapshot != "" {
			data, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		} else {
			data, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(s.File)))
		}
		if err != nil {
			add("source %s: its text is missing: %v", s.Name, err)
			continue
		}
		if got := EditionOf(data); got != s.Edition {
			add("source %s: %s has the sha256 %s, the Edition says %s", s.Name, path, got, s.Edition)
		}
		if s.Snapshot == "" {
			verbatim := false
			for _, r := range byName[s.Name] {
				verbatim = verbatim || r.Kind == Verbatim
			}
			if !verbatim {
				add("source %s is a Go body and has no Verbatim row", s.Name)
			}
			continue
		}
		nlines := strings.Count(Canonical(data), "\n")
		for _, line := range Sites(Canonical(data)) {
			if !at[fmt.Sprintf("%s@%d", s.Name, line)] {
				add("source %s line %d is an assertion site and has no row", s.Name, line)
			}
		}
		for _, r := range byName[s.Name] {
			if r.At < 1 || r.At > nlines {
				add("row %s: line %d is not in %s (%d lines)", r.ID, r.At, s.Name, nlines)
			}
		}
	}

	// Each row's kind holds what it promises.
	tagged := map[string]map[string]bool{} // id -> functions holding its tag
	for _, t := range ix.tags {
		if tagged[t.id] == nil {
			tagged[t.id] = map[string]bool{}
		}
		tagged[t.id][t.fn] = true
		if _, ok := ids[t.id]; !ok {
			add("the tag ported:%s (%s:%d) has no row", t.id, filepath.ToSlash(relTo(root, t.file)), t.line)
		}
		if !ix.checks(t) {
			add("the tag ported:%s (%s:%d) is not on or just above a statement that checks", t.id, filepath.ToSlash(relTo(root, t.file)), t.line)
		}
	}
	for _, r := range rows {
		s := src[r.Source]
		inFake := s.FakeSide[0] > 0 && r.At >= s.FakeSide[0] && r.At <= s.FakeSide[1]
		if inFake && !(r.Kind == Helper && strings.HasPrefix(r.By, "internal/testutil/fakegithub.") && strings.HasPrefix(r.Via, "tests/acceptance/")) {
			add("row %s is a stand-in GitHub check and must be a Helper row in internal/testutil/fakegithub with a Via naming an acceptance test", r.ID)
		}
		switch r.Kind {
		case Ported, Amended:
			if r.Kind == Amended && r.Why == "" {
				add("row %s is Amended and has no Why", r.ID)
			}
			if r.Kind == Amended && r.By == "" {
				continue
			}
			f, ok := ix.funcs[r.By]
			if !ok {
				add("row %s: %s is no function in the repository", r.ID, r.By)
				continue
			}
			_ = f
			if !tagged[r.ID][r.By] {
				add("row %s: %s holds no ported:%s tag", r.ID, r.By, r.ID)
			}
			if !isTest(r.By) {
				if _, ok := ix.funcs[r.Via]; !ok || !isTest(r.Via) || !strings.HasPrefix(r.Via, "tests/acceptance/") {
					add("row %s: %s is not a test, and its Via %q is not a test under tests/acceptance", r.ID, r.By, r.Via)
				}
			}
		case Verbatim:
			if _, ok := ix.funcs[r.By]; !ok || !isTest(r.By) {
				add("row %s: Verbatim By %q is not a test function", r.ID, r.By)
			}
			if s.Snapshot != "" || s.File == "" {
				add("row %s: only a Go body can be Verbatim", r.ID)
			}
		case Helper:
			if r.Why == "" {
				add("row %s is a Helper row and has no Why", r.ID)
			}
			ok := false
			for _, d := range []string{"internal/testutil/headless.", "internal/testutil/program.", "internal/testutil/fakegithub."} {
				if strings.HasPrefix(r.By, d) {
					_, found := ix.funcs[r.By]
					ok = found && isTest(r.By)
				}
			}
			if !ok {
				add("row %s: Helper By %q is not an existing test in internal/testutil/headless, program or fakegithub", r.ID, r.By)
			}
		case Superseded:
			if r.Why == "" {
				add("row %s is Superseded and has no Why", r.ID)
			}
			if r.By != "" {
				add("row %s is Superseded and names a By", r.ID)
			}
		default:
			add("row %s has the unknown kind %q", r.ID, r.Kind)
		}
	}
	sort.Strings(problems)
	return problems
}

func relTo(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}
