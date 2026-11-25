package program

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// The readers below are the finish lines' own page readers, ported: what a page
// shows as a count beside its label, as a comment (an element id="comment-<n>"),
// as a placeholder, as a report's visible-comment count. They look at pages
// only; nothing here reaches into the program.

var labelKinds = []string{"likes", "dislikes", "comments", "opens"}

// labelWords are the spellings of each label, in the order a match prefers
// them (a longer spelling before the shorter one it contains).
var labelWords = map[string][]string{
	"likes":    append(append([]string{"likes", "like"}, thumbs("up")...), "upvotes", "upvote", "\U0001F44D"),
	"dislikes": append(append([]string{"dislikes", "dislike"}, thumbs("down")...), "downvotes", "downvote", "\U0001F44E"),
	"comments": {"comments", "comment", "\U0001F4AC"},
	"opens":    {"times opened", "opened", "opens", "open", "views", "view"},
}

func thumbs(dir string) (out []string) {
	for _, th := range []string{"thumbs", "thumb"} {
		for _, sep := range []string{" ", "_", "-", ""} {
			for _, d := range []string{dir + "s", dir} {
				out = append(out, th+sep+d)
			}
		}
	}
	return out
}

type token struct {
	kind string // a label's name, "num", or "" for any other word
	text string
}

func isWordLetter(r rune) bool { return unicode.IsLetter(r) }

// tokens reads text as labels, numbers and other words.
func tokens(text string) []token {
	rs := []rune(strings.ToLower(text))
	var out []token
	for i := 0; i < len(rs); {
		matched := false
		for _, kind := range labelKinds {
			for _, w := range labelWords[kind] {
				wr := []rune(w)
				if i+len(wr) > len(rs) || string(rs[i:i+len(wr)]) != w {
					continue
				}
				if i > 0 && isWordLetter(rs[i-1]) {
					continue
				}
				if j := i + len(wr); j < len(rs) && isWordLetter(rs[j]) {
					continue
				}
				out = append(out, token{kind, w})
				i += len(wr)
				matched = true
				break
			}
			if matched {
				break
			}
		}
		if matched {
			continue
		}
		switch {
		case rs[i] >= '0' && rs[i] <= '9':
			j := i
			for j < len(rs) && rs[j] >= '0' && rs[j] <= '9' {
				j++
			}
			out = append(out, token{"num", string(rs[i:j])})
			i = j
		case isWordLetter(rs[i]):
			j := i
			for j < len(rs) && isWordLetter(rs[j]) {
				j++
			}
			out = append(out, token{"", string(rs[i:j])})
			i = j
		default:
			i++
		}
	}
	return out
}

// tally is each label's numbers in one piece of text: the number beside each
// label, on the side that reads more of them.
func tally(text string) map[string]map[int]bool {
	toks := tokens(text)
	best := map[string]map[int]bool{}
	size := func(m map[string]map[int]bool) (n int) {
		for _, v := range m {
			n += len(v)
		}
		return n
	}
	for _, side := range []int{-1, 1} {
		got, used := map[string]map[int]bool{}, map[int]bool{}
		for i, t := range toks {
			j := i + side
			for t.kind != "" && j >= 0 && j < len(toks) && toks[j].kind == t.kind {
				j += side
			}
			if t.kind != "" && t.kind != "num" && j >= 0 && j < len(toks) && toks[j].kind == "num" && !used[j] {
				used[j] = true
				n, _ := strconv.Atoi(toks[j].text)
				if got[t.kind] == nil {
					got[t.kind] = map[int]bool{}
				}
				got[t.kind][n] = true
			}
		}
		if size(got) > size(best) {
			best = got
		}
	}
	return best
}

var blockTags = " article aside br button dd div dl dt footer form h1 h2 h3 h4 h5 h6 header hr label li main nav ol p section summary table td th tr ul "

var splitPieces = regexp.MustCompile("[,;|·•\n]")

// Pieces are the texts a person reads apart: split at block elements, and at
// commas, semicolons, bars, middle dots and bullets.
func Pieces(n *Node) []string {
	out := []string{""}
	var visit func(*Node)
	visit = func(x *Node) {
		switch {
		case x.Tag == "#text":
			out[len(out)-1] += x.Data
		case !strings.Contains(skipText, " "+x.Tag+" "):
			block := strings.Contains(blockTags, " "+x.Tag+" ")
			if block {
				out = append(out, "")
			}
			out[len(out)-1] += " " + x.Attrs["aria-label"] + " " + x.Attrs["alt"] + " "
			for _, k := range x.Kids {
				visit(k)
			}
			if block {
				out = append(out, "")
			}
		}
	}
	visit(n)
	var pieces []string
	for _, s := range out {
		for _, p := range splitPieces.Split(s, -1) {
			if strings.TrimSpace(p) != "" {
				pieces = append(pieces, p)
			}
		}
	}
	return pieces
}

// Counts is each label's numbers as n (a page, or part of one) shows them.
func Counts(n *Node) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	for _, k := range labelKinds {
		out[k] = map[int]bool{}
	}
	for _, p := range Pieces(n) {
		for k, v := range tally(p) {
			for x := range v {
				out[k][x] = true
			}
		}
	}
	return out
}

// Numbers lists a set of counts in order.
func Numbers(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for x := range m {
		out = append(out, x)
	}
	sort.Ints(out)
	return out
}

// Count is the one count of that kind n shows: a number beside its label
// ("3 likes", "Likes: 3", a thumb and 3). It fails t unless there is exactly one.
func Count(t T, n *Node, kind, where string) int {
	t.Helper()
	got := Numbers(Counts(n)[kind])
	// ported:E3-304,E5-302,E62-303
	if len(got) != 1 {
		t.Fatalf("%s shows %s count(s) %v, not one number beside its label", where, kind, got)
		return 0
	}
	return got[0]
}

var splitBeside = regexp.MustCompile(`[,;|<>\n]`)

// Beside is every number next to a dislike label, on either side, in the page's
// text or in its markup.
func Beside(pg Page) map[int]bool {
	found := map[int]bool{}
	parts := append(Pieces(pg.Doc()), splitBeside.Split(pg.Body, -1)...)
	for _, p := range parts {
		toks := tokens(p)
		for i, tk := range toks {
			if tk.kind != "dislikes" {
				continue
			}
			for _, j := range []int{i - 1, i + 1} {
				if j >= 0 && j < len(toks) && toks[j].kind == "num" {
					n, _ := strconv.Atoi(toks[j].text)
					found[n] = true
				}
			}
		}
	}
	return found
}

// Vote posts vote=up|down|none from the report page and fails t on a status of 400 or more.
func Vote(t T, v *Visitor, slug, kind string) {
	t.Helper()
	v.GetFollow(t, "/reports/"+slug)
	got := v.PostFollow(t, "/reports/"+slug+"/vote", url.Values{"vote": {kind}})
	// ported:E3-310,E5-308,E62-309
	if got.Status >= 400 {
		t.Fatalf("%s's vote %s on %s answered %d: %.200s", v.Login, kind, slug, got.Status, got.Body)
	}
}

// Dislikes is the dislike count on the owner's admin view of the report.
func Dislikes(t T, owner *Visitor, slug string) int {
	t.Helper()
	// ported:E3-314,E62-313
	return Count(t, owner.GetFollow(t, "/admin/reports/"+slug).Doc(), "dislikes", "/admin/reports/"+slug)
}

var cidRE = regexp.MustCompile(`^comment-(\d+)$`)

// IsComment reports whether n is a comment's element, id="comment-<n>".
func IsComment(n *Node) bool { return cidRE.MatchString(n.Attrs["id"]) }

// Shown lists the comments a page shows, in page order: their ids, and their elements by id.
func Shown(doc *Node) (order []string, by map[string]*Node) {
	by = map[string]*Node{}
	for _, n := range doc.Walk() {
		if m := cidRE.FindStringSubmatch(n.Attrs["id"]); m != nil {
			if _, dup := by[m[1]]; !dup {
				order = append(order, m[1])
			}
			by[m[1]] = n
		}
	}
	return order, by
}

// Own is a comment's own text: its element's, less the comments nested in it.
func Own(n *Node) string {
	parts := make([]string, len(n.Kids))
	for i, k := range n.Kids {
		parts[i] = k.Text(IsComment)
	}
	return strings.Join(parts, " ")
}

// Find is the id of the comment whose own text holds text, or "".
func Find(doc *Node, text string) string {
	order, by := Shown(doc)
	for _, id := range order {
		if strings.Contains(Own(by[id]), text) {
			return id
		}
	}
	return ""
}

// Depth is how many comments hold n, itself counted.
func Depth(n *Node) int {
	d := 0
	for _, a := range n.Chain() {
		if IsComment(a) {
			d++
		}
	}
	return d
}

// Mine are the nodes of a comment's element that are its own, not its nested comments'.
func Mine(n *Node) []*Node {
	var out []*Node
	for _, k := range n.Kids {
		if !IsComment(k) {
			out = append(out, k)
			out = append(out, Mine(k)...)
		}
	}
	return out
}

// Say has v post text from the page (default /reports/<slug>) to
// POST /reports/<slug>/comments, answering comment parent ("" for a top-level
// one), and returns the new comment's id where that page shows it.
func Say(t T, v *Visitor, slug, text, parent string, from *Page) string {
	t.Helper()
	var pg Page
	if from != nil {
		pg = *from
	} else {
		pg = v.GetFollow(t, "/reports/"+slug)
	}
	form := url.Values{"body": {text}}
	if parent != "" {
		form.Set("parent", parent)
	}
	// ported:E41-336
	got := v.PostFollow(t, "/reports/"+slug+"/comments", form)
	// ported:E41-337,E42-279,E61-339,E62-340
	if got.Status >= 400 {
		t.Fatalf("%s's comment on %s answered %d: %.200s", v.Login, slug, got.Status, got.Body)
		return ""
	}
	return Find(v.GetFollow(t, pg.URL).Doc(), text)
}

// Part is the largest element around text that shows none of others.
func Part(t T, pg Page, text string, others []string) *Node {
	t.Helper()
	var n *Node
	for _, x := range pg.Doc().Walk() {
		if x.Tag == "#text" && strings.Contains(x.Data, text) {
			n = x
			break
		}
	}
	// ported:E62-353
	if n == nil {
		t.Fatalf("%s (%d) does not show %s", pg.URL, pg.Status, text)
		return nil
	}
	for n.Parent.Parent != nil {
		seen := false
		for _, o := range others {
			if strings.Contains(n.Parent.Text(nil), o) {
				seen = true
			}
		}
		if seen {
			break
		}
		n = n.Parent
	}
	return n
}

// Visible is a report's public visible-comment count, as /reports (its row) and
// /reports/<slug> show it. It fails t unless the two agree on one number.
func Visible(t T, slug string, v *Visitor) int {
	t.Helper()
	got := Counts(v.GetFollow(t, "/reports/"+slug).Doc())["comments"]
	listing := v.GetFollow(t, "/reports")
	own := regexp.MustCompile(`/reports/` + regexp.QuoteMeta(slug) + `/?$`)
	other := regexp.MustCompile(`/reports/[^/?#]+`)
	var link *Node
	for _, x := range listing.Doc().Walk() {
		if x.Tag == "a" && own.MatchString(x.Attrs["href"]) {
			link = x
			break
		}
	}
	if listing.Status == 200 && link != nil {
		n := link
		for n.Parent.Parent != nil {
			rival := false
			for _, x := range n.Parent.Walk() {
				if m := other.FindString(x.Attrs["href"]); x.Tag == "a" && m != "" && !own.MatchString(x.Attrs["href"]) {
					rival = true
				}
			}
			if rival {
				break
			}
			n = n.Parent
		}
		for x := range Counts(n)["comments"] {
			got[x] = true
		}
	}
	nums := Numbers(got)
	// ported:E61-367,E62-368
	if len(nums) != 1 {
		t.Fatalf("%s's visible-comment count reads %v on /reports and /reports/%s, not one number", slug, nums, slug)
		return 0
	}
	return nums[0]
}
