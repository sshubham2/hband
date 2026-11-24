package fitnessrules

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The scanner behind TestFitnessYAMLScalarCorpus and TestFitnessYAMLFileHasNoRefusedScalars.
// TestFitnessYAMLPlainScalarsParse, in yaml_scalar_test.go, is the review's test and
// stays as the review wrote it; this is the same check widened to the other ways text
// written into fitness.yaml fails a YAML parser, and tested as a table.
var (
	// yamlKeyLine is a mapping key at the start of a line, with the value after it.
	yamlKeyLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):(?:\s+(.*))?$`)
	// yamlListKeys are the keys of fitness.yaml whose value is a list of scalars.
	yamlListKeys = map[string]bool{"must_not_match": true, "must_not_import": true, "except_files": true}
)

// yamlScalarRefusals scans text laid out like fitness.yaml (block mappings and
// lists whose values are scalars) and returns one finding per scalar a YAML parser
// would refuse or read as something else, each as "N: what" with N the 1-based line.
// It is not a YAML parser. It knows the shapes the file is written in, and refuses a
// shape it can see is wrong; what it accepts a parser may still dislike, which is why
// the corpus lists the spellings it is known to refuse and to accept.
//
// The class is bounded by who writes the text. A prose author (name, because) writes
// ": ", a trailing ":", " #" and apostrophes; a pattern or glob author (files, the
// items of must_not_match, must_not_import and except_files) starts a value with a
// character YAML gives a meaning (* & ! [ { @ and the like), or writes an item that
// reads as a mapping. A quoted scalar fails differently: an apostrophe that is not
// doubled, or a backslash that is not an escape.
func yamlScalarRefusals(src string) []string {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []string
	add := func(n int, format string, args ...any) {
		out = append(out, fmt.Sprintf("%d: ", n)+fmt.Sprintf(format, args...))
	}
	indentOf := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	blank := func(s string) bool { return strings.TrimSpace(s) == "" }
	comment := func(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "#") }

	// parent is the nearest key above with no value of its own, which a list belongs to.
	parent, parentCol := "", -1
	for i := 0; i < len(lines); {
		line := lines[i]
		if blank(line) || comment(line) {
			i++
			continue
		}
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if strings.Contains(lead, "\t") {
			add(i+1, "a tab in the indentation, which YAML refuses")
			i++
			continue
		}
		indent := len(lead)
		rest := line[indent:]
		dash := strings.HasPrefix(rest, "- ")
		if dash {
			rest = strings.TrimLeft(rest[2:], " ")
		}
		col := indent // continuation lines of the scalar are indented deeper than col
		var value string
		start := 0 // where the value begins in the line
		m := yamlKeyLine.FindStringSubmatch(strings.TrimRight(rest, " "))
		switch {
		case m != nil && dash && parentCol >= 0 && parentCol <= indent && yamlListKeys[parent]:
			// "- word: text" under a list of scalars is a mapping, not the pattern written.
			add(i+1, "a list item of %s that reads as a mapping key: %q", parent, rest)
			i++
			continue
		case m != nil:
			col = len(line) - len(rest)
			value = m[2]
			start = col + len(m[1]) + 1
			for start < len(line) && line[start] == ' ' {
				start++
			}
			if value == "" {
				parent, parentCol = m[1], col
				if dash {
					parentCol = indent
				}
				i++
				continue
			}
		case dash:
			value = rest
			start = len(line) - len(rest)
		default:
			i++
			continue
		}

		switch value[0] {
		case '|', '>':
			// A block scalar: everything indented deeper is text, whatever it says.
			j := i + 1
			for j < len(lines) && (blank(lines[j]) || indentOf(lines[j]) > col) {
				j++
			}
			i = j
		case '\'', '"':
			end, problem := scanQuoted(lines, i, start, value[0])
			if problem != "" {
				add(end+1, "%s", problem)
				i = end + 1
				break
			}
			if j := end + 1; j < len(lines) && !blank(lines[j]) && !comment(lines[j]) && indentOf(lines[j]) > col {
				add(j+1, "text after the closing quote of the scalar above, which YAML cannot attach to it: %q", strings.TrimSpace(lines[j]))
			}
			i = end + 1
		default:
			if why := plainStart(value); why != "" {
				add(i+1, "a plain scalar a YAML parser refuses, %s: %q", why, value)
			}
			pieces := []struct {
				n int
				v string
			}{{i + 1, strings.TrimSpace(value)}}
			j := i + 1
			for j < len(lines) && !blank(lines[j]) && !comment(lines[j]) && indentOf(lines[j]) > col {
				pieces = append(pieces, struct {
					n int
					v string
				}{j + 1, strings.TrimSpace(lines[j])})
				j++
			}
			for _, p := range pieces {
				switch {
				case strings.Contains(p.v, ": "):
					add(p.n, "a plain scalar a YAML parser refuses, it holds \": \": %q", p.v)
				case strings.HasSuffix(p.v, ":"):
					add(p.n, "a plain scalar a YAML parser refuses, it ends with \":\": %q", p.v)
				case strings.Contains(p.v, " #"):
					add(p.n, "a plain scalar a YAML parser reads as cut short, it holds \" #\": %q", p.v)
				}
			}
			i = j
		}
	}
	return out
}

// plainStart says why a plain scalar may not begin with its first characters, or "".
func plainStart(v string) string {
	switch {
	case strings.ContainsRune("*&!%@`,]}#", rune(v[0])):
		return fmt.Sprintf("it begins with %q, which YAML gives a meaning (alias, anchor, tag, directive, reserved or comment)", v[0])
	case v[0] == '[' || v[0] == '{':
		return "it begins a flow collection, which this file never holds and the loader cannot read"
	case strings.HasPrefix(v, "- ") || strings.HasPrefix(v, "? ") || strings.HasPrefix(v, ": "):
		return "it begins with a block indicator"
	}
	return ""
}

// scanQuoted reads a quoted scalar that opens at lines[at][pos], over as many lines
// as it takes, and returns the line it closes on. problem is "" when it closes
// properly: nothing but a comment after the closing quote, and in a double-quoted
// scalar only escapes YAML knows.
func scanQuoted(lines []string, at, pos int, quote byte) (end int, problem string) {
	n, p := at, pos+1
	for {
		l := lines[n]
		for p < len(l) {
			c := l[p]
			switch {
			case quote == '\'' && c == '\'':
				if p+1 < len(l) && l[p+1] == '\'' {
					p += 2
					continue
				}
				return closedQuote(lines, n, p)
			case quote == '"' && c == '"':
				return closedQuote(lines, n, p)
			case quote == '"' && c == '\\':
				if p+1 >= len(l) { // an escaped line break folds the line
					p++
					continue
				}
				e := l[p+1]
				digits := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				switch {
				case digits > 0:
					if p+2+digits > len(l) || strings.Trim(l[p+2:p+2+digits], "0123456789abcdefABCDEF") != "" {
						return n, fmt.Sprintf("a double-quoted scalar with a short or non-hex \\%c escape: %q", e, strings.TrimSpace(l))
					}
					p += 2 + digits
					continue
				case !strings.ContainsRune("0abtnvfre \t\"/\\N_LP", rune(e)):
					return n, fmt.Sprintf("a double-quoted scalar with \\%c, which is not a YAML escape: %q", e, strings.TrimSpace(l))
				}
				p += 2
				continue
			}
			p++
		}
		n++
		if n >= len(lines) {
			return at, fmt.Sprintf("a quoted scalar that is never closed: %q", strings.TrimSpace(lines[at]))
		}
		p = 0
	}
}

// closedQuote checks what follows the closing quote at lines[n][p].
func closedQuote(lines []string, n, p int) (int, string) {
	rest := lines[n][p+1:]
	t := strings.TrimSpace(rest)
	if t != "" && !(strings.HasPrefix(rest, " ") && strings.HasPrefix(t, "#")) {
		return n, fmt.Sprintf("text after the closing quote (an apostrophe that is not doubled, or a quote that is not escaped): %q", strings.TrimSpace(lines[n]))
	}
	return n, ""
}

// TestFitnessYAMLFileHasNoRefusedScalars runs the widened scan over the real file:
// quoted scalars, list items and start characters as well as the plain scalars the
// review's test reads.
func TestFitnessYAMLFileHasNoRefusedScalars(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".taad", "fitness.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range yamlScalarRefusals(string(data)) {
		t.Errorf("fitness.yaml:%s", f)
	}
}
