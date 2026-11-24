package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/comments"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// removeComment sends the owner's remove form for the comment whose id is spelled
// as given, with a raw urlencoded body. sid "" is a visitor with no session.
func (h *harness) removeComment(sid, id, body string, extra ...func(*http.Request)) result {
	h.t.Helper()
	opts := []func(*http.Request){withBody(formType, []byte(body))}
	if sid != "" {
		opts = append(opts, h.withSID(sid))
	}
	return h.serve("POST", "/admin/comments/"+id+"/remove", append(opts, extra...)...)
}

// mustRemove removes comment id as the owner, with no field, and fails unless the
// answer is the 303 to the comments of the reader page.
func (h *harness) mustRemove(owner string, id int64) {
	h.t.Helper()
	res := h.removeComment(owner, itoa(id), "")
	if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#comments" {
		h.t.Fatalf("removing comment %d: %d %q %q", id, res.Status, res.Location(), res.Body)
	}
}

// chainOf posts n comments on slug, each answering the one before, written by the
// given sessions in turn, and returns their ids top first.
func (h *harness) chainOf(slug string, n int, sids ...string) []int64 {
	h.t.Helper()
	ids := make([]int64, 0, n)
	var parent int64
	for i := 1; i <= n; i++ {
		parent = h.say(sids[(i-1)%len(sids)], slug, fmt.Sprintf("level %d of the chain", i), parent)
		ids = append(ids, parent)
	}
	return ids
}

var countRE = regexp.MustCompile(`<p class="comment-count">(\d+) (comments?)</p>`)

// countOn is the comment count a reader page prints, and its label.
func countOn(t *testing.T, page string) (int, string) {
	t.Helper()
	m := countRE.FindAllStringSubmatch(page, -1)
	if len(m) != 1 {
		t.Fatalf("%d comment counts on the page, want exactly one:\n%s", len(m), page)
	}
	n, _ := strconv.Atoi(m[0][1])
	return n, m[0][2]
}

// visibleWhere is the one visibility predicate, spelled for a test that counts
// rows: not removed, and the writer not blocked.
const visibleWhere = `removed_at IS NULL AND NOT EXISTS (SELECT 1 FROM blocked_accounts k WHERE k.github_id = comments.github_id)`

// reportOf is the id of the stored report with the slug.
func (h *harness) reportOf(slug string) int64 {
	h.t.Helper()
	id, _, _, _ := h.reportRow(slug)
	return id
}

// removeFormRE matches the whole control as the reader pages draw it for one
// comment. Line endings between the elements are whatever the template file has.
func removeFormRE(id int64) *regexp.Regexp {
	return regexp.MustCompile(`<details class="remove">\s*<summary>Remove</summary>\s*<form method="post" action="/admin/comments/` + itoa(id) +
		`/remove">\s*<button type="submit">Remove for everyone</button>\s*</form>\s*</details>`)
}

func TestCommentRemove(t *testing.T) {
	t.Run("leaf", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		lone := h.mint(9, "lone-writer")
		h.say(reader, "x", "a comment that stays", 0)
		gone := h.say(lone, "x", "the only comment of the lone writer", 0)
		if n, _ := countOn(t, h.page("", "/reports/x")); n != 2 {
			t.Fatalf("before: %d comments", n)
		}
		res := h.removeComment(owner, itoa(gone), "")
		if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#comments" || res.Body != "" {
			t.Fatalf("the removal: %d %q %q, want a 303 to /reports/x#comments", res.Status, res.Location(), res.Body)
		}
		if got := res.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		for viewer, sid := range map[string]string{"anonymous": "", "reader": reader, "owner": owner} {
			for _, p := range []string{"/", "/reports/x"} {
				page := h.page(sid, p)
				for _, leak := range []string{"lone-writer", "the only comment of the lone writer", `id="comment-` + itoa(gone) + `"`} {
					if strings.Contains(page, leak) {
						t.Errorf("%s GET %s still shows %q", viewer, p, leak)
					}
				}
				if !strings.Contains(page, "a comment that stays") {
					t.Errorf("%s GET %s lost the comment that was not removed", viewer, p)
				}
				if n, label := countOn(t, page); n != 1 || label != "comment" {
					t.Errorf("%s GET %s: count %d %s, want 1 comment", viewer, p, n, label)
				}
			}
		}
		if h.commentCount("id = ? AND removed_at IS NOT NULL", gone) != 1 {
			t.Error("the comment was not marked removed (or its row is gone)")
		}
	})

	t.Run("held", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		other := h.mint(8, "writer-eight")
		c1 := h.say(reader, "x", "one at the top", 0)
		c2 := h.say(other, "x", "two beneath one", c1)
		c3 := h.say(reader, "x", "three, three levels deep", c2)
		c4 := h.say(other, "x", "four, a visible reply under three", c3)
		if n, _ := countOn(t, h.page("", "/reports/x")); n != 4 {
			t.Fatalf("before: %d comments", n)
		}
		held := func(step string, id, parent int64, want int, leaks ...string) {
			t.Helper()
			for viewer, sid := range map[string]string{"anonymous": "", "reader": reader, "owner": owner} {
				page := h.page(sid, "/reports/x")
				arts := parseThread(t, page)
				a := articleByID(arts, id)
				if a == nil {
					t.Fatalf("%s, %s: comment %d is not drawn: it must stay as a placeholder", step, viewer, id)
				}
				for _, s := range []string{`<p class="removed">comment removed</p>`} {
					if !strings.Contains(a.Own, s) {
						t.Errorf("%s, %s: comment %d lacks %q:\n%s", step, viewer, id, s, a.Own)
					}
				}
				for _, s := range []string{"<form", "/admin/comments/", "<img", `class="body"`, "<time", "login", "reader", "writer-eight"} {
					if strings.Contains(a.Own, s) {
						t.Errorf("%s, %s: the placeholder %d carries %q:\n%s", step, viewer, id, s, a.Own)
					}
				}
				if a.Parent != parent {
					t.Errorf("%s, %s: the placeholder %d sits under %d, want %d", step, viewer, id, a.Parent, parent)
				}
				if r := articleByID(arts, c4); r == nil || r.Parent != c3 {
					t.Errorf("%s, %s: the visible reply is not under comment %d: %+v", step, viewer, c3, r)
				}
				for _, s := range leaks {
					if strings.Contains(page, s) {
						t.Errorf("%s, %s: the page still shows %q", step, viewer, s)
					}
				}
				if n, _ := countOn(t, page); n != want {
					t.Errorf("%s, %s: count %d, want %d", step, viewer, n, want)
				}
			}
		}
		// The reply three levels deep has a visible reply beneath it: it stays.
		h.mustRemove(owner, c3)
		held("after removing the reply three levels deep", c3, c2, 3, "three, three levels deep")
		// Then the reply above it: two placeholders in a row, the count one lower again.
		h.mustRemove(owner, c2)
		held("after removing the reply above it", c2, c1, 2, "two beneath one")
		if h.commentCount("1 = 1") != 4 {
			t.Error("a removal deleted a row")
		}
	})

	t.Run("chain-collapses", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		other := h.mint(8, "writer-eight")
		c1 := h.say(reader, "x", "one at the top", 0)
		c2 := h.say(other, "x", "two beneath one", c1)
		c3 := h.say(reader, "x", "three beneath two", c2)
		c4 := h.say(other, "x", "four, the last visible reply", c3)
		h.mustRemove(owner, c2)
		h.mustRemove(owner, c3)
		page := h.page("", "/reports/x")
		if got := articleIDs(parseThread(t, page)); fmt.Sprint(got) != fmt.Sprint([]int64{c1, c2, c3, c4}) {
			t.Fatalf("a chain of two placeholders over the last reply: %v", got)
		}
		if n, _ := countOn(t, page); n != 2 {
			t.Fatalf("count %d, want 2", n)
		}
		// One removal collapses three nodes and lowers the count by one.
		h.mustRemove(owner, c4)
		for viewer, sid := range map[string]string{"anonymous": "", "reader": reader, "owner": owner} {
			page := h.page(sid, "/reports/x")
			if got := articleIDs(parseThread(t, page)); fmt.Sprint(got) != fmt.Sprint([]int64{c1}) {
				t.Errorf("%s: after the last reply was removed the thread is %v, want only %d", viewer, got, c1)
			}
			for _, id := range []int64{c2, c3, c4} {
				if strings.Contains(page, `id="comment-`+itoa(id)+`"`) || strings.Contains(page, `id="replies-`+itoa(id)+`"`) {
					t.Errorf("%s: comment %d is still in the page source", viewer, id)
				}
			}
			if strings.Contains(page, "comment removed") {
				t.Errorf("%s: a placeholder with nothing beneath it is still drawn", viewer)
			}
			if n, _ := countOn(t, page); n != 1 {
				t.Errorf("%s: count %d, want 1", viewer, n)
			}
		}
	})

	t.Run("below-cap", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		ids := h.chainOf("x", 300, reader, owner)
		nodes, err := h.book().Tree(bg, h.reportOf("x"))
		if err != nil {
			t.Fatal(err)
		}
		branchOf := func(id int64) string {
			root := branchRoot(comments.Find(nodes, id))
			if root == nil {
				return "/reports/x"
			}
			return "/reports/x/comments/" + itoa(root.ID)
		}
		depth7, last := ids[6], ids[299]
		if branchOf(depth7) == "/reports/x" || branchOf(last) == "/reports/x" {
			t.Fatal("the seeded comments are drawn on the report page: the case would test nothing")
		}
		// Depth 7 is drawn only on its branch page, which carries its control.
		if page := h.page(owner, "/reports/x"); strings.Contains(page, `action="/admin/comments/`+itoa(depth7)+`/remove"`) {
			t.Fatal("the report page draws a comment below the depth cap")
		}
		for _, id := range []int64{last, depth7} {
			page := h.page(owner, branchOf(id))
			if !removeFormRE(id).MatchString(page) {
				t.Fatalf("the branch page %s has no remove control for comment %d", branchOf(id), id)
			}
			h.mustRemove(owner, id)
		}
		for viewer, sid := range map[string]string{"anonymous": "", "owner": owner} {
			if n, _ := countOn(t, h.page(sid, "/reports/x")); n != 298 {
				t.Errorf("%s: count %d after removing depth 7 and the end of the chain, want 298", viewer, n)
			}
		}
		// The end of the chain is gone; depth 7 is held in its place on its branch page.
		page := h.page(owner, branchOf(depth7))
		arts := parseThread(t, page)
		if a := articleByID(arts, depth7); a == nil || !strings.Contains(a.Own, "comment removed") || strings.Contains(a.Own, "<form") {
			t.Errorf("depth 7 is not held as a bare placeholder on its branch page: %+v", a)
		}
		if res := h.serve("GET", branchOf(last), h.withSID(owner)); strings.Contains(res.Body, `id="comment-`+itoa(last)+`"`) {
			t.Error("the end of the chain is still drawn")
		}
	})

	t.Run("already-hidden", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		blocked := h.mint(8, "blocked-writer")
		c1 := h.say(blocked, "x", "by a writer who is blocked later", 0)
		h.say(reader, "x", "a visible reply under it", c1)
		gone := h.say(reader, "x", "removed twice", 0)
		h.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (8, 5)`)
		count := func() int { n, _ := countOn(t, h.page("", "/reports/x")); return n }
		if count() != 2 {
			t.Fatalf("control: %d comments under the block, want 2", count())
		}
		// A comment a block already hides: removing it is a success, with no second drop.
		h.mustRemove(owner, c1)
		if count() != 2 {
			t.Errorf("removing a comment a block already hid changed the count to %d", count())
		}
		h.exec(`DELETE FROM blocked_accounts WHERE github_id = 8`)
		if page := h.page("", "/reports/x"); strings.Contains(page, "by a writer who is blocked later") || count() != 2 {
			t.Errorf("lifting the block brought a removed comment back (count %d)", count())
		}
		// Removed twice: the second is a 303 and keeps the first time.
		h.mustRemove(owner, gone)
		var at int64
		if err := h.store.DB().QueryRow(`SELECT removed_at FROM comments WHERE id = ?`, gone).Scan(&at); err != nil {
			t.Fatalf("read removed_at: %v", err)
		}
		h.clock.Advance(time.Hour)
		h.mustRemove(owner, gone)
		var again int64
		if err := h.store.DB().QueryRow(`SELECT removed_at FROM comments WHERE id = ?`, gone).Scan(&again); err != nil || again != at {
			t.Errorf("removed_at moved from %d to %d on a repeat: %v", at, again, err)
		}
	})

	t.Run("ids", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		top := h.say(reader, "x", "a comment that must survive", 0)
		h.mustUpload(owner, "y", "Report Y", "published", reportHTML(300, "y"))
		onY := h.say(reader, "y", "a comment that goes with its report", 0)
		h.mustAct(owner, "y", "delete")
		huge := "from=" + strings.Repeat("a", 2<<10)
		for name, id := range map[string]string{
			"not a number": "x", "zero": "0", "leading zero": "01", "plus sign": "+1", "minus sign": "-1",
			"overflow": "99999999999999999999999", "unknown": itoa(top + 100),
			"the comment of a deleted report": itoa(onY),
		} {
			// The id is checked before the body is read: a body over the cap changes nothing.
			for bodyName, body := range map[string]string{"no body": "", "a form": "from=admin", "an oversize body": huge} {
				res := h.removeComment(owner, id, body)
				if res.Status != http.StatusNotFound {
					t.Errorf("%s (%q) with %s: %d, want 404", name, id, bodyName, res.Status)
				}
				if res.Location() != "" {
					t.Errorf("%s with %s: a 404 with Location %q", name, bodyName, res.Location())
				}
			}
		}
		if n := h.commentCount("removed_at IS NOT NULL"); n != 0 {
			t.Errorf("%d comments were removed by requests that named no comment", n)
		}
		if h.commentCount("id = ?", top) != 1 {
			t.Error("the control comment is gone")
		}
	})

	t.Run("refused", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		top := h.say(reader, "x", "a comment that must survive", 0)
		id := itoa(top)
		// The owner's login on another account is not the owner.
		impostor := h.mint(999, "the-owner")
		for name, sid := range map[string]string{"an anonymous visitor": "", "a signed-in reader": reader, "a reader with the owner's login and another id": impostor} {
			res := h.removeComment(sid, id, "")
			if res.Status != http.StatusForbidden || res.Location() != "" {
				t.Errorf("%s: %d %q, want 403 and no Location", name, res.Status, res.Location())
			}
			if hasMarker(res.Body) {
				t.Errorf("%s: the admin marker reached them", name)
			}
		}
		// A POST from another site stops at cross-origin protection, owner or not.
		for _, hdr := range [][2]string{{"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}, {"Origin", "https://evil.example"}} {
			if res := h.removeComment(owner, id, "", withHeader(hdr[0], hdr[1])); res.Status != http.StatusForbidden {
				t.Errorf("the owner's POST with %s: %s: %d, want 403", hdr[0], hdr[1], res.Status)
			}
		}
		// The owner asking with GET or HEAD gets 405 from the mux and changes nothing.
		for _, method := range []string{"GET", "HEAD"} {
			if res := h.serve(method, "/admin/comments/"+id+"/remove", h.withSID(owner)); res.Status != http.StatusMethodNotAllowed {
				t.Errorf("the owner's %s: %d, want 405", method, res.Status)
			}
		}
		if n := h.commentCount("removed_at IS NOT NULL"); n != 0 {
			t.Fatalf("%d comments removed by refused requests", n)
		}
		// Controls: the site's own forms carry these headers and are taken.
		for _, hdr := range [][2]string{{"Sec-Fetch-Site", "same-origin"}, {"Origin", h.cfg.Origin}} {
			if res := h.removeComment(owner, id, "", withHeader(hdr[0], hdr[1])); res.Status != http.StatusSeeOther {
				t.Errorf("control %s: %s: %d", hdr[0], hdr[1], res.Status)
			}
		}
		if h.commentCount("id = ? AND removed_at IS NOT NULL", top) != 1 {
			t.Error("control: the owner's removal did not remove the comment")
		}
	})

	t.Run("landing", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		mk := func(text string) string { return itoa(h.say(reader, "x", text, 0)) }
		gone := func(id string) bool {
			n, _ := strconv.ParseInt(id, 10, 64)
			return h.commentCount("id = ? AND removed_at IS NOT NULL", n) == 1
		}
		for _, c := range []struct {
			name, body, target string // target is the request path suffix after ?
			status             int
			loc                string
		}{
			{"no field", "", "", 303, "/reports/x#comments"},
			{"from admin", "from=admin", "", 303, "/admin/reports/x#comments"},
			{"from blocked, on a published report", "from=blocked", "", 303, "/admin/blocked"},
			{"from blocked in other case", "from=Blocked", "", 400, ""},
			{"from admin and from blocked", "from=admin&from=blocked", "", 400, ""},
			{"from blocked in the query string only", "", "?from=blocked", 303, "/reports/x#comments"},
			{"another value", "from=reports", "", 400, ""},
			{"an empty value", "from=", "", 400, ""},
			{"the value in other case", "from=Admin", "", 400, ""},
			{"repeated", "from=admin&from=admin", "", 400, ""},
			{"repeated with another value", "from=admin&from=x", "", 400, ""},
			{"in the query string only", "", "?from=admin", 303, "/reports/x#comments"},
			{"an unknown field alongside", "other=1", "", 303, "/reports/x#comments"},
			{"a body over 1 KiB", "from=admin&pad=" + strings.Repeat("a", 2<<10), "", 400, ""},
		} {
			id := mk("comment for " + c.name)
			res := h.serve("POST", "/admin/comments/"+id+"/remove"+c.target, h.withSID(owner), withBody(formType, []byte(c.body)))
			if res.Status != c.status || res.Location() != c.loc {
				t.Errorf("%s: %d %q, want %d %q", c.name, res.Status, res.Location(), c.status, c.loc)
			}
			if gone(id) != (c.status == http.StatusSeeOther) {
				t.Errorf("%s: removed = %v for a %d", c.name, gone(id), res.Status)
			}
		}
		// A report with no reader page has nowhere to land but the admin page.
		// One that was published and taken down, and one that never was (its comment is
		// seeded by SQL, since a draft takes no comment).
		h.mustUpload(owner, "taken-down", "Taken down", "published", reportHTML(300, "down"))
		unpublished := h.say(reader, "taken-down", "written while the report was published", 0)
		unpublishedBlocked := h.say(reader, "taken-down", "written while the report was published too", 0)
		h.mustAct(owner, "taken-down", "unpublish")
		h.mustUpload(owner, "never-live", "Never live", "draft", reportHTML(300, "never"))
		h.exec(`INSERT INTO comments (report_id, github_id, body, created_at) VALUES (?, 7, 'on a draft', 1)`, h.reportOf("never-live"))
		h.exec(`INSERT INTO comments (report_id, github_id, body, created_at) VALUES (?, 7, 'on a draft too', 1)`, h.reportOf("never-live"))
		var draft, draftBlocked int64
		if err := h.store.DB().QueryRow(`SELECT id FROM comments WHERE body = 'on a draft'`).Scan(&draft); err != nil {
			t.Fatal(err)
		}
		if err := h.store.DB().QueryRow(`SELECT id FROM comments WHERE body = 'on a draft too'`).Scan(&draftBlocked); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			slug string
			id   int64
		}{{"taken-down", unpublished}, {"never-live", draft}} {
			if res := h.serve("GET", "/reports/"+c.slug); res.Status != 404 {
				t.Fatalf("control: %s answers %d to a reader", c.slug, res.Status)
			}
			res := h.removeComment(owner, itoa(c.id), "")
			if res.Status != 303 || res.Location() != "/admin/reports/"+c.slug+"#comments" || !gone(itoa(c.id)) {
				t.Errorf("a comment of %s: %d %q removed=%v", c.slug, res.Status, res.Location(), gone(itoa(c.id)))
			}
		}
		// Pressed on the Blocked users page, the same comments land back on that page:
		// from=blocked comes before the rule that sends a report with no reader page to
		// its admin page, because the Blocked users page lists comments of both.
		for _, c := range []struct {
			slug string
			id   int64
		}{{"taken-down", unpublishedBlocked}, {"never-live", draftBlocked}} {
			res := h.removeComment(owner, itoa(c.id), "from=blocked")
			if res.Status != 303 || res.Location() != "/admin/blocked" || !gone(itoa(c.id)) {
				t.Errorf("a comment of %s removed from the Blocked users page: %d %q removed=%v, want a 303 to /admin/blocked", c.slug, res.Status, res.Location(), gone(itoa(c.id)))
			}
		}
	})
}

func TestRemoveControl(t *testing.T) {
	h, owner, reader := commentWorld(t)
	other := h.mint(8, "writer-eight")
	// A thread with a placeholder that has a visible reply, and a chain deep enough
	// to need a branch page.
	top := h.say(reader, "x", "a comment from a reader", 0)
	h.say(owner, "x", "the owner answers", top)
	gone := h.say(other, "x", "a comment that will be removed", 0)
	h.say(reader, "x", "a reply that outlives it", gone)
	chain := h.chainOf("x", 7, reader, other)
	h.mustRemove(owner, gone)
	h.mustRemove(owner, chain[5]) // depth 6: a placeholder on the branch page, with a reply
	branch := "/reports/x/comments/" + itoa(chain[4])

	pages := []string{"/", "/reports/x", branch}
	for _, p := range pages {
		page := h.page(owner, p)
		arts := parseThread(t, page)
		shown, held := 0, 0
		for _, a := range arts {
			if strings.Contains(a.Own, "comment removed") {
				held++
				if strings.Contains(a.Own, "<form") || strings.Contains(a.Own, "/admin/comments/") || strings.Contains(a.Own, `class="remove"`) {
					t.Errorf("owner GET %s: the placeholder %d carries a control:\n%s", p, a.ID, a.Own)
				}
				continue
			}
			shown++
			if got := len(removeFormRE(a.ID).FindAllString(a.Own, -1)); got != 1 {
				t.Errorf("owner GET %s: comment %d has %d remove controls, want exactly one:\n%s", p, a.ID, got, a.Own)
			}
		}
		if shown == 0 || held == 0 {
			t.Errorf("owner GET %s: %d shown and %d held comments: the page tests nothing", p, shown, held)
		}
		if got := strings.Count(page, `/admin/comments/`); got != shown {
			t.Errorf("owner GET %s: %d control actions on the page, want %d (one per shown comment)", p, got, shown)
		}
	}
	// The owner's two reader pages stay the same bytes, controls included.
	home, report := h.serve("GET", "/", h.withSID(owner)), h.serve("GET", "/reports/x", h.withSID(owner))
	if d := sameResponse(home, report); d != "" {
		t.Errorf("the owner's / and /reports/x differ: %s", d)
	}
	// The control carries no field, no digit in any text or label, and no word "comment".
	for _, form := range regexpAll(`(?s)<details class="remove">.*?</details>`, h.page(owner, "/reports/x")) {
		text := regexp.MustCompile(`action="[^"]*"`).ReplaceAllString(form, "")
		if regexp.MustCompile(`\d`).MatchString(text) || strings.Contains(strings.ToLower(text), "comment") ||
			strings.Contains(text, "<input") || strings.Contains(text, "aria-label") || strings.Contains(text, "alt=") {
			t.Errorf("the control carries a digit, the word comment, a field or a label:\n%s", form)
		}
	}
	// A reader and a visitor are shown no control and no admin path, anywhere.
	for viewer, sid := range map[string]string{"signed-in reader": reader, "visitor": ""} {
		for _, p := range pages {
			page := h.page(sid, p)
			for _, s := range []string{"/admin/comments/", `class="remove"`, "Remove for everyone", "<summary>Remove</summary>"} {
				if strings.Contains(page, s) {
					t.Errorf("%s GET %s shows %q", viewer, p, s)
				}
			}
		}
	}
	// Sensitivity: the signed-in reader is offered a reply form where the owner is, so
	// the absence above is not a page that draws no comment controls at all.
	if !strings.Contains(h.page(reader, "/reports/x"), `<summary>Reply</summary>`) {
		t.Error("the reader is offered no reply form: the comparison is blind")
	}
}

func regexpAll(pattern, s string) []string {
	return regexp.MustCompile(pattern).FindAllString(s, -1)
}

func TestCommentCount(t *testing.T) {
	all := func(h *harness, owner, reader string) map[string]string {
		out := map[string]string{}
		for viewer, sid := range map[string]string{"anonymous": "", "reader": reader, "owner": owner} {
			for _, p := range []string{"/", "/reports/x"} {
				out[viewer+" GET "+p] = h.page(sid, p)
			}
		}
		return out
	}
	// want fails unless every page of every viewer prints the count and label, the
	// count is exactly what the database counts under the visibility predicate, and
	// it is the only number beside a comments label.
	want := func(t *testing.T, h *harness, owner, reader string, n int, label string) {
		t.Helper()
		if sql := h.commentCount(visibleWhere); sql != n {
			t.Fatalf("the test's own expectation %d is not what the database counts, %d", n, sql)
		}
		for name, page := range all(h, owner, reader) {
			if got, l := countOn(t, page); got != n || l != label {
				t.Errorf("%s: %d %s, want %d %s", name, got, l, n, label)
			}
			if m := regexp.MustCompile(`(?i)\b(\d+)\s*comments?\b`).FindAllStringSubmatch(page, -1); len(m) != 1 {
				t.Errorf("%s: %d numbers beside a comments label: %q, want exactly one", name, len(m), m)
			}
			if !regexp.MustCompile(`class="likes">[^<]*</p>\s*<p class="comment-count">`).MatchString(page) {
				t.Errorf("%s: the count does not follow the likes line in its own paragraph", name)
			}
		}
	}

	t.Run("0-1-many", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		want(t, h, owner, reader, 0, "comments")
		first := h.say(reader, "x", "the first comment", 0)
		want(t, h, owner, reader, 1, "comment")
		h.say(owner, "x", "a second one", first)
		h.say(reader, "x", "a third one", 0)
		want(t, h, owner, reader, 3, "comments")
		// A branch page draws part of the thread and prints no count.
		chain := h.chainOf("x", 7, reader, owner)
		branch := h.serve("GET", "/reports/x/comments/"+itoa(chain[4]), h.withSID(owner))
		if branch.Status != 200 || strings.Contains(branch.Body, "comment-count") {
			t.Errorf("the branch page: %d, count line %v, want 200 and none", branch.Status, strings.Contains(branch.Body, "comment-count"))
		}
		want(t, h, owner, reader, 10, "comments")
	})

	t.Run("placeholders", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		c1 := h.say(reader, "x", "one", 0)
		c2 := h.say(owner, "x", "two", c1)
		h.say(reader, "x", "three, a reply that stays visible", c2)
		h.say(reader, "x", "four, a top-level leaf", 0)
		want(t, h, owner, reader, 4, "comments")
		// A placeholder is not counted, and the visible reply under it still is.
		h.mustRemove(owner, c2)
		want(t, h, owner, reader, 3, "comments")
		h.mustRemove(owner, c1)
		want(t, h, owner, reader, 2, "comments")
	})

	t.Run("blocked-writer", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		blocked := h.mint(8, "blocked-writer")
		b1 := h.say(blocked, "x", "by a writer who is blocked", 0)
		h.say(reader, "x", "a visible reply under it", b1)
		h.say(blocked, "x", "another by the same writer", 0)
		h.say(reader, "x", "an ordinary comment", 0)
		want(t, h, owner, reader, 4, "comments")
		h.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (8, 5)`)
		want(t, h, owner, reader, 2, "comments")
		h.exec(`DELETE FROM blocked_accounts WHERE github_id = 8`)
		want(t, h, owner, reader, 4, "comments")
	})

	t.Run("below-the-cap", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		ids := h.chainOf("x", 8, reader, owner)
		// Three of the eight are not drawn on the report page, and all are counted.
		page := h.page("", "/reports/x")
		if got := len(parseThread(t, page)); got != threadDepthCap {
			t.Fatalf("the report page draws %d comments of the chain, want %d", got, threadDepthCap)
		}
		want(t, h, owner, reader, 8, "comments")
		h.mustRemove(owner, ids[7])
		want(t, h, owner, reader, 7, "comments")
	})

	t.Run("reconciles", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		ids := h.chainOf("x", 6, reader, owner)
		extra := h.say(reader, "x", "a top-level comment", 0)
		for step, id := range []int64{ids[2], ids[0], extra, ids[5], ids[2]} {
			h.mustRemove(owner, id)
			n := h.commentCount(visibleWhere)
			for name, page := range all(h, owner, reader) {
				if got, _ := countOn(t, page); got != n {
					t.Errorf("step %d, %s: the page counts %d, the database %d", step, name, got, n)
				}
			}
		}
	})
}

func TestAdminCommentList(t *testing.T) {
	h, owner, reader := commentWorld(t)
	other := h.mint(8, "writer-eight")
	top := h.say(reader, "x", "t1: a lone top-level comment", 0)
	chain := h.chainOf("x", 12, reader, other)
	p1 := h.say(reader, "x", "p1: removed with a reply", 0)
	p2 := h.say(other, "x", "p2: a reply under a placeholder", p1)
	leaf := h.say(other, "x", "leaf: removed with nothing under it", 0)
	mine := h.say(owner, "x", "mine: the author's own comment", 0)
	h.mustRemove(owner, p1)
	h.mustRemove(owner, leaf)

	page := h.adminPageOf(owner, "x")
	inOrder(t, "the admin page", page, `id="feedback"`, `<section class="moderation" id="comments">`, "<h2>Preview</h2>")
	section := page[strings.Index(page, `<section class="moderation"`):]
	section = section[:strings.Index(section, "</section>")]

	type item struct {
		id      int64
		replyTo string
		inForm  string
	}
	var got []item
	for _, m := range regexp.MustCompile(`(?s)<article class="mod">(.*?)</article>`).FindAllStringSubmatch(section, -1) {
		idm := regexp.MustCompile(`action="/admin/comments/(\d+)/remove"`).FindStringSubmatch(m[1])
		rep := regexp.MustCompile(`<p class="reply-to">(.*?)</p>`).FindStringSubmatch(m[1])
		if idm == nil || rep == nil {
			t.Fatalf("an item without its form or its reply-to line:\n%s", m[1])
		}
		id, _ := strconv.ParseInt(idm[1], 10, 64)
		got = append(got, item{id, rep[1], m[1]})
		if !strings.Contains(m[1], `<input type="hidden" name="from" value="admin">`) {
			t.Errorf("item %d: the form does not carry from=admin:\n%s", id, m[1])
		}
	}
	// Pre-order, every depth, no removed comment.
	wantIDs := append(append([]int64{top}, chain...), p2, mine)
	var gotIDs []int64
	for _, it := range got {
		gotIDs = append(gotIDs, it.id)
	}
	if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
		t.Fatalf("the list is %v, want %v (flat pre-order, 12 levels deep, placeholders and removed comments left out)", gotIDs, wantIDs)
	}
	replyTo := map[int64]string{top: "top level", chain[0]: "top level", chain[1]: "in reply to @reader", chain[2]: "in reply to @writer-eight",
		chain[11]: "in reply to @reader", p2: "in reply to a hidden comment", mine: "top level"}
	for _, it := range got {
		if want, ok := replyTo[it.id]; ok && it.replyTo != want {
			t.Errorf("item %d reads %q, want %q", it.id, it.replyTo, want)
		}
	}
	if !strings.Contains(got[len(got)-1].inForm, badgeMarkup) {
		t.Error("the author's own comment carries no Author badge")
	}
	for _, s := range []string{"p1: removed with a reply", "leaf: removed with nothing under it"} {
		if strings.Contains(page, s) {
			t.Errorf("the admin page shows removed text %q", s)
		}
	}
	// No ids of the reader pages, no count line, and the dislike number stays alone.
	for _, s := range []string{`id="comment-`, `id="feedback-`, "comment-count"} {
		if strings.Contains(section, s) {
			t.Errorf("the moderation section contains %q", s)
		}
	}
	if m := regexp.MustCompile(`(?i)\b(\d+)\s*comments?\b`).FindAllString(section, -1); len(m) != 0 {
		t.Errorf("numbers beside a comments label in the moderation section: %q", m)
	}
	if m := regexp.MustCompile(`(?i)\b(\d+)\s*dislikes?\b`).FindAllString(page, -1); len(m) != 1 {
		t.Errorf("numbers beside a dislike label on the admin page: %q, want just the tally", m)
	}

	// A comment 12 levels deep is removed from the admin page, and lands back there.
	res := h.removeComment(owner, itoa(chain[11]), "from=admin")
	if res.Status != 303 || res.Location() != "/admin/reports/x#comments" {
		t.Fatalf("from the admin page: %d %q", res.Status, res.Location())
	}
	if after := h.adminPageOf(owner, "x"); strings.Contains(after, `action="/admin/comments/`+itoa(chain[11])+`/remove"`) || strings.Contains(after, "level 12 of the chain") {
		t.Error("the removed comment is still listed")
	}
	if n, _ := countOn(t, h.page("", "/reports/x")); n != 14 {
		t.Errorf("public count %d after the removal, want 14 (top, 11 of the chain, p2, mine)", n)
	}

	// A draft or unpublished report lists its comments, and they can be removed there.
	h.mustUpload(owner, "drafty", "Drafty", "published", reportHTML(300, "drafty"))
	d1 := h.say(reader, "drafty", "d1: written while published", 0)
	d2 := h.say(other, "drafty", "d2: a reply on the report that became a draft", d1)
	h.mustAct(owner, "drafty", "unpublish")
	if res := h.serve("GET", "/reports/drafty"); res.Status != 404 {
		t.Fatalf("control: the unpublished report answers %d to a reader", res.Status)
	}
	draft := h.adminPageOf(owner, "drafty")
	for _, id := range []int64{d1, d2} {
		if !strings.Contains(draft, `action="/admin/comments/`+itoa(id)+`/remove"`) {
			t.Errorf("the draft's admin page does not list comment %d", id)
		}
	}
	h.mustRemoveFromAdmin(t, owner, d1, "drafty")
	after := h.adminPageOf(owner, "drafty")
	if strings.Contains(after, "d1: written while published") || !strings.Contains(after, "d2: a reply") ||
		!strings.Contains(after, "in reply to a hidden comment") {
		t.Errorf("after removing d1 from the draft's page:\n%s", after)
	}

	// Nothing visible: said so, in place of a list.
	h.mustUpload(owner, "empty", "Empty", "published", reportHTML(300, "empty"))
	if page := h.adminPageOf(owner, "empty"); !strings.Contains(page, "<p>Nothing visible on this report.</p>") || strings.Contains(page, `<article class="mod">`) {
		t.Errorf("a report with no comments:\n%s", page)
	}
}

func (h *harness) mustRemoveFromAdmin(t *testing.T, owner string, id int64, slug string) {
	t.Helper()
	res := h.removeComment(owner, itoa(id), "from=admin")
	if res.Status != 303 || res.Location() != "/admin/reports/"+slug+"#comments" {
		t.Fatalf("removing %d from the admin page of %s: %d %q", id, slug, res.Status, res.Location())
	}
}

func TestRemoveReadFailure(t *testing.T) {
	h, owner, reader := commentWorld(t)
	top := h.say(reader, "x", "a comment that is on the page", 0)
	calls := 0
	h.app.tree = func(context.Context, int64) ([]*comments.Node, error) {
		calls++
		return nil, errors.New("injected thread read failure")
	}
	// Every page that draws the thread is an outage, never "0 comments".
	for _, p := range []string{"/", "/reports/x", "/reports/x/comments/" + itoa(top)} {
		res := h.serve("GET", p, h.withSID(owner))
		if res.Status != http.StatusServiceUnavailable || strings.Contains(res.Body, "comment-count") || strings.Contains(res.Body, "0 comments") ||
			!strings.Contains(res.Body, "Reports cannot be shown right now") {
			t.Errorf("GET %s with the thread unreadable: %d, want a 503 outage page with no count", p, res.Status)
		}
	}
	// The owner's report page is an error, never an empty list.
	res := h.serve("GET", "/admin/reports/x", h.withSID(owner))
	if res.Status != http.StatusInternalServerError || strings.Contains(res.Body, "Nothing visible") || strings.Contains(res.Body, `class="moderation"`) || strings.Contains(res.Body, "<article class=\"mod\">") {
		t.Errorf("the admin page with the thread unreadable: %d, want a 500 and no list", res.Status)
	}
	if calls == 0 {
		t.Fatal("the seam was never reached")
	}
	// The removal does not read the thread, so it is not affected.
	if res := h.removeComment(owner, itoa(top), ""); res.Status != http.StatusSeeOther {
		t.Errorf("the removal with the thread unreadable: %d, want 303", res.Status)
	}
	if h.commentCount("id = ? AND removed_at IS NOT NULL", top) != 1 {
		t.Error("the removal did not happen")
	}
}

// TestThreadViewerCanRemove pins the display hint on its own, below the template
// (which draws the control only in a comment's shown branch): the owner's views
// carry CanRemove on shown comments and never on a placeholder, and nobody else's
// carry it at any depth. The hint is not the gate (TestCommentRemove/refused is).
func TestThreadViewerCanRemove(t *testing.T) {
	forest := []*comments.Node{{ID: 1, GitHubID: 7, Login: "a", Body: "b", Replies: []*comments.Node{
		{ID: 2, Placeholder: true, Replies: []*comments.Node{{ID: 3, GitHubID: 8, Login: "c", Body: "d"}}},
	}}}
	flat := func(vs []commentView) map[int64]bool {
		out := map[int64]bool{}
		var walk func([]commentView)
		walk = func(vs []commentView) {
			for _, v := range vs {
				out[v.ID] = v.CanRemove
				walk(v.Replies)
			}
		}
		walk(vs)
		return out
	}
	for _, c := range []struct {
		name string
		tv   threadViewer
		want map[int64]bool
	}{
		{"the owner", threadViewer{SignedIn: true, Owner: true}, map[int64]bool{1: true, 2: false, 3: true}},
		{"a signed-in reader", threadViewer{SignedIn: true}, map[int64]bool{1: false, 2: false, 3: false}},
		{"a visitor", threadViewer{}, map[int64]bool{1: false, 2: false, 3: false}},
	} {
		got := flat(buildThread(forest, "x", c.tv, testOwnerID, threadDepthCap))
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: CanRemove by comment = %v, want %v", c.name, got, c.want)
		}
	}
	// The same flags decide the reply form, and the two are not swapped.
	views := buildThread(forest, "x", threadViewer{SignedIn: true}, testOwnerID, threadDepthCap)
	if !views[0].CanReply || views[0].CanRemove {
		t.Errorf("a signed-in reader: CanReply %v, CanRemove %v, want true and false", views[0].CanReply, views[0].CanRemove)
	}
}
