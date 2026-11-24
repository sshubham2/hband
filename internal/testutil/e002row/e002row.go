// Package e002row reads a show-all page the way the finish line's row reader
// does (the E-002 acceptance bodies, which live in the ledger and not in this
// tree): the elements as a forgiving tree, the row of each /reports/<slug> link,
// the likes, visible comments and times opened a row shows, and the publish date.
//
// It is a port of that reader's own functions (dom, listed, rowText, counts,
// dated), kept apart from any handler so a test can feed it a rendered row and
// learn whether the row can be "read apart" before the finish line ever runs. It
// holds no production code and is imported by tests only. One change from the
// original: counts and dated return an error or a bool instead of failing a
// *testing.T, so a test can assert on them.
package e002row

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var attrRE = regexp.MustCompile(`([^\s"'<>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?`)

// Node is an element of a page, or a run of its text (tag "").
type Node struct {
	tag, data string
	attrs     map[string]string
	up        *Node
	kids      []*Node
}

var (
	junk   = regexp.MustCompile(`(?is)<!--.*?-->|<script\b.*?</script\s*>|<style\b.*?</style\s*>|<[!?][^>]*>`)
	pieces = regexp.MustCompile(`<(/?)([a-zA-Z][^\s/>]*)((?:[^>"']|"[^"]*"|'[^']*')*)>|[^<]+|<`)
	ends   = map[string]string{"li": " li ", "tr": " tr td th ", "td": " td th ", "th": " td th ", "p": " p "}
	empty  = " area base br col embed hr img input link meta source track wbr "
)

// Dom is a page's elements as a tree, forgiving of the end tags HTML may leave out.
func Dom(text string) *Node {
	root := &Node{tag: "#root"}
	cur := root
	for _, m := range pieces.FindAllStringSubmatch(junk.ReplaceAllString(text, " "), -1) {
		name := strings.ToLower(m[2])
		switch {
		case name == "":
			cur.kids = append(cur.kids, &Node{data: html.UnescapeString(m[0]), up: cur})
		case m[1] == "/":
			for n := cur; n.up != nil; n = n.up {
				if n.tag == name {
					cur = n.up
					break
				}
			}
		default:
			for cur.up != nil && strings.Contains(ends[name], " "+cur.tag+" ") {
				cur = cur.up
			}
			n := &Node{tag: name, attrs: map[string]string{}, up: cur}
			for _, a := range attrRE.FindAllStringSubmatch(m[3], -1) {
				n.attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2] + a[3] + a[4])
			}
			if cur.kids = append(cur.kids, n); !strings.HasSuffix(m[3], "/") && !strings.Contains(empty, " "+name+" ") {
				cur = n
			}
		}
	}
	return root
}

// All lists the elements under n with that tag, or every one for "*".
func (n *Node) All(tag string) (out []*Node) {
	for _, k := range n.kids {
		if k.tag != "" && (k.tag == tag || tag == "*") {
			out = append(out, k)
		}
		out = append(out, k.All(tag)...)
	}
	return out
}

func (n *Node) within(tags string) *Node {
	for p := n.up; p != nil; p = p.up {
		if strings.Contains(tags, " "+p.tag+" ") {
			return p
		}
	}
	return nil
}

// Text is what an element reads as: its text, with each child's aria-label, title and text.
func (n *Node) Text() string {
	var out []string
	for _, k := range n.kids {
		if out = append(out, k.data); k.tag != "" {
			out = append(out, k.attrs["aria-label"], k.attrs["title"], k.Text())
		}
	}
	return strings.Join(strings.Fields(strings.Join(out, " ")), " ")
}

var slugRE = regexp.MustCompile(`^/reports/([^/]+)/?$`)

func slugOf(a *Node) string {
	if u, err := url.Parse(strings.TrimSpace(a.attrs["href"])); err == nil {
		if m := slugRE.FindStringSubmatch(u.EscapedPath()); m != nil {
			s, _ := url.PathUnescape(m[1])
			return s
		}
	}
	return ""
}

// Listed reads the show-all page: report slugs in the order their links come, and each one's row:
// its tr, li or article, else the largest element around its link that links no other report.
func Listed(page string) (order []string, rows map[string]*Node) {
	rows = map[string]*Node{}
	for _, a := range Dom(page).All("a") {
		slug := slugOf(a)
		alone := func(n *Node) bool {
			for _, x := range n.All("a") {
				if s := slugOf(x); s != "" && s != slug {
					return false
				}
			}
			return true
		}
		if slug == "" || rows[slug] != nil {
			continue
		}
		box := a.within(" tr li article ")
		if box == nil || !alone(box) {
			for box = a; box.up.tag != "#root" && alone(box.up); box = box.up {
			}
		}
		order, rows[slug] = append(order, slug), box
	}
	return order, rows
}

var (
	dates = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:[t ][\d:.]+(?:z|[+-][\d:]+)?)?` +
		`|\b\d{1,2}(?:st|nd|rd|th)?\s+(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?,?\s+\d{4}` +
		`|\b(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+\d{1,2}(?:st|nd|rd|th)?,?\s+\d{4}` +
		`|\b\d{1,2}[/.]\d{1,2}[/.]\d{2,4}\b|\b\d{1,2}:\d{2}(?::\d{2})?\b`)
	icons  = map[rune]int{0x1F44D: 1, 0x1F4AC: 2, 0x1F441: 3}
	labels = map[string]int{"like": 1, "likes": 1, "comment": 2, "comments": 2, "times opened": 3, "opened": 3, "open": 3, "opens": 3, "view": 3, "views": 3}
)

func init() {
	for _, w := range []string{"thumbup", "thumbups", "thumbsup", "thumbsups"} {
		labels[w], labels[strings.Replace(w, "up", " up", 1)], labels[strings.Replace(w, "up", "-up", 1)] = 1, 1, 1
	}
}

// rowText is a row's text; in a table, each cell after its column's heading.
func rowText(row *Node) string {
	cells := func(tr *Node, heads bool) (out []string) {
		for _, c := range tr.kids {
			if heads && c.tag != "" && c.tag != "th" {
				return nil
			} else if c.tag == "td" || c.tag == "th" {
				out = append(out, c.Text())
			}
		}
		return out
	}
	if table := row.within(" table "); row.tag == "tr" && table != nil {
		for _, tr := range table.All("tr") {
			if heads, mine := cells(tr, true), cells(row, false); tr != row && len(heads) > 0 {
				if len(heads) != len(mine) {
					break
				}
				for i := range mine {
					mine[i] = heads[i] + " " + mine[i]
				}
				return strings.Join(mine, " ; ")
			}
		}
	}
	return row.Text()
}

func letter(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) && !unicode.IsDigit(r) }

// Counts reads a row's likes, visible comments and times opened as a person reads them: each
// label's number beside it, on the same side for every label where that reads. The error says
// the row's numbers "cannot be read apart", as the finish line's reader does.
func Counts(row *Node) ([3]int, error) {
	text := rowText(row)
	rs := []rune(dates.ReplaceAllString(strings.ToLower(text), " ") + "  ")
	type word struct{ label, n, at int }
	var ws []word // label 1, 2, 3 for likes, comments, times opened; -1 a number; 0 any other word
	end := func(i int) int {
		for letter(rs[i]) {
			i++
		}
		return i
	}
	for i := 0; i < len(rs)-2; {
		w, j := word{icons[rs[i]], 0, len(ws)}, i+1
		switch {
		case w.label > 0:
			if i > 0 && letter(rs[i-1]) || letter(rs[j]) {
				w.label = -2
			}
		case rs[i] >= '0' && rs[i] <= '9':
			for w.label, j = -1, i; rs[j] >= '0' && rs[j] <= '9' || rs[j] == ','; j++ {
				if rs[j] != ',' {
					w.n = w.n*10 + int(rs[j]-'0')
				}
			}
		case letter(rs[i]):
			j = end(i)
			w.label = labels[string(rs[i:j])]
			if k := end(j + 1); (rs[j] == ' ' || rs[j] == '-') && k > j+1 && labels[string(rs[i:k])] > 0 {
				w.label, j = labels[string(rs[i:k])], k
			}
		default:
			w.label = -2
		}
		if i = j; w.label != -2 {
			ws = append(ws, w)
		}
	}
	var near [4][]word
	for i, w := range ws {
		for _, j := range []int{i - 1, i + 1} {
			if w.label > 0 && j >= 0 && j < len(ws) && ws[j].label == -1 {
				near[w.label] = append(near[w.label], word{j - i, ws[j].n, j})
			}
		}
	}
	found := map[[3]int]bool{}
	for even := 1; even >= 0 && len(found) == 0; even-- {
		for _, a := range near[1] {
			for _, b := range near[2] {
				for _, c := range near[3] {
					if a.at != b.at && a.at != c.at && b.at != c.at && (even == 0 || a.label == b.label && b.label == c.label) {
						found[[3]int{a.n, b.n, c.n}] = true
					}
				}
			}
		}
	}
	for f := range found {
		if len(found) == 1 {
			return f, nil
		}
	}
	return [3]int{}, fmt.Errorf("this row's likes, comments and times opened cannot be read apart: %q", text)
}

// Dated says whether the row shows the publish date of at (UTC or local time, a day either side).
func Dated(row *Node, at time.Time) bool {
	parts := []string{row.Text()}
	for _, n := range append(row.All("*"), row) {
		for _, v := range n.attrs {
			parts = append(parts, v)
		}
	}
	blob := regexp.MustCompile(`(\d)(?:st|nd|rd|th)\b`).ReplaceAllString(strings.ToLower(strings.Join(parts, " ")), "${1} ")
	blob = strings.Join(strings.Fields(strings.NewReplacer(".", " ", ",", " ").Replace(blob)), " ")
	for _, now := range []time.Time{at.UTC(), at} {
		for k := -1; k <= 1; k++ {
			y, mo, d := now.AddDate(0, 0, k).Date()
			m, month := int(mo), strings.ToLower(mo.String())
			forms := []string{fmt.Sprintf("%04d-%02d-%02d", y, m, d), fmt.Sprintf("%d/%02d/%02d", y, m, d), fmt.Sprintf("%02d/%02d/%d", d, m, y),
				fmt.Sprintf("%02d/%02d/%d", m, d, y), fmt.Sprintf("%d/%d/%d", d, m, y), fmt.Sprintf("%d/%d/%d", m, d, y), fmt.Sprintf("%02d %02d %d", d, m, y)}
			for _, name := range []string{month, month[:3]} {
				forms = append(forms, fmt.Sprintf("%d %s %d", d, name, y), fmt.Sprintf("%02d %s %d", d, name, y), fmt.Sprintf("%s %d %d", name, d, y), fmt.Sprintf("%s %02d %d", name, d, y))
			}
			for _, f := range forms {
				if strings.Contains(blob, f) {
					return true
				}
			}
		}
	}
	return false
}
