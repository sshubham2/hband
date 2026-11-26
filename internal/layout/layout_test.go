package layout

import (
	"io/fs"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// outsideDefines is the text of a template file that is not inside a {{define}}
// block, with the actions that sit there. A page is a file with any: the pieces a
// file only defines (a header, a comment thread) are partials.
func outsideDefines(src string) string {
	var out strings.Builder
	depth, defineDepth := 0, 0 // defineDepth is the depth a {{define}} opened at, or 0
	for i := 0; i < len(src); {
		open := strings.Index(src[i:], "{{")
		if open < 0 {
			if defineDepth == 0 {
				out.WriteString(src[i:])
			}
			break
		}
		if defineDepth == 0 {
			out.WriteString(src[i : i+open])
		}
		end := strings.Index(src[i+open:], "}}")
		if end < 0 {
			break
		}
		action := src[i+open : i+open+end+2]
		i += open + end + 2
		word := strings.Fields(strings.Trim(action, "{}- \t\r\n"))
		kw := ""
		if len(word) > 0 {
			kw = word[0]
		}
		switch kw {
		case "define", "block":
			depth++
			if defineDepth == 0 {
				defineDepth = depth
				continue
			}
		case "if", "range", "with":
			depth++
		case "end":
			if defineDepth == depth {
				depth--
				defineDepth = 0
				continue
			}
			depth--
		}
		if defineDepth == 0 {
			out.WriteString(action)
		}
	}
	return out.String()
}

const (
	pageStart = `{{template "page-start" .}}`
	pageEnd   = `{{template "page-end" .}}`
	// threadTag is the one script tag the site's pages may carry, behind the gate.
	threadTag = `{{if .ThreadScript}}<script src="/static/thread.js" defer></script>`
)

var scriptRE = regexp.MustCompile(`(?i)<script`)

// shellProblems reads every html file under a templates folder, at any depth, of
// fsys (the internal folder) and says what breaks the one-shell rule:
//   - a page (a file with text outside its defines) must open with page-start and
//     hold page-end exactly once;
//   - base.html holds one stylesheet link, no admin marker and exactly one script
//     tag, the thread script behind the ThreadScript gate;
//   - no other template holds a script tag, in any letter case.
func shellProblems(fsys fs.FS) []string {
	var problems []string
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		inTemplates := false
		for _, el := range strings.Split(path.Dir(p), "/") {
			if el == "templates" {
				inTemplates = true
			}
		}
		if !inTemplates {
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		src := string(raw)
		if path.Base(p) == "base.html" && strings.HasSuffix(path.Dir(p), "layout/templates") || p == "layout/templates/base.html" {
			if n := strings.Count(src, `<link rel="stylesheet" href="/static/site.css">`); n != 1 {
				problems = append(problems, p+": base.html has "+strconv.Itoa(n)+" site.css links, want exactly 1")
			}
			if strings.Contains(src, "data-hband-admin") {
				problems = append(problems, p+": base.html carries the admin marker, which belongs to the admin pages only")
			}
			if n := len(scriptRE.FindAllString(src, -1)); n != 1 {
				problems = append(problems, p+": base.html has "+strconv.Itoa(n)+" script tags, want exactly 1")
			}
			if n := strings.Count(src, threadTag); n != 1 {
				problems = append(problems, p+": base.html has "+strconv.Itoa(n)+" thread script tags behind the ThreadScript gate, want exactly 1")
			}
			return nil
		}
		if scriptRE.MatchString(src) {
			problems = append(problems, p+": a template other than base.html holds a script tag")
		}
		body := strings.TrimSpace(outsideDefines(src))
		if body == "" {
			return nil
		}
		if !strings.HasPrefix(body, pageStart) {
			problems = append(problems, p+": a page must open with "+pageStart)
		}
		if n := strings.Count(body, pageEnd); n != 1 {
			problems = append(problems, p+": a page must hold "+pageEnd+" exactly once, has "+strconv.Itoa(n))
		}
		return nil
	})
	return problems
}

// TestEveryPageTakesTheShell is the positive half of "no page carries its own
// head": every page template of every package opens with the shell and closes
// with it. The fitness rule refuses a per-page head, style block or style
// attribute; this refuses a page that skips the shell altogether, and a second
// script tag anywhere.
func TestEveryPageTakesTheShell(t *testing.T) {
	root := os.DirFS("..") // the internal folder
	if problems := shellProblems(root); len(problems) > 0 {
		t.Fatalf("the one-shell rule is broken:\n%s", strings.Join(problems, "\n"))
	}
	// The test must have read the pages: a walk that found nothing passes everything.
	pages := 0
	fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".html") && strings.Contains(p, "/templates/") {
			raw, _ := fs.ReadFile(root, p)
			if strings.TrimSpace(outsideDefines(string(raw))) != "" {
				pages++
			}
		}
		return nil
	})
	if pages < 7 {
		t.Fatalf("read %d page templates under internal/, want at least 7 (the three reader pages, the three admin pages and the forbidden page): the walk reads nothing", pages)
	}
}

// The checker has to be able to fail: each plant is a tree with one fault, and
// the quiet row is a tree with none.
func TestShellCheckerSeesEachFault(t *testing.T) {
	good := `{{template "page-start" .}}<main></main>
{{template "page-end" .}}
{{define "x"}}<p>{{if .A}}a{{end}}</p>{{end}}
`
	base := `{{define "head"}}<link rel="stylesheet" href="/static/site.css">{{end}}
{{define "page-start"}}{{if .ThreadScript}}<script src="/static/thread.js" defer></script>
{{end}}{{end}}
`
	cases := []struct {
		name  string
		files map[string]string
		want  string // "" is quiet; otherwise a substring of a problem
	}{
		{"quiet", map[string]string{"server/templates/a.html": good, "layout/templates/base.html": base}, ""},
		{"a partial is not a page", map[string]string{"server/templates/p.html": `{{define "header"}}<header></header>{{end}}`, "layout/templates/base.html": base}, ""},
		{"no page-start", map[string]string{"server/templates/a.html": "<main></main>\n" + pageEnd, "layout/templates/base.html": base}, "must open with"},
		{"text before page-start", map[string]string{"server/templates/a.html": "<!doctype html>\n" + good, "layout/templates/base.html": base}, "must open with"},
		{"no page-end", map[string]string{"server/templates/a.html": pageStart + "<main></main>\n", "layout/templates/base.html": base}, "exactly once, has 0"},
		{"two page-ends", map[string]string{"server/templates/a.html": good + pageEnd, "layout/templates/base.html": base}, "exactly once, has 2"},
		{"a nested package's templates folder is walked", map[string]string{"x/y/templates/deep/a.html": "<main></main>", "layout/templates/base.html": base}, "must open with"},
		{"a script tag in another template", map[string]string{"server/templates/a.html": good + `<script src="/x.js"></script>`, "layout/templates/base.html": base}, "holds a script tag"},
		{"a script tag in capitals", map[string]string{"server/templates/a.html": good + `<SCRIPT src="/x.js"></SCRIPT>`, "layout/templates/base.html": base}, "holds a script tag"},
		{"a script tag in mixed case", map[string]string{"server/templates/a.html": good + `<Script src="/x.js"></Script>`, "layout/templates/base.html": base}, "holds a script tag"},
		{"a second script tag in base.html", map[string]string{"server/templates/a.html": good, "layout/templates/base.html": base + `<SCRIPT src="/y.js"></SCRIPT>`}, "base.html has 2 script tags"},
		{"an ungated thread script in base.html", map[string]string{"server/templates/a.html": good, "layout/templates/base.html": strings.Replace(base, "{{if .ThreadScript}}", "", 1)}, "behind the ThreadScript gate"},
		{"no script in base.html", map[string]string{"server/templates/a.html": good, "layout/templates/base.html": strings.Replace(base, `<script src="/static/thread.js" defer></script>`, "", 1)}, "base.html has 0 script tags"},
		{"the admin marker in base.html", map[string]string{"server/templates/a.html": good, "layout/templates/base.html": base + "<main data-hband-admin>"}, "admin marker"},
		{"two stylesheet links in base.html", map[string]string{"server/templates/a.html": good, "layout/templates/base.html": base + `<link rel="stylesheet" href="/static/site.css">`}, "2 site.css links"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := fstest.MapFS{}
			for p, s := range c.files {
				m[p] = &fstest.MapFile{Data: []byte(s)}
			}
			problems := strings.Join(shellProblems(m), "\n")
			switch {
			case c.want == "" && problems != "":
				t.Fatalf("a clean tree was refused:\n%s", problems)
			case c.want != "" && !strings.Contains(problems, c.want):
				t.Fatalf("want a problem naming %q, got:\n%s", c.want, problems)
			}
		})
	}
}
