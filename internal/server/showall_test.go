package server

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/comments"
	"github.com/sshubham2/hband/internal/testutil/e002row"
)

// seedBulk writes n rows directly: n readers each holding a vote of kind on the
// report, and (separately) n opens of it. It is the only way to reach a count of
// 1000 without 1000 sign-ins.
func (h *harness) seedLikes(reportID int64, firstUser, n int) {
	h.t.Helper()
	tx, err := h.store.DB().Begin()
	if err != nil {
		h.t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		id := firstUser + i
		if _, err := tx.Exec(`INSERT OR IGNORE INTO users (github_id, login, avatar_url, created_at, updated_at) VALUES (?, ?, '', 1, 1)`, id, "bulk"+strconv.Itoa(id)); err != nil {
			h.t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO votes (report_id, github_id, kind, created_at, updated_at) VALUES (?, ?, 'up', 1, 1)`, reportID, id); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) seedOpens(reportID int64, n int) {
	h.t.Helper()
	tx, err := h.store.DB().Begin()
	if err != nil {
		h.t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		b := []byte(fmt.Sprintf("seeded--%08d", i)) // 16 bytes
		if _, err := tx.Exec(`INSERT INTO report_opens (report_id, day, visitor) VALUES (?, 20000, ?)`, reportID, b); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		h.t.Fatal(err)
	}
}

var liTag = regexp.MustCompile(`<li[ >]`)
var rowList = regexp.MustCompile(`(?s)<ol class="report-list">.*?</ol>`)

// TestShowAll draws the show-all page over a library that holds published reports,
// a draft, a report unpublished after it was published, a deleted one and two
// published in one second, with counts of 0, 1 and 1000 and a title that ends in a
// digit. Every row goes through a port of the finish line's own row reader.
func TestShowAll(t *testing.T) {
	t.Run("nothing is published yet", func(t *testing.T) {
		h := newHarness(t, false)
		res := h.serve("GET", "/reports")
		if res.Status != 200 || !strings.Contains(res.Body, "No reports yet.") || liTag.MatchString(res.Body) ||
			strings.Contains(res.Body, "report-list") || !strings.Contains(res.Body, "<h1>All reports</h1>") {
			t.Fatalf("an empty library: %d\n%s", res.Status, res.Body)
		}
		// A library with only a draft is empty to the public too.
		owner := h.mint(testOwnerID, "the-owner")
		h.mustUpload(owner, "d", "Only a draft", "draft", reportHTML(300, "d"))
		if res := h.serve("GET", "/reports"); !strings.Contains(res.Body, "No reports yet.") || strings.Contains(res.Body, "Only a draft") {
			t.Fatalf("a draft shown, or the empty state missing:\n%s", res.Body)
		}
	})

	h := newHarness(t, false)
	h.trustPeer()
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "r-old", "Harbour Survey 2", "published", reportHTML(300, "old"))
	h.clock.Advance(time.Hour)
	// Two published in one second: the one saved later has the higher id and is first.
	h.mustUpload(owner, "r-two", "Two Of A Second", "published", reportHTML(300, "two"))
	h.mustUpload(owner, "r-one", "One Of A Second", "published", reportHTML(300, "one"))
	h.clock.Advance(time.Hour)
	h.mustUpload(owner, "hidden-draft", "A Hidden Draft", "draft", reportHTML(300, "draft"))
	h.mustUpload(owner, "was-published", "Was Published Once", "published", reportHTML(300, "was"))
	h.mustUpload(owner, "was-deleted", "Was Deleted Once", "published", reportHTML(300, "del"))
	h.mustAct(owner, "was-published", "unpublish")
	h.mustAct(owner, "was-deleted", "delete")

	idOf := func(slug string) int64 { id, _, _, _ := h.reportRow(slug); return id }
	reader, other := h.mint(7, "reader"), h.mint(8, "other")
	// r-old: 1000 likes, 5 comments, 1000 views. r-one: 1, 1, 1. r-two: 0, 2, 0.
	h.seedLikes(idOf("r-old"), 1000, 1000)
	h.seedOpens(idOf("r-old"), 1000)
	for i := 0; i < 5; i++ {
		h.say(reader, "r-old", "comment "+strconv.Itoa(i), 0)
	}
	h.mustVote(reader, "r-one", "up")
	h.mustVote(other, "r-one", "down") // a dislike is never in the number
	h.mustVote(other, "r-two", "down")
	h.say(reader, "r-one", "the one comment", 0)
	h.say(reader, "r-two", "first", 0)
	gone := h.say(other, "r-two", "removed, so not counted", 0)
	h.say(reader, "r-two", "second", 0)
	h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, gone)
	h.visit("/reports/r-one", "198.51.100.1")

	res := h.serve("GET", "/reports")
	if res.Status != 200 {
		t.Fatalf("GET /reports: %d\n%s", res.Status, res.Body)
	}
	order, rows := e002row.Listed(res.Body)
	if want := []string{"r-one", "r-two", "r-old"}; strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("rows in order %v, want %v (newest publish date first, the higher id first within a second)", order, want)
	}
	for slug, c := range map[string]struct {
		title string
		want  [3]int
	}{
		"r-one": {"One Of A Second", [3]int{1, 1, 1}},
		"r-two": {"Two Of A Second", [3]int{0, 2, 0}},
		"r-old": {"Harbour Survey 2", [3]int{1000, 5, 1000}},
	} {
		row := rows[slug]
		if !strings.Contains(row.Text(), c.title) {
			t.Errorf("row %s does not hold its title %q: %q", slug, c.title, row.Text())
		}
		got, err := e002row.Counts(row)
		if err != nil || got != c.want {
			t.Errorf("row %s reads %q as %v (%v), want %v", slug, row.Text(), got, err, c.want)
		}
		if !e002row.Dated(row, h.clock.Now()) && slug != "r-old" {
			t.Errorf("row %s shows no publish date of the day it was published: %q", slug, row.Text())
		}
	}
	// r-old was published two hours before the others: its own date is the first day
	// too (the fake clock moves by hours), so it is dated as well.
	if !e002row.Dated(rows["r-old"], h.clock.Now().Add(-2*time.Hour)) {
		t.Errorf("the row of r-old shows no publish date: %q", rows["r-old"].Text())
	}

	// The labels are singular at 1 and plural otherwise, the numbers sit left of
	// their labels with no thousands separator, and the date follows the numbers.
	list := rowList.FindString(res.Body)
	for _, want := range []string{
		`<p class="numbers">1 like &middot; 1 comment &middot; 1 view</p>`,
		`<p class="numbers">0 likes &middot; 2 comments &middot; 0 views</p>`,
		`<p class="numbers">1000 likes &middot; 5 comments &middot; 1000 views</p>`,
		`<h2><a href="/reports/r-one">One Of A Second</a></h2>`,
		`<p class="published">Published <time datetime="2027-01-15">15 January 2027</time></p>`,
	} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %s:\n%s", want, list)
		}
	}
	if i, j := strings.Index(list, `class="numbers"`), strings.Index(list, `class="published"`); i < 0 || j < i {
		t.Error("a row shows its date before its numbers: a count right after the date reads as part of it")
	}
	// Nothing hidden, nothing about dislikes, the legend outside the rows.
	for _, never := range []string{"A Hidden Draft", "hidden-draft", "Was Published Once", "was-published", "Was Deleted Once", "was-deleted", "ikes</p>\n<p>"} {
		if strings.Contains(res.Body, never) && never != "ikes</p>\n<p>" {
			t.Errorf("the page shows %q", never)
		}
	}
	if strings.Contains(strings.ToLower(res.Body), "dislike") {
		t.Error("the show-all page mentions dislikes")
	}
	if strings.Contains(list, "A view is one visitor") || !strings.Contains(res.Body, "A view is one visitor opening a report on one day (UTC). Obvious bots are not counted.") {
		t.Error("the legend of what a view is must be on the page, outside the list")
	}
	if n := strings.Count(list, "<li class=\"report-row\">"); n != 3 {
		t.Errorf("%d rows, want 3", n)
	}
	for _, rowTitle := range []string{"title=", "aria-label"} {
		if strings.Contains(list, rowTitle) {
			t.Errorf("a row carries %s, which the finish line's reader would add to the row's text", rowTitle)
		}
	}

	// The page is a reader page: private cache, the nav link, and sign-in comes back here.
	if got := res.Header.Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
	if !strings.Contains(res.Body, `<a href="/reports">All reports</a>`) || !strings.Contains(res.Body, "<title>All reports - hband</title>") {
		t.Error("the page lacks its title or the All reports link")
	}
	if next := signInNext(t, res.Body); next != "/reports" {
		t.Errorf("sign-in returns to %q, want /reports", next)
	}
	for _, p := range []string{"/", "/reports/r-one", "/reports/r-old/comments/1"} {
		if body := h.serve("GET", p).Body; !strings.Contains(body, `<a href="/reports">All reports</a>`) {
			t.Errorf("GET %s has no All reports link", p)
		}
	}
	// HEAD is the GET's headers and nothing is counted.
	before := h.allOpens()
	head := h.serve("HEAD", "/reports", withHeader("User-Agent", chromeUA), withHeader("X-Forwarded-For", "198.51.100.9"))
	if get := h.serve("GET", "/reports"); head.Status != 200 || headerSig(head.Header) != headerSig(get.Header) || head.Header.Get("Cache-Control") != "private, no-cache" {
		t.Errorf("HEAD /reports: %d %v, want the GET's headers", head.Status, head.Header)
	}
	h.visit("/reports", "198.51.100.9")
	if h.allOpens() != before {
		t.Error("the show-all page counted an open")
	}

	// Rows are the same for an anonymous visitor, a reader and the owner.
	anon := rowList.FindString(h.serve("GET", "/reports").Body)
	for who, sid := range map[string]string{"reader": reader, "owner": owner} {
		if got := rowList.FindString(h.serve("GET", "/reports", h.withSID(sid)).Body); got != anon || got == "" {
			t.Errorf("the rows differ for the %s", who)
		}
	}

	// A new visitor opening a report is one more view on its row.
	h.visit("/reports/r-two", "198.51.100.2")
	h.visit("/reports/r-two", "198.51.100.2")
	h.visit("/reports/r-two", "198.51.100.3")
	_, rows = e002row.Listed(h.serve("GET", "/reports").Body)
	if got, err := e002row.Counts(rows["r-two"]); err != nil || got != [3]int{0, 2, 2} {
		t.Errorf("r-two after two visitors opened it: %v %v, want 2 views", got, err)
	}

	// Unpublishing removes the row and keeps its numbers; republishing returns it.
	h.mustAct(owner, "r-two", "unpublish")
	if body := h.serve("GET", "/reports").Body; strings.Contains(body, "Two Of A Second") {
		t.Error("an unpublished report is listed")
	}
	h.mustAct(owner, "r-two", "publish")
	_, rows = e002row.Listed(h.serve("GET", "/reports").Body)
	if got, err := e002row.Counts(rows["r-two"]); err != nil || got != [3]int{0, 2, 2} {
		t.Errorf("r-two after being unpublished and republished: %v %v", got, err)
	}
}

// drawn is the ids of the comments a reader sees on the report page and on every
// branch page its "continue this thread" links lead to, placeholders left out. A
// comment that a link ends a page on is drawn again as the root of the page the
// link opens, so the ids are a set.
func (h *harness) drawn(t *testing.T, slug string) map[int64]bool {
	t.Helper()
	cont := regexp.MustCompile(`<a class="continue" href="(/reports/[^"]+)">`)
	seen := map[int64]bool{}
	pages := []string{"/reports/" + slug}
	visited := map[string]bool{}
	for len(pages) > 0 {
		p := pages[0]
		pages = pages[1:]
		if visited[p] {
			continue
		}
		visited[p] = true
		res := h.serve("GET", p)
		if res.Status != 200 {
			t.Fatalf("GET %s: %d", p, res.Status)
		}
		for _, a := range parseThread(t, res.Body) {
			if !strings.Contains(a.Own, `class="removed"`) {
				seen[a.ID] = true
			}
		}
		for _, m := range cont.FindAllStringSubmatch(res.Body, -1) {
			pages = append(pages, m[1])
		}
	}
	return seen
}

var printedCount = regexp.MustCompile(`<p class="comment-count">(\d+) comments?</p>`)

// TestShowAllCommentCount: for each of the spike's fixtures the number on the
// show-all row is the one the report page prints, is the count of the comments
// that pass the visibility predicate, and is the number of comments the report
// page and its branch pages draw together (the report page alone draws fewer for a
// thread deeper than the cap, and the rest are on the branch pages).
func TestShowAllCommentCount(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "b", "Deep Chain", "published", reportHTML(300, "b"))
	h.mustUpload(owner, "c", "No Comments", "published", reportHTML(300, "c"))
	h.mustUpload(owner, "d", "All By A Blocked Writer", "published", reportHTML(300, "d"))
	// Slug x is fixture A.
	h.mint(8, "eight")
	h.mint(9, "nine")
	nine := h.mint(9, "nine")
	_ = nine
	book := h.book()
	post := func(slug string, who, parent int64, body string) int64 {
		t.Helper()
		id, _, _, _ := h.reportRow(slug)
		c, err := book.Post(bg, id, who, parent, body)
		if err != nil {
			t.Fatalf("post on %s: %v", slug, err)
		}
		return c
	}
	// A: a removed root with a visible reply, and a blocked writer at depth with a
	// visible child.
	root := post("x", 7, 0, "a root that is removed")
	post("x", 8, root, "a visible reply under it")
	c1 := post("x", 7, 0, "c1")
	c2 := post("x", 9, c1, "c2 by the writer who is blocked")
	c3 := post("x", 7, c2, "c3, a visible child of a blocked comment")
	post("x", 7, c3, "c4, deeper still")
	post("x", 9, 0, "a blocked writer's leaf")
	h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, root)
	// B: 110 deep.
	chainThread(t, h, "b", 7, 110)
	// D: every comment by the blocked writer.
	d1 := post("d", 9, 0, "d1")
	d2 := post("d", 9, d1, "d2")
	post("d", 9, d2, "d3")
	h.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (9, 5)`)

	check := func(label string) {
		t.Helper()
		_, rows := e002row.Listed(h.serve("GET", "/reports").Body)
		for slug, want := range map[string]int{"x": 4, "b": 110, "c": 0, "d": 0} {
			if label == "after the block is lifted" {
				want = map[string]int{"x": 6, "b": 110, "c": 0, "d": 3}[slug]
			}
			row := rows[slug]
			if row == nil {
				t.Fatalf("%s: no row for %s", label, slug)
			}
			counts, err := e002row.Counts(row)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			shown := counts[1]
			// The number the report page prints.
			page := h.serve("GET", "/reports/"+slug)
			m := printedCount.FindStringSubmatch(page.Body)
			if m == nil {
				t.Fatalf("%s: /reports/%s prints no comment count", label, slug)
			}
			printed, _ := strconv.Atoi(m[1])
			// What the forest says.
			id, _, _, _ := h.reportRow(slug)
			nodes, err := h.app.comments.Tree(bg, id)
			if err != nil {
				t.Fatal(err)
			}
			// What the visibility predicate says, in SQL.
			var sqlCount int
			h.store.DB().QueryRow(`SELECT count(*) FROM comments c WHERE c.report_id = ? AND c.removed_at IS NULL
				AND NOT EXISTS (SELECT 1 FROM blocked_accounts k WHERE k.github_id = c.github_id)`, id).Scan(&sqlCount)
			// What a reader can read, page by page.
			drawn := len(h.drawn(t, slug))
			if shown != want || printed != want || comments.Visible(nodes) != want || sqlCount != want || drawn != want {
				t.Errorf("%s, report %s: show-all %d, page prints %d, Visible(Tree) %d, SQL %d, drawn on the page and its branches %d; want all %d",
					label, slug, shown, printed, comments.Visible(nodes), sqlCount, drawn, want)
			}
		}
	}
	check("while 9 is blocked")
	// The report page alone draws fewer than the number for the chain deeper than the
	// cap: that is why the count of drawn entries must follow the branch links.
	idB, _, _, _ := h.reportRow("b")
	if one := len(parseThread(t, h.serve("GET", "/reports/b").Body)); one >= 110 || one != threadDepthCap {
		t.Errorf("the report page alone draws %d entries of the chain for the cap of %d (id %d)", one, threadDepthCap, idB)
	}
	h.exec(`DELETE FROM blocked_accounts WHERE github_id = 9`)
	check("after the block is lifted")
	_ = reader
}
