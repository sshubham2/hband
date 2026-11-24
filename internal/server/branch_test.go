package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sshubham2/hband/internal/comments"
)

// grow stores n comments as who through the book, the first answering parent (0:
// a top-level comment) and each of the others answering the one before, and
// returns their ids. The i-th (from 1) reads "<label>-<i>-end".
func grow(t *testing.T, h *harness, slug string, who, parent int64, n int, label string) []int64 {
	t.Helper()
	rep, _, _, ok := h.reportRow(slug)
	if !ok {
		t.Fatalf("no report %s", slug)
	}
	book := h.book()
	ids := make([]int64, 0, n)
	for i := 1; i <= n; i++ {
		id, err := book.Post(bg, rep, who, parent, label+"-"+strconv.Itoa(i)+"-end")
		if err != nil {
			t.Fatalf("%s level %d: %v", label, i, err)
		}
		ids = append(ids, id)
		parent = id
	}
	return ids
}

func branchPath(slug string, id int64) string {
	return "/reports/" + slug + "/comments/" + strconv.FormatInt(id, 10)
}

func continueLink(slug string, id int64) string {
	return `<a class="continue" href="` + branchPath(slug, id) + `">continue this thread &rarr;</a>`
}

// toggleMarkup is the toggle a comment with drawn replies carries, and the
// element it controls.
func toggleMarkup(id int64) (button, replies string) {
	n := strconv.FormatInt(id, 10)
	return `<button type="button" class="toggle" hidden aria-expanded="true" aria-controls="replies-` + n + `">Hide replies</button>`,
		`<div class="replies" id="replies-` + n + `">`
}

// checkDrawn reads a page's comment articles and checks what no page may get
// wrong: nothing nests deeper than the cap; a continue link is on exactly the
// articles in cut, each pointing at its own branch page; and a comment has the
// toggle (and the element it controls) if and only if replies are drawn inside it.
// It returns the articles.
func checkDrawn(t *testing.T, what, body, slug string, cut map[int64]bool) []threadArticle {
	t.Helper()
	arts := parseThread(t, body)
	children := map[int64]int{}
	for _, a := range arts {
		if a.Parent != 0 {
			children[a.Parent]++
		}
	}
	for _, a := range arts {
		if a.Depth > threadDepthCap {
			t.Errorf("%s: comment %d is nested %d deep, the cap is %d", what, a.ID, a.Depth, threadDepthCap)
		}
		has := strings.Contains(a.Own, "continue this thread")
		switch {
		case has != cut[a.ID]:
			t.Errorf("%s: comment %d at depth %d: continue link present = %v, want %v", what, a.ID, a.Depth, has, cut[a.ID])
		case has && !strings.Contains(a.Own, continueLink(slug, a.ID)):
			t.Errorf("%s: comment %d: the link is not %s:\n%s", what, a.ID, continueLink(slug, a.ID), a.Own)
		}
		button, replies := toggleMarkup(a.ID)
		if kids := children[a.ID]; kids > 0 {
			if strings.Count(a.Own, button) != 1 || strings.Count(a.Own, replies) != 1 || strings.Index(a.Own, button) > strings.Index(a.Own, replies) {
				t.Errorf("%s: comment %d has %d replies drawn and needs the toggle, then the element it controls:\n%s", what, a.ID, kids, a.Own)
			}
		} else if strings.Contains(a.Own, `class="toggle"`) || strings.Contains(a.Own, `class="replies"`) {
			t.Errorf("%s: comment %d has no replies drawn and carries a toggle or a replies block:\n%s", what, a.ID, a.Own)
		}
	}
	if got := strings.Count(body, "continue this thread"); got != len(cut) {
		t.Errorf("%s: %d continue links, want %d", what, got, len(cut))
	}
	// Every toggle names an element that is on the page: the two counts agree.
	if strings.Count(body, `aria-controls="replies-`) != strings.Count(body, `id="replies-`) {
		t.Errorf("%s: a toggle controls an element that is not on the page", what)
	}
	return arts
}

func TestThreadCapFive(t *testing.T) {
	if threadDepthCap != 5 {
		t.Fatalf("threadDepthCap = %d, a page draws five levels", threadDepthCap)
	}
	h, _, reader := commentWorld(t)
	h.mint(9, "blocked-nine")
	rep, _, _, _ := h.reportRow("x")

	a := grow(t, h, "x", 7, 0, 12, "A") // twelve levels: cut at 5, and the branch below it is cut at 9
	b := grow(t, h, "x", 7, 0, 5, "B")  // exactly the cap: nothing below, so no link
	c := grow(t, h, "x", 7, 0, 5, "C")  // the cap, with only a hidden reply below: pruned, so no link
	// The hidden reply is written before its writer is blocked: a blocked account's
	// Post is refused, and this test is about the depth cap, not about blocking.
	if _, err := h.book().Post(bg, rep, 9, c[4], "C-hidden-6-end"); err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (9, 5)`)
	e := grow(t, h, "x", 7, 0, 5, "E") // the cap, removed, with a visible reply: a placeholder with a link
	e6 := grow(t, h, "x", 7, e[4], 1, "E6")[0]
	h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, e[4])

	for viewer, sid := range map[string]string{"anonymous": "", "reader": reader} {
		var opts []func(*http.Request)
		if sid != "" {
			opts = append(opts, h.withSID(sid))
		}
		for _, p := range []string{"/reports/x", "/"} {
			res := h.serve("GET", p, opts...)
			if res.Status != 200 {
				t.Fatalf("%s GET %s: %d", viewer, p, res.Status)
			}
			what := viewer + " GET " + p
			arts := checkDrawn(t, what, res.Body, "x", map[int64]bool{a[4]: true, e[4]: true})
			// The five levels are drawn, the sixth is not; the cap and the end of a short
			// chain are told apart.
			for i, id := range a {
				if shown := articleByID(arts, id) != nil; shown != (i < threadDepthCap) {
					t.Errorf("%s: level %d of chain A drawn = %v", what, i+1, shown)
				}
			}
			for _, id := range append(append([]int64{}, b...), c...) {
				if articleByID(arts, id) == nil {
					t.Errorf("%s: comment %d, at most at the cap, is not drawn", what, id)
				}
			}
			if strings.Contains(res.Body, "C-hidden-6-end") || strings.Contains(res.Body, "blocked-nine") {
				t.Errorf("%s: a hidden reply is drawn", what)
			}
			if ph := articleByID(arts, e[4]); ph == nil || strings.TrimSpace(strings.TrimSuffix(plain(ph.Own), "continue this thread →")) != "comment removed" ||
				!strings.Contains(ph.Own, continueLink("x", e[4])) {
				t.Errorf("%s: the removed comment at the cap is %+v, want the notice and the link", what, ph)
			}
			if articleByID(arts, e6) != nil {
				t.Errorf("%s: the reply below the cap is drawn on the report page", what)
			}
		}
	}

	// The branch pages. root is drawn at level 1, what lies below it to the cap, and
	// the comment at the cap has its own link when more lies below.
	type branch struct {
		name  string
		root  int64
		drawn []int64
		cut   int64 // 0: none
	}
	for _, br := range []branch{
		{"the cut comment of the report page", a[4], a[4:9], a[8]},
		{"the cut comment of that branch page", a[8], a[8:12], 0},
		{"a top-level comment", a[0], a[0:5], a[4]},
		{"a comment in the middle", a[1], a[1:6], a[5]},
		{"a removed comment, with its visible reply", e[4], []int64{e[4], e6}, 0},
	} {
		for viewer, sid := range map[string]string{"anonymous": "", "reader": reader} {
			var opts []func(*http.Request)
			if sid != "" {
				opts = append(opts, h.withSID(sid))
			}
			path := branchPath("x", br.root)
			res := h.serve("GET", path, opts...)
			what := viewer + " GET " + path + " (" + br.name + ")"
			if res.Status != 200 {
				t.Errorf("%s: %d", what, res.Status)
				continue
			}
			cut := map[int64]bool{}
			if br.cut != 0 {
				cut[br.cut] = true
			}
			arts := checkDrawn(t, what, res.Body, "x", cut)
			if len(arts) != len(br.drawn) {
				t.Errorf("%s: articles %v, want %v", what, articleIDs(arts), br.drawn)
				continue
			}
			for i, id := range br.drawn {
				// The root is drawn at level 1 and each comment one level below the one before:
				// the branch begins where the link was, not at the top-level comment above it.
				if arts[i].ID != id || arts[i].Depth != i+1 {
					t.Errorf("%s: article %d is %+v, want comment %d at level %d", what, i, arts[i], id, i+1)
				}
			}
			if br.root == e[4] {
				if ph := arts[0]; strings.Contains(strings.ReplaceAll(plain(ph.Own), "Hide replies", ""), "E-") || !strings.Contains(ph.Own, "comment removed") {
					t.Errorf("%s: the root reads %q", what, plain(ph.Own))
				}
			}
			for _, want := range []string{
				`<a class="back" href="/reports/x">Back to the full report</a>`,
				`<script src="/static/thread.js" defer></script>`,
				`<title>Report X - hband</title>`,
				`<h1>Report X</h1>`,
			} {
				if strings.Count(res.Body, want) != 1 {
					t.Errorf("%s: %q is not on the page exactly once", what, want)
				}
			}
			if strings.Contains(res.Body, "<iframe") || strings.Contains(res.Body, "/raw/") {
				t.Errorf("%s: a branch page draws the report", what)
			}
			if got := res.Header.Get("Cache-Control"); got != "private, no-cache" {
				t.Errorf("%s: Cache-Control = %q", what, got)
			}
			replyForms := strings.Count(res.Body, `<input type="hidden" name="parent" value="`)
			shown := 0
			for _, art := range arts {
				if !strings.Contains(art.Own, "comment removed") {
					shown++
				}
			}
			if sid != "" {
				if replyForms != shown || strings.Contains(res.Body, `rows="4"`) {
					t.Errorf("%s: %d reply forms for %d shown comments, or a form for a new top-level comment", what, replyForms, shown)
				}
			} else {
				if strings.Contains(res.Body, "<form") || strings.Contains(res.Body, "<textarea") {
					t.Errorf("%s: a visitor is offered a form", what)
				}
				if got := signInNext(t, res.Body); got != path {
					t.Errorf("%s: sign-in returns to %q, want %q", what, got, path)
				}
			}
		}
	}

	// A node that the report page has not cut is a plain thread page too: the same
	// rules, the same page. And a page that draws no cut comment shows no link.
	if res := h.serve("GET", branchPath("x", b[0])); res.Status != 200 || strings.Contains(res.Body, "continue this thread") {
		t.Errorf("a chain exactly as deep as the cap: %d, or a link where nothing is below", res.Status)
	}
}

func TestBranchNotFoundIdentical(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "y", "Report Y", "published", reportHTML(300, "y"))
	h.mustUpload(owner, "gone", "Gone", "published", reportHTML(300, "gone"))
	h.mustUpload(owner, "drafty", "Drafty", "draft", reportHTML(300, "drafty"))
	live := grow(t, h, "x", 7, 0, 7, "L")
	onY := grow(t, h, "y", 7, 0, 2, "Y")
	onGone := grow(t, h, "gone", 7, 0, 2, "G")
	// A removed top-level comment with only removed comments below: pruned.
	pruned := grow(t, h, "x", 7, 0, 3, "P")
	for _, id := range pruned {
		h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, id)
	}
	h.mustAct(owner, "y", "unpublish")
	h.mustAct(owner, "gone", "delete")

	// Controls: a visible comment of a published report is drawn.
	if res := h.serve("GET", branchPath("x", live[4])); res.Status != 200 {
		t.Fatalf("control: %d", res.Status)
	}
	baseline := h.serve("GET", branchPath("nothing-here", 1))
	if baseline.Status != 404 || baseline.Body != notFoundTxt {
		t.Fatalf("baseline: %d %q", baseline.Status, baseline.Body)
	}
	for viewer, sid := range map[string]string{"anonymous": "", "reader": reader, "owner": owner} {
		var opts []func(*http.Request)
		if sid != "" {
			opts = append(opts, h.withSID(sid))
		}
		for what, target := range map[string]string{
			"a draft report":                branchPath("drafty", live[0]),
			"an unpublished report":         branchPath("y", onY[0]),
			"a deleted report":              branchPath("gone", onGone[0]),
			"an unknown report":             branchPath("unknown-slug", live[0]),
			"an upper-case slug":            branchPath("X", live[0]),
			"an over-long slug":             branchPath(strings.Repeat("a", 81), live[0]),
			"an id that does not exist":     branchPath("x", 99999),
			"a comment of an unpublished":   branchPath("x", onY[0]),
			"a comment of a deleted report": branchPath("x", onGone[0]),
			"a pruned comment":              branchPath("x", pruned[0]),
			"a pruned comment deeper down":  branchPath("x", pruned[2]),
			"letters":                       "/reports/x/comments/abc",
			"a leading zero":                "/reports/x/comments/0" + strconv.FormatInt(live[0], 10),
			"zero":                          "/reports/x/comments/0",
			"a minus sign":                  "/reports/x/comments/-" + strconv.FormatInt(live[0], 10),
			"a plus sign":                   "/reports/x/comments/%2B" + strconv.FormatInt(live[0], 10),
			"a decimal point":               "/reports/x/comments/1.0",
			"an exponent":                   "/reports/x/comments/1e3",
			"too large for an id":           "/reports/x/comments/99999999999999999999",
			"a space":                       "/reports/x/comments/%20" + strconv.FormatInt(live[0], 10),
		} {
			res := h.serve("GET", target, opts...)
			if d := sameResponse(res, baseline); d != "" {
				t.Errorf("%s, %s (GET %s): not the same http.NotFound as an unknown report: %s", viewer, what, target, d)
			}
		}
	}
}

func TestBranchStoreFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(h *harness)
		works string // a table the page's other lookups need, which must still read
	}{
		{"the comments read fails while the report lookup works", func(h *harness) { h.exec(`ALTER TABLE comments RENAME TO comments_gone`) }, "reports"},
		{"the report lookup fails while the comments read works", func(h *harness) { h.exec(`ALTER TABLE reports RENAME TO reports_gone`) }, "comments"},
		{"only the thread read after the lookup fails", func(h *harness) {
			h.app.tree = func(context.Context, int64) ([]*comments.Node, error) {
				return nil, errors.New("injected thread read failure")
			}
		}, "comments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, owner, reader := commentWorld(t)
			ids := grow(t, h, "x", 7, 0, 7, "L")
			path := branchPath("x", ids[4])
			if res := h.serve("GET", path); res.Status != 200 || !strings.Contains(res.Body, "L-5-end") {
				t.Fatalf("control: %d", res.Status)
			}
			tc.fault(h)
			if _, err := h.store.DB().Exec(`SELECT count(*) FROM ` + tc.works); err != nil {
				t.Fatalf("the fault is not isolated, %s must still read: %v", tc.works, err)
			}
			for viewer, sid := range map[string]string{"anonymous": "", "reader": reader, "owner": owner} {
				var opts []func(*http.Request)
				if sid != "" {
					opts = append(opts, h.withSID(sid))
				}
				res := h.serve("GET", path, opts...)
				if res.Status != 503 || !strings.Contains(res.Body, "Reports cannot be shown right now") {
					t.Errorf("%s: GET %s = %d, want the 503 page (an outage is not an absence):\n%s", viewer, path, res.Status, res.Body)
				}
				if strings.Contains(res.Body, "L-5-end") || strings.Contains(res.Body, "<iframe") || strings.Contains(res.Body, "<textarea") {
					t.Errorf("%s: the 503 page draws a thread or a report:\n%s", viewer, res.Body)
				}
				if got := res.Header.Get("Cache-Control"); got != "private, no-cache" {
					t.Errorf("%s: Cache-Control = %q", viewer, got)
				}
			}
		})
	}
}

// landing parses a 303's Location into the path and the comment id its fragment
// names, and fails unless the fragment is exactly comment-<id>.
func landing(t *testing.T, res result) (path string, id int64) {
	t.Helper()
	if res.Status != http.StatusSeeOther {
		t.Fatalf("answered %d %q, want 303", res.Status, res.Body)
	}
	u, err := url.Parse(res.Location())
	if err != nil {
		t.Fatalf("Location %q: %v", res.Location(), err)
	}
	m := regexp.MustCompile(`^comment-([1-9][0-9]*)$`).FindStringSubmatch(u.Fragment)
	if m == nil {
		t.Fatalf("Location %q: the fragment is not exactly comment-<id>", res.Location())
	}
	id, _ = strconv.ParseInt(m[1], 10, 64)
	return u.Path, id
}

// TestReplyRedirectTable is the m8 test: a reply stored at any depth is answered
// with a 303 to a page that draws it, at its own anchor. The oracle is the pages,
// not the formula in branchRoot: for every depth the reply is posted through the
// real handler, the Location is parsed, and a GET of it must draw the comment, as
// a comment with its own text and not as a notice.
func TestReplyRedirectTable(t *testing.T) {
	branchRE := regexp.MustCompile(`^/reports/x/comments/([1-9][0-9]*)$`)
	drawn := func(t *testing.T, h *harness, path string, id int64, body string, sid string) {
		t.Helper()
		var opts []func(*http.Request)
		if sid != "" {
			opts = append(opts, h.withSID(sid))
		}
		res := h.serve("GET", path, opts...)
		if res.Status != 200 {
			t.Errorf("GET %s answered %d", path, res.Status)
			return
		}
		art := articleByID(parseThread(t, res.Body), id)
		switch {
		case art == nil:
			t.Errorf("GET %s does not draw comment %d at all", path, id)
		case strings.Contains(art.Own, "comment removed") || !strings.Contains(plain(art.Own), body):
			t.Errorf("GET %s draws comment %d as %q, want its own text %q", path, id, plain(art.Own), body)
		}
	}

	t.Run("a reply at every depth lands on the page that draws it", func(t *testing.T) {
		h, _, reader := commentWorld(t)
		var ids []int64
		for d := 1; d <= 3*threadDepthCap+2; d++ {
			var parent int64
			if d > 1 {
				parent = ids[d-2]
			}
			body := fmt.Sprintf("redirect-oracle-%d-end", d)
			path, id := landing(t, h.postComment(reader, "x", commentForm(body, parent)))
			ids = append(ids, id)
			if d <= threadDepthCap {
				if path != "/reports/x" {
					t.Errorf("depth %d: sent to %s, want the report page", d, path)
				}
			} else if m := branchRE.FindStringSubmatch(path); m == nil {
				t.Errorf("depth %d: sent to %s, want a branch page", d, path)
			} else if n, _ := strconv.ParseInt(m[1], 10, 64); !containsID(ids[:d-1], n) {
				t.Errorf("depth %d: the branch page is rooted at %d, which is not above the comment in its chain %v", d, n, ids[:d-1])
			}
			drawn(t, h, path, id, body, reader)
			drawn(t, h, path, id, body, "")
		}
	})

	t.Run("a branch page that begins at a removed comment", func(t *testing.T) {
		h, _, reader := commentWorld(t)
		chain := grow(t, h, "x", 7, 0, 3*threadDepthCap-3, "R")
		roots := []int{threadDepthCap, 2*threadDepthCap - 1} // where the first two branch pages begin
		for _, d := range roots {
			h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, chain[d-1])
		}
		// One reply to the comment below each removed root, so each lands on the branch
		// page whose root is the notice.
		for i, d := range roots {
			body := fmt.Sprintf("redirect-below-the-removed-%d-end", i)
			path, id := landing(t, h.postComment(reader, "x", commentForm(body, chain[d])))
			if want := branchPath("x", chain[d-1]); path != want {
				t.Errorf("a reply below the removed comment at depth %d: sent to %s, want %s", d, path, want)
			}
			drawn(t, h, path, id, body, reader)
			drawn(t, h, path, id, body, "")
		}
	})

	t.Run("a blocked account's comment is refused at any depth and stored nowhere", func(t *testing.T) {
		// A blocked account's comment is refused, so no page and no row has it: a 403 and
		// no Location, whether the comment is top level, a reply to the first comment of a
		// chain or a reply far below the depth cap (it used to be stored and never drawn).
		h, _, _ := commentWorld(t)
		blocked := h.mint(9, "blocked-nine")
		h.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (9, 5)`)
		chain := grow(t, h, "x", 7, 0, threadDepthCap+3, "V")
		before := h.commentCount("1 = 1")
		for _, parent := range []int64{0, chain[0], chain[len(chain)-1]} {
			res := h.postComment(blocked, "x", commentForm("written by a blocked account", parent))
			if res.Status != http.StatusForbidden || res.Location() != "" || !strings.Contains(res.Body, blockedRefusal) {
				t.Errorf("a blocked account's reply to %d: %d %q (Location %q), want a 403 with %q", parent, res.Status, res.Body, res.Location(), blockedRefusal)
			}
		}
		if after := h.commentCount("1 = 1"); after != before {
			t.Errorf("%d comments stored, want %d: a blocked account's comment was kept", after, before)
		}
	})
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// TestReplyRedirectRereadFails: the write has happened by the time the thread is
// read to choose the page, so a read that fails there must not turn the answer
// into an error. The double fails only the read that follows a stored comment;
// every other lookup works, so nothing earlier can mask it (see
// TestStoreFaultSteps for the rest of the class).
func TestReplyRedirectRereadFails(t *testing.T) {
	h, _, reader := commentWorld(t)
	ids := grow(t, h, "x", 7, 0, threadDepthCap+2, "D")
	before := 0
	real := h.app.tree
	reads, failed := 0, 0
	h.app.tree = func(ctx context.Context, id int64) ([]*comments.Node, error) {
		reads++
		if h.commentCount("1 = 1") > before {
			failed++
			return nil, errors.New("injected: the thread read after the write fails")
		}
		return real(ctx, id)
	}
	for i, parent := range []int64{0, ids[0], ids[len(ids)-1]} { // a top-level, a shallow and a deep reply
		before = h.commentCount("1 = 1")
		body := fmt.Sprintf("stored-while-the-reread-fails-%d", i)
		res := h.postComment(reader, "x", commentForm(body, parent))
		if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#comments" || res.Body != "" {
			t.Errorf("reply to %d: %d %q %q, want 303 /reports/x#comments and no body", parent, res.Status, res.Location(), res.Body)
		}
		if n := h.commentCount("body = ?", body); n != 1 {
			t.Errorf("reply to %d: %d rows stored, want 1", parent, n)
		}
	}
	if reads == 0 || failed != 3 {
		t.Errorf("the double was reached %d times and failed %d, want it to fail exactly the three reads after a write", reads, failed)
	}
}

// TestStaticThreadScript: the site's own script is served from the site's own
// path and loaded by the pages that draw a thread, and the isolated report path
// still forbids every script.
func TestStaticThreadScript(t *testing.T) {
	h, _, reader := commentWorld(t)
	ids := grow(t, h, "x", 7, 0, threadDepthCap+1, "S")

	res := h.serve("GET", "/static/thread.js")
	if res.Status != 200 || !strings.Contains(res.Body, "aria-controls") || !strings.Contains(res.Body, "addEventListener") {
		t.Fatalf("GET /static/thread.js: %d, %d bytes", res.Status, len(res.Body))
	}
	if got := res.Header.Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'self'") || strings.Contains(got, "unsafe-") {
		t.Errorf("a site page's policy is %q, want script-src 'self' and no unsafe-*", got)
	}
	const tag = `<script src="/static/thread.js" defer></script>`
	for name, target := range map[string]string{"/": "/", "/reports/x": "/reports/x", "a branch page": branchPath("x", ids[4])} {
		for viewer, opts := range map[string][]func(*http.Request){"anonymous": nil, "reader": {h.withSID(reader)}} {
			page := h.serve("GET", target, opts...)
			if page.Status != 200 || strings.Count(page.Body, tag) != 1 || strings.Count(page.Body, "<script") != 1 {
				t.Errorf("%s GET %s: %d, the page does not load exactly the one site script, with defer", viewer, name, page.Status)
			}
			if got := page.Header.Get("Content-Security-Policy"); got != res.Header.Get("Content-Security-Policy") {
				t.Errorf("%s GET %s: policy %q", viewer, name, got)
			}
		}
	}
	// A page with no report draws no script.
	empty := newHarness(t, false)
	if page := empty.serve("GET", "/"); page.Status != 200 || strings.Contains(page.Body, "<script") {
		t.Errorf("the empty home page: %d, or it loads a script", page.Status)
	}

	raw := h.serve("GET", "/raw/x")
	want := (&app{cfg: h.cfg}).rawCSP()
	if raw.Status != 200 || raw.Header.Get("Content-Security-Policy") != want || !strings.Contains(want, "script-src 'none'") {
		t.Errorf("GET /raw/x: %d, policy %q, want %q with script-src 'none'", raw.Status, raw.Header.Get("Content-Security-Policy"), want)
	}
	if strings.Contains(raw.Body, "thread.js") {
		t.Error("the isolated report path carries the site script")
	}
}
