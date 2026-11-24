package server

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/auth"
	"github.com/sshubham2/hband/internal/comments"
	"github.com/sshubham2/hband/internal/reports"
	"github.com/sshubham2/hband/internal/store"
)

// postComment sends the comment form for slug with a raw urlencoded body. sid ""
// is a visitor with no session.
func (h *harness) postComment(sid, slug, body string, extra ...func(*http.Request)) result {
	h.t.Helper()
	opts := []func(*http.Request){withBody(formType, []byte(body))}
	if sid != "" {
		opts = append(opts, h.withSID(sid))
	}
	return h.serve("POST", "/reports/"+slug+"/comments", append(opts, extra...)...)
}

// commentForm is the urlencoded form of a body and, when parent is not 0, a parent.
func commentForm(text string, parent int64) string {
	v := url.Values{"body": {text}}
	if parent != 0 {
		v.Set("parent", strconv.FormatInt(parent, 10))
	}
	return v.Encode()
}

// say posts a comment as sid, answering parent (0: top level), and fails unless
// it is answered with the 303 to its own anchor. It returns the new id.
func (h *harness) say(sid, slug, text string, parent int64) int64 {
	h.t.Helper()
	res := h.postComment(sid, slug, commentForm(text, parent))
	loc := res.Location()
	const anchor = "#comment-"
	i := strings.Index(loc, anchor)
	// A reply deeper than the report page draws is answered with the branch page
	// that draws it (TestReplyRedirectTable pins which); here only the anchor counts.
	if res.Status != http.StatusSeeOther || !strings.HasPrefix(loc, "/reports/"+slug) || i < 0 {
		h.t.Fatalf("comment %q on %s (parent %d): %d %q %q", text, slug, parent, res.Status, loc, res.Body)
	}
	id, err := strconv.ParseInt(loc[i+len(anchor):], 10, 64)
	if err != nil {
		h.t.Fatalf("comment anchor in %q: %v", loc, err)
	}
	return id
}

func (h *harness) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.store.DB().Exec(q, args...); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
}

func (h *harness) commentCount(where string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.store.DB().QueryRow("SELECT count(*) FROM comments WHERE "+where, args...).Scan(&n); err != nil {
		h.t.Fatalf("count comments: %v", err)
	}
	return n
}

// book is a comment book on the harness's store and clock, for seeding.
func (h *harness) book() *comments.Book { return comments.New(h.store.DB(), h.clock.Now) }

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// threadArticle is one <article id="comment-N"> of a page.
type threadArticle struct {
	ID     int64
	Parent int64 // the comment it sits in; 0 at the top level
	Depth  int   // 1 at the top level
	Own    string
}

var articleTokenRE = regexp.MustCompile(`<article class="comment" id="comment-(\d+)">|</article>`)

// parseThread reads the comment articles of a page with a token stack. Own is
// the markup that belongs to the article itself: everything between its
// boundaries except what lies inside the articles nested in it. It fails the
// test when the articles are not balanced, which is how a body that closes an
// element early would show.
func parseThread(t *testing.T, page string) []threadArticle {
	t.Helper()
	var out []threadArticle
	var stack []int
	owns := map[int]*strings.Builder{}
	last := 0
	for _, loc := range articleTokenRE.FindAllStringSubmatchIndex(page, -1) {
		if len(stack) > 0 {
			owns[stack[len(stack)-1]].WriteString(page[last:loc[0]])
		}
		last = loc[1]
		if loc[2] >= 0 {
			id, _ := strconv.ParseInt(page[loc[2]:loc[3]], 10, 64)
			a := threadArticle{ID: id, Depth: len(stack) + 1}
			if len(stack) > 0 {
				a.Parent = out[stack[len(stack)-1]].ID
			}
			out = append(out, a)
			owns[len(out)-1] = &strings.Builder{}
			stack = append(stack, len(out)-1)
			continue
		}
		if len(stack) == 0 {
			t.Fatalf("a closing </article> with no open comment, in:\n%s", page)
		}
		stack = stack[:len(stack)-1]
	}
	if len(stack) != 0 {
		t.Fatalf("%d comment articles are never closed, in:\n%s", len(stack), page)
	}
	for i := range out {
		out[i].Own = owns[i].String()
	}
	return out
}

func articleByID(arts []threadArticle, id int64) *threadArticle {
	for i := range arts {
		if arts[i].ID == id {
			return &arts[i]
		}
	}
	return nil
}

func articleIDs(arts []threadArticle) []int64 {
	ids := make([]int64, 0, len(arts))
	for _, a := range arts {
		ids = append(ids, a.ID)
	}
	return ids
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// plain is the text a person reads in a piece of markup.
func plain(markup string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRE.ReplaceAllString(markup, " "))), " ")
}

const (
	badgeMarkup = `<span class="badge">Author</span>`
	notFoundTxt = "404 page not found\n"
)

var authorWordRE = regexp.MustCompile(`(?i)\bauthor\b`)

// commentWorld is a published report x, the owner and one signed-in reader.
func commentWorld(t *testing.T) (h *harness, owner, reader string) {
	t.Helper()
	h = newHarness(t, false)
	owner = h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	reader = h.mint(7, "reader")
	return h, owner, reader
}

func TestCommentPostFlow(t *testing.T) {
	h, owner, _ := commentWorld(t)
	const avatar = "https://avatars.githubusercontent.com/u/7?v=4"
	reader := h.mintUser(store.User{GitHubID: 7, Login: "reader", AvatarURL: avatar})

	page := h.serve("GET", "/reports/x", h.withSID(reader))
	if !strings.Contains(page.Body, `<form method="post" action="/reports/x/comments">`) ||
		!strings.Contains(page.Body, `<textarea name="body"`) || !strings.Contains(page.Body, "No comments yet.") {
		t.Fatalf("a signed-in reader is offered no comment form, or the empty thread is not said:\n%s", page.Body)
	}

	res := h.postComment(reader, "x", commentForm("Hello there", 0))
	if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#comment-1" || res.Body != "" {
		t.Fatalf("the first comment: %d %q %q, want a 303 to /reports/x#comment-1", res.Status, res.Location(), res.Body)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var reportID, writer int64
	var parent *int64
	var body string
	if err := h.store.DB().QueryRow(`SELECT report_id, github_id, parent_id, body FROM comments WHERE id = 1`).Scan(&reportID, &writer, &parent, &body); err != nil {
		t.Fatal(err)
	}
	if id, _, _, _ := h.reportRow("x"); reportID != id || writer != 7 || parent != nil || body != "Hello there" {
		t.Errorf("stored (report %d, by %d, parent %v, %q)", reportID, writer, parent, body)
	}

	// The owner replies through the same form.
	res = h.postComment(owner, "x", commentForm("Thanks for reading", 1))
	if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#comment-2" {
		t.Fatalf("the owner's reply: %d %q", res.Status, res.Location())
	}
	if h.commentCount("id = 2 AND parent_id = 1 AND github_id = ?", testOwnerID) != 1 {
		t.Error("the reply is not stored under comment 1 by the owner")
	}

	// The thread is public, on both pages, with the writer's login and avatar.
	for viewer, opts := range map[string][]func(*http.Request){"anonymous": nil, "reader": {h.withSID(reader)}} {
		for _, p := range []string{"/reports/x", "/"} {
			got := h.serve("GET", p, opts...)
			arts := parseThread(t, got.Body)
			if len(arts) != 2 || arts[0].ID != 1 || arts[0].Depth != 1 || arts[1].ID != 2 || arts[1].Parent != 1 || arts[1].Depth != 2 {
				t.Fatalf("%s GET %s: articles %+v, want comment 1 with comment 2 inside it", viewer, p, arts)
			}
			first, second := arts[0].Own, arts[1].Own
			if !strings.Contains(first, `<span class="login">reader</span>`) || !strings.Contains(first, `src="`+avatar+`"`) ||
				!strings.Contains(first, `<p class="body">Hello there</p>`) {
				t.Errorf("%s GET %s: comment 1 lacks the login, the avatar or the text:\n%s", viewer, p, first)
			}
			if !strings.Contains(second, `<span class="login">the-owner</span>`) || !strings.Contains(second, badgeMarkup) ||
				strings.Contains(second, "<img") {
				t.Errorf("%s GET %s: the owner's reply has no login, or no badge, or an avatar it does not have:\n%s", viewer, p, second)
			}
			if strings.Contains(first, badgeMarkup) {
				t.Errorf("%s GET %s: a reader's comment carries the badge", viewer, p)
			}
			if got.Header.Get("Cache-Control") != "private, no-cache" {
				t.Errorf("%s GET %s: Cache-Control = %q", viewer, p, got.Header.Get("Cache-Control"))
			}
		}
	}
	// Only a signed-in reader is offered a form, and the reply form names its parent.
	anon := h.serve("GET", "/reports/x")
	if strings.Contains(anon.Body, "<form") || strings.Contains(anon.Body, "<textarea") || !strings.Contains(anon.Body, "Sign in to comment.") {
		t.Errorf("a visitor is offered a form, or no hint:\n%s", anon.Body)
	}
	if !strings.Contains(h.serve("GET", "/reports/x", h.withSID(reader)).Body, `<input type="hidden" name="parent" value="2">`) {
		t.Error("the reply form of comment 2 does not carry its parent")
	}
}

func TestCommentSignedOutStoresNothing(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "drafty", "Drafty", "draft", reportHTML(300, "drafty"))
	first := h.say(reader, "x", "a comment to answer", 0)
	before := h.commentCount("1 = 1")

	expired := h.mint(8, "reader8")
	h.clock.Advance(31 * 24 * time.Hour) // past the 30-day session
	unknown := strings.Repeat("A", 43)   // grammar-valid, no such session
	huge := "body=" + strings.Repeat("a", 70<<10)
	for name, sid := range map[string]string{
		"no session": "", "an expired session": expired, "an unknown session": unknown, "a malformed cookie": "garbage",
	} {
		for what, body := range map[string]string{
			"a top-level comment": commentForm("hello", 0),
			"a reply":             commentForm("hello", first),
			// Nothing is parsed before the visitor is known: a form that a signed-in
			// reader would be refused is answered the same way here.
			"an empty body":     "body=",
			"no form at all":    "",
			"a body over 64KiB": huge,
		} {
			res := h.postComment(sid, "x", body)
			if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#comments" || res.Body != "" {
				t.Errorf("%s, %s: %d %q %q, want 303 to /reports/x#comments", name, what, res.Status, res.Location(), res.Body)
			}
			if res.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s, %s: Cache-Control %q", name, what, res.Header.Get("Cache-Control"))
			}
		}
	}
	if n := h.commentCount("1 = 1"); n != before {
		t.Fatalf("%d comments written for visitors who are not signed in, want %d", n-before, 0)
	}

	// A draft and an unknown slug are the same http.NotFound for everybody.
	baseline := h.postComment("", "unknown-slug", commentForm("hello", 0))
	if baseline.Status != 404 || baseline.Body != notFoundTxt {
		t.Fatalf("baseline: %d %q", baseline.Status, baseline.Body)
	}
	live, liveOwner := h.mint(9, "reader9"), h.mint(testOwnerID, "the-owner")
	for viewer, sid := range map[string]string{"anonymous": "", "reader": live, "owner": liveOwner} {
		for _, slug := range []string{"drafty", "unknown-slug", "UPPER-CASE", strings.Repeat("a", 81)} {
			for _, body := range []string{commentForm("hello", 0), commentForm("hello", first), "", "body="} {
				if d := sameResponse(h.postComment(sid, slug, body), baseline); d != "" {
					t.Errorf("%s: POST /reports/%s/comments %q differs from an unknown slug: %s", viewer, slug, body, d)
				}
			}
		}
	}

	// A signed-in reader's POST from another site stops at cross-origin
	// protection, before the handler.
	for _, hdr := range [][2]string{{"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}, {"Origin", "https://evil.example"}} {
		for _, body := range []string{commentForm("hello", 0), commentForm("hello", first)} {
			if res := h.postComment(live, "x", body, withHeader(hdr[0], hdr[1])); res.Status != http.StatusForbidden {
				t.Errorf("%s: %s: %d, want 403", hdr[0], hdr[1], res.Status)
			}
		}
	}
	if n := h.commentCount("1 = 1"); n != before {
		t.Fatalf("%d comments written by requests from another site", n-before)
	}

	// Controls: the site's own forms carry these headers and are taken.
	for _, hdr := range [][2]string{{"Sec-Fetch-Site", "same-origin"}, {"Origin", h.cfg.Origin}} {
		if res := h.postComment(live, "x", commentForm("hello", 0), withHeader(hdr[0], hdr[1])); res.Status != http.StatusSeeOther {
			t.Errorf("control %s: %s: %d", hdr[0], hdr[1], res.Status)
		}
	}
	if n := h.commentCount("1 = 1"); n != before+2 {
		t.Fatalf("control: %d comments, want %d", n, before+2)
	}
}

func TestCommentLiteralHTML(t *testing.T) {
	h, _, reader := commentWorld(t)
	typed := "<b>bold</b> <script>window.hbandRan=1</script> <img src=x onerror=\"alert(1)\"> & done \"quoted\" 'single' </article> {{.}} <!-- c -->"
	id := h.say(reader, "x", typed, 0)
	h.say(reader, "x", "a second comment after the hostile one", 0)

	for _, p := range []string{"/reports/x", "/"} {
		for viewer, opts := range map[string][]func(*http.Request){"anonymous": nil, "reader": {h.withSID(reader)}} {
			got := h.serve("GET", p, opts...)
			arts := parseThread(t, got.Body) // fails when </article> in the body closed the element
			if len(arts) != 2 || arts[0].ID != id || arts[0].Depth != 1 || arts[1].Depth != 1 {
				t.Fatalf("%s GET %s: articles %+v, want two top-level comments", viewer, p, arts)
			}
			m := regexp.MustCompile(`(?s)<p class="body">(.*?)</p>`).FindStringSubmatch(arts[0].Own)
			if m == nil {
				t.Fatalf("%s GET %s: no body paragraph:\n%s", viewer, p, arts[0].Own)
			}
			if html.UnescapeString(m[1]) != typed {
				t.Errorf("%s GET %s: the body shows as %q, want the typed text", viewer, p, html.UnescapeString(m[1]))
			}
			if strings.ContainsAny(m[1], "<>") || !strings.Contains(m[1], "&lt;b&gt;bold&lt;/b&gt;") || !strings.Contains(m[1], "{{.}}") {
				t.Errorf("%s GET %s: the body is not escaped text: %s", viewer, p, m[1])
			}
			// The one script on the page is the site's own, loaded by its path; the typed
			// one is literal text.
			page := strings.Replace(got.Body, `<script src="/static/thread.js" defer></script>`, "", 1)
			for _, live := range []string{"<b>", "<script", "<img src=x", "onerror=\"", "<!--"} {
				if strings.Contains(page, live) {
					t.Errorf("%s GET %s: the page holds live markup %q:\n%s", viewer, p, live, got.Body)
				}
			}
			if strings.Count(got.Body, "<article") != strings.Count(got.Body, "</article>") {
				t.Errorf("%s GET %s: the articles are not balanced", viewer, p)
			}
		}
	}
	var stored string
	if err := h.store.DB().QueryRow(`SELECT body FROM comments WHERE id = ?`, id).Scan(&stored); err != nil || stored != typed {
		t.Errorf("stored %q, %v: the text must be stored exactly as typed", stored, err)
	}
}

func TestCommentBadBody(t *testing.T) {
	h, _, reader := commentWorld(t)
	enc := url.QueryEscape
	rn := func(s string, n int) string { return strings.Repeat(s, n) }
	bad := []struct{ name, body string }{
		{"no body field", "parent="},
		{"another field only", "text=hello"},
		{"empty", "body="},
		{"white space only", "body=" + enc("  \n\t ")},
		{"a no-break space only", "body=" + enc("\u00a0")},
		{"5001 runes", "body=" + rn("a", 5001)},
		{"5001 four-byte runes", "body=" + enc(rn("😀", 5001))},
		{"invalid UTF-8", "body=a%FFb"},
		{"a truncated rune", "body=ab%C3"},
		{"NUL", "body=a%00b"},
		{"a C0 control", "body=a%01b"},
		{"escape", "body=a%1Bb"},
		{"DEL", "body=a%7Fb"},
		{"U+0085", "body=a%C2%85b"},
		{"U+009F", "body=a%C2%9Fb"},
		{"repeated", "body=one&body=two"},
		{"repeated alike", "body=one&body=one"},
		{"over 64 KiB", "body=" + rn("a", 64<<10)},
		{"far over 64 KiB", "body=" + rn("a", 1<<20)},
	}
	for _, c := range bad {
		res := h.postComment(reader, "x", c.body)
		if res.Status != http.StatusBadRequest || res.Location() != "" {
			t.Errorf("%s: %d %q, want 400 and no redirect", c.name, res.Status, res.Location())
		}
	}
	for name, res := range map[string]result{
		"only in the query string": h.serve("POST", "/reports/x/comments?body=hello", h.withSID(reader), withBody(formType, nil)),
		"query string and no body": h.serve("POST", "/reports/x/comments?body=hello", h.withSID(reader)),
		"not a form":               h.serve("POST", "/reports/x/comments", h.withSID(reader), withBody("application/json", []byte(`{"body":"hello"}`))),
		"plain text":               h.serve("POST", "/reports/x/comments", h.withSID(reader), withBody("text/plain", []byte("body=hello"))),
		"multipart":                h.serve("POST", "/reports/x/comments", h.withSID(reader), withBody("multipart/form-data; boundary=b", []byte("--b\r\nContent-Disposition: form-data; name=\"body\"\r\n\r\nhello\r\n--b--\r\n"))),
	} {
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, res.Status)
		}
	}
	if n := h.commentCount("1 = 1"); n != 0 {
		t.Fatalf("%d comments written by forms that were refused", n)
	}

	// Controls: the limits are exact, and the text is stored as normalised.
	for _, c := range []struct{ name, body, want string }{
		{"exactly 5000 runes", "body=" + rn("a", 5000), rn("a", 5000)},
		{"exactly 5000 four-byte runes", "body=" + enc(rn("😀", 5000)), rn("😀", 5000)},
		{"CRLF is stored as LF", "body=a%0D%0Ab", "a\nb"},
		{"a lone CR is stored as LF", "body=a%0Db", "a\nb"},
		{"tab and newline inside are kept", "body=" + enc("a\tb\nc"), "a\tb\nc"},
		{"the ends are trimmed", "body=" + enc("  hi  "), "hi"},
	} {
		res := h.postComment(reader, "x", c.body)
		if res.Status != http.StatusSeeOther {
			t.Errorf("%s: %d, want 303", c.name, res.Status)
			continue
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(res.Location(), "/reports/x#comment-"), 10, 64)
		var got string
		if err := h.store.DB().QueryRow(`SELECT body FROM comments WHERE id = ?`, id).Scan(&got); err != nil || got != c.want {
			t.Errorf("%s: stored %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if n := h.commentCount("1 = 1"); n != 6 {
		t.Errorf("%d comments after the controls, want 6", n)
	}
}

func TestCommentParentRefused(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "y", "Report Y", "published", reportHTML(300, "y"))
	blocked := h.mint(9, "blocked-nine")
	good := h.say(reader, "x", "an ordinary comment", 0)
	onY := h.say(reader, "y", "a comment on another report", 0)
	removed := h.say(reader, "x", "a comment that will be removed", 0)
	byBlocked := h.say(blocked, "x", "a comment by an account that will be blocked", 0)
	h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, removed)
	h.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (9, 5)`)
	before := h.commentCount("1 = 1")

	baseline := h.postComment("", "unknown-slug", commentForm("hello", 0))
	for name, parent := range map[string]int64{
		"a missing id": 9999, "a comment on another report": onY, "a removed comment": removed, "a comment by a blocked account": byBlocked,
	} {
		for viewer, sid := range map[string]string{"reader": reader, "owner": owner} {
			res := h.postComment(sid, "x", commentForm("a reply", parent))
			if d := sameResponse(res, result{Status: 404, Header: baseline.Header, Body: notFoundTxt}); d != "" {
				t.Errorf("%s replying to %s: %d %q, want the http.NotFound answer: %s", viewer, name, res.Status, res.Body, d)
			}
		}
	}
	if n := h.commentCount("1 = 1"); n != before {
		t.Fatalf("%d comments written by replies that were refused", n-before)
	}
	// Control: the same reply to a usable parent is taken, and after the block is
	// lifted the blocked account's comment can be answered again.
	h.say(reader, "x", "a reply", good)
	h.exec(`DELETE FROM blocked_accounts WHERE github_id = 9`)
	h.say(reader, "x", "a reply to the unblocked account", byBlocked)
	if n := h.commentCount("1 = 1"); n != before+2 {
		t.Fatalf("control: %d comments, want %d", n, before+2)
	}
}

func TestCommentParentField(t *testing.T) {
	h, _, reader := commentWorld(t)
	first := h.say(reader, "x", "a comment to answer", 0)
	before := h.commentCount("1 = 1")
	if first != 1 {
		t.Fatalf("the first comment has id %d, the table below assumes 1", first)
	}

	for _, c := range []struct{ name, body string }{
		{"letters", "body=hi&parent=abc"},
		{"zero", "body=hi&parent=0"},
		{"negative", "body=hi&parent=-1"},
		{"a plus sign", "body=hi&parent=%2B1"},
		{"a leading zero", "body=hi&parent=01"},
		{"two leading zeros", "body=hi&parent=007"},
		{"present but empty", "body=hi&parent="},
		{"repeated", "body=hi&parent=1&parent=1"},
		{"repeated and different", "body=hi&parent=1&parent=2"},
		{"an exponent", "body=hi&parent=1e3"},
		{"a leading space", "body=hi&parent=%201"},
		{"a trailing space", "body=hi&parent=1%20"},
		{"a decimal point", "body=hi&parent=1.0"},
		{"too large for an id", "body=hi&parent=99999999999999999999"},
		{"hexadecimal", "body=hi&parent=0x1"},
	} {
		res := h.postComment(reader, "x", c.body)
		if res.Status != http.StatusBadRequest || res.Location() != "" {
			t.Errorf("%s: %d %q, want 400", c.name, res.Status, res.Location())
		}
	}
	if n := h.commentCount("1 = 1"); n != before {
		t.Fatalf("%d comments written by malformed parents", n-before)
	}

	// A parent sent only in the query string is ignored, as the vote handler
	// ignores r.Form: the comment is a top-level one.
	res := h.serve("POST", "/reports/x/comments?parent=1", h.withSID(reader), withBody(formType, []byte("body=top+level")))
	if res.Status != http.StatusSeeOther {
		t.Fatalf("a parent only in the query string: %d", res.Status)
	}
	if n := h.commentCount("body = 'top level' AND parent_id IS NULL"); n != 1 {
		t.Errorf("the query-string parent was used: %d top-level rows with that body", n)
	}
	// Control: the canonical form is taken, and is a reply.
	h.say(reader, "x", "a real reply", 1)
	if n := h.commentCount("body = 'a real reply' AND parent_id = 1"); n != 1 {
		t.Errorf("the canonical parent was not used")
	}
}

// chainThread posts n comments as who, each a reply to the one before, with the
// text "level-<i>-end", and returns their ids.
func chainThread(t *testing.T, h *harness, slug string, who int64, n int) []int64 {
	t.Helper()
	rep, _, _, ok := h.reportRow(slug)
	if !ok {
		t.Fatalf("no report %s", slug)
	}
	book := h.book()
	ids := make([]int64, 0, n)
	var parent int64
	for i := 1; i <= n; i++ {
		id, err := book.Post(bg, rep, who, parent, "level-"+strconv.Itoa(i)+"-end")
		if err != nil {
			t.Fatalf("level %d: %v", i, err)
		}
		ids = append(ids, id)
		parent = id
	}
	return ids
}

func TestCommentThreadCap(t *testing.T) {
	t.Run("a chain as deep as the cap is drawn in full, each reply inside the one it answers", func(t *testing.T) {
		h, _, _ := commentWorld(t)
		ids := chainThread(t, h, "x", 7, threadDepthCap)
		arts := parseThread(t, h.serve("GET", "/reports/x").Body)
		if len(arts) != threadDepthCap {
			t.Fatalf("%d articles, want %d", len(arts), threadDepthCap)
		}
		for i, a := range arts {
			if a.ID != ids[i] || a.Depth != i+1 || (i > 0 && a.Parent != ids[i-1]) {
				t.Fatalf("article %d = %+v, want id %d at depth %d inside %v", i, a, ids[i], i+1, ids[:i])
			}
			if strings.Contains(a.Own, "continue this thread") {
				t.Errorf("level %d carries a continue link, though nothing is below it", i+1)
			}
		}
	})

	t.Run("a 150-level chain stored through Post is drawn to the cap and no deeper, and continues on a branch page", func(t *testing.T) {
		h, _, reader := commentWorld(t)
		const n = 150
		ids := chainThread(t, h, "x", 7, n)
		if got := h.commentCount("1 = 1"); got != n {
			t.Fatalf("%d comments stored, want %d: writes must accept any depth", got, n)
		}
		for _, p := range []string{"/reports/x", "/"} {
			got := h.serve("GET", p, h.withSID(reader))
			arts := parseThread(t, got.Body)
			if len(arts) != threadDepthCap {
				t.Fatalf("GET %s: %d articles, want exactly the cap %d", p, len(arts), threadDepthCap)
			}
			max := 0
			for _, a := range arts {
				if a.Depth > max {
					max = a.Depth
				}
			}
			if max != threadDepthCap {
				t.Errorf("GET %s: deepest article at %d, want %d", p, max, threadDepthCap)
			}
			last := arts[threadDepthCap-1]
			link := `<a class="continue" href="/reports/x/comments/` + strconv.FormatInt(ids[threadDepthCap-1], 10) + `">continue this thread &rarr;</a>`
			if last.ID != ids[threadDepthCap-1] || !strings.Contains(last.Own, link) {
				t.Errorf("GET %s: the article at the cap is %+v, want the link %s", p, last, link)
			}
			if strings.Contains(last.Own, `class="replies"`) {
				t.Errorf("GET %s: the article at the cap draws replies", p)
			}
			if strings.Count(got.Body, "continue this thread") != 1 {
				t.Errorf("GET %s: the continue link is not shown exactly once", p)
			}
			if !strings.Contains(got.Body, "level-"+strconv.Itoa(threadDepthCap)+"-end") {
				t.Errorf("GET %s: the comment at the cap is not shown", p)
			}
			for _, deeper := range []int{threadDepthCap + 1, 120, n} {
				if strings.Contains(got.Body, "level-"+strconv.Itoa(deeper)+"-end") {
					t.Errorf("GET %s: level %d, below the cap, is drawn", p, deeper)
				}
			}
			// The reply form of the comment at the cap is still there: a reply is never a dead end.
			if !strings.Contains(last.Own, `<input type="hidden" name="parent" value="`+strconv.FormatInt(last.ID, 10)+`">`) {
				t.Errorf("GET %s: the comment at the cap offers no reply form", p)
			}
		}
	})
}

// chainNodes is n comments, each the only reply of the one before.
func chainNodes(n int) []*comments.Node {
	root := &comments.Node{ID: 1, GitHubID: 7, Login: "w", Body: "b"}
	at := root
	for i := 2; i <= n; i++ {
		next := &comments.Node{ID: int64(i), GitHubID: 7, Login: "w", Body: "b"}
		at.Replies = []*comments.Node{next}
		at = next
	}
	return []*comments.Node{root}
}

func TestBuildThreadDepth(t *testing.T) {
	for _, limit := range []int{1, 5, threadDepthCap} {
		views := buildThread(chainNodes(10000), "x", true, 0, limit)
		depth, last := 0, (*commentView)(nil)
		for v := views; len(v) > 0; v = v[0].Replies {
			depth++
			last = &v[0]
		}
		if depth != limit || !last.Cut || last.ID != int64(limit) || !last.CanReply {
			t.Errorf("limit %d: depth %d, last %+v; want depth %d, the last one cut and still answerable", limit, depth, last, limit)
		}
	}
	// A chain shorter than the cap has no cut.
	views := buildThread(chainNodes(3), "x", false, 0, 5)
	if v := views[0].Replies[0].Replies[0]; v.Cut || v.CanReply || len(v.Replies) != 0 {
		t.Errorf("the end of a short chain: %+v", v)
	}
}

func TestCommentOrderIgnoresClock(t *testing.T) {
	h, owner, reader := commentWorld(t)
	base := h.clock.Now()
	// The clock runs backwards between posts, and some posts share a second.
	h.clock.Set(base.Add(10 * time.Hour))
	a := h.say(reader, "x", "first posted, latest stamp", 0)
	h.clock.Set(base)
	b := h.say(owner, "x", "second posted, earlier stamp", 0)
	h.clock.Set(base.Add(-48 * time.Hour))
	c := h.say(reader, "x", "third posted, earliest stamp", 0)
	h.clock.Set(base.Add(20 * time.Hour))
	a1 := h.say(owner, "x", "reply one", a)
	a2 := h.say(reader, "x", "reply two", a)
	h.clock.Set(base.Add(-time.Hour))
	a3 := h.say(owner, "x", "reply three", a)
	b1 := h.say(reader, "x", "reply to the second", b)
	a1a := h.say(reader, "x", "reply to a reply", a1)

	for _, p := range []string{"/reports/x", "/"} {
		arts := parseThread(t, h.serve("GET", p).Body)
		var tops []int64
		kids := map[int64][]int64{}
		for _, art := range arts {
			if art.Depth == 1 {
				tops = append(tops, art.ID)
			} else {
				kids[art.Parent] = append(kids[art.Parent], art.ID)
			}
		}
		if fmt.Sprint(tops) != fmt.Sprint([]int64{a, b, c}) {
			t.Errorf("GET %s: top-level order %v, want %v (posting order, whatever the clock said)", p, tops, []int64{a, b, c})
		}
		if fmt.Sprint(kids[a]) != fmt.Sprint([]int64{a1, a2, a3}) || fmt.Sprint(kids[b]) != fmt.Sprint([]int64{b1}) || fmt.Sprint(kids[a1]) != fmt.Sprint([]int64{a1a}) {
			t.Errorf("GET %s: replies %v, want %d: %v, %d: %v", p, kids, a, []int64{a1, a2, a3}, a1, []int64{a1a})
		}
	}
}

func TestCommentPlaceholders(t *testing.T) {
	h, owner, reader := commentWorld(t)
	eight := h.mint(8, "reader-eight")
	nine := h.mint(9, "blocked-nine")
	say := h.say

	// A: a removed leaf under a visible comment.
	a1 := say(reader, "x", "A-visible-top", 0)
	a2 := say(eight, "x", "A-removed-leaf-text", a1)
	// B: removed, removed, removed, with a visible reply three levels down.
	b1 := say(reader, "x", "B-removed-text-1", 0)
	b2 := say(eight, "x", "B-removed-text-2", b1)
	b3 := say(reader, "x", "B-removed-text-3", b2)
	b4 := say(eight, "x", "B-visible-leaf", b3)
	// C: removed over removed over visible.
	c1 := say(reader, "x", "C-removed-text-1", 0)
	c2 := say(eight, "x", "C-removed-text-2", c1)
	c3 := say(reader, "x", "C-visible-leaf", c2)
	// D: an entirely hidden subtree.
	d1 := say(reader, "x", "D-removed-text-1", 0)
	d2 := say(eight, "x", "D-removed-text-2", d1)
	d3 := say(reader, "x", "D-removed-text-3", d2)
	// E: a blocked account's comment with another reader's reply.
	e1 := say(nine, "x", "E-blocked-text", 0)
	e2 := say(reader, "x", "E-visible-reply", e1)
	// F: a removed chain one short of the depth cap over one visible leaf at the cap
	// (the deepest level a report page draws).
	f := []int64{say(reader, "x", "F-removed-text-1", 0)}
	for i := 2; i < threadDepthCap; i++ {
		f = append(f, say(eight, "x", "F-removed-text-"+strconv.Itoa(i), f[i-2]))
	}
	f6 := say(reader, "x", "F-visible-leaf", f[len(f)-1])
	// G: the owner's own removed comment with a visible reply.
	g1 := say(owner, "x", "G-owner-removed-text", 0)
	g2 := say(reader, "x", "G-visible-reply", g1)

	for _, id := range append([]int64{a2, b1, b2, b3, c1, c2, d1, d2, d3, g1}, f...) {
		h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, id)
	}
	h.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (9, 5)`)
	rows := h.commentCount("1 = 1")

	placeholders := map[int64]int64{ // id: parent it sits in
		b1: 0, b2: b1, b3: b2, c1: 0, c2: c1, e1: 0, g1: 0,
	}
	for i, id := range f {
		if i == 0 {
			placeholders[id] = 0
		} else {
			placeholders[id] = f[i-1]
		}
	}
	visible := map[int64]int64{a1: 0, b4: b3, c3: c2, e2: e1, f6: f[len(f)-1], g2: g1}
	gone := []int64{a2, d1, d2, d3}
	secrets := []string{"A-removed-leaf-text", "B-removed-text", "C-removed-text", "D-removed-text", "E-blocked-text", "F-removed-text", "G-owner-removed-text", "blocked-nine"}

	check := func(when string, sid string) {
		t.Helper()
		var opts []func(*http.Request)
		if sid != "" {
			opts = append(opts, h.withSID(sid))
		}
		for _, p := range []string{"/reports/x", "/"} {
			res := h.serve("GET", p, opts...)
			arts := parseThread(t, res.Body)
			if len(arts) != len(placeholders)+len(visible) {
				t.Errorf("%s GET %s: %d articles %v, want %d", when, p, len(arts), articleIDs(arts), len(placeholders)+len(visible))
			}
			for id, parent := range placeholders {
				art := articleByID(arts, id)
				switch {
				case art == nil:
					t.Errorf("%s GET %s: no placeholder for comment %d", when, p, id)
				case art.Parent != parent:
					t.Errorf("%s GET %s: placeholder %d sits in %d, want %d", when, p, id, art.Parent, parent)
				default:
					// The notice, and the toggle's label when replies are drawn below it: the
					// replies are real comments and fold like any other.
					if got := plain(art.Own); got != "comment removed" && got != "comment removed Hide replies" {
						t.Errorf("%s GET %s: placeholder %d reads %q, want just the notice", when, p, id, got)
					}
					for _, leak := range []string{"<img", `class="login"`, `class="badge"`, "<form", "<textarea", "<details", `class="body"`, "<time"} {
						if strings.Contains(art.Own, leak) {
							t.Errorf("%s GET %s: placeholder %d carries %s: %s", when, p, id, leak, art.Own)
						}
					}
				}
			}
			for id, parent := range visible {
				art := articleByID(arts, id)
				switch {
				case art == nil:
					t.Errorf("%s GET %s: visible comment %d is missing", when, p, id)
				case art.Parent != parent:
					t.Errorf("%s GET %s: comment %d sits in %d, want %d", when, p, id, art.Parent, parent)
				case strings.Contains(art.Own, "comment removed"):
					t.Errorf("%s GET %s: visible comment %d is drawn as removed", when, p, id)
				case (sid != "") != strings.Contains(art.Own, "<form"):
					t.Errorf("%s GET %s: comment %d reply form present = %v for a viewer with session %q", when, p, id, !(sid != ""), sid)
				}
			}
			for _, id := range gone {
				if articleByID(arts, id) != nil {
					t.Errorf("%s GET %s: comment %d has nothing visible below it and is still drawn", when, p, id)
				}
			}
			for _, s := range secrets {
				if strings.Contains(res.Body, s) {
					t.Errorf("%s GET %s: the page holds %q, which is hidden", when, p, s)
				}
			}
			if strings.Count(res.Body, "comment removed") != len(placeholders) {
				t.Errorf("%s GET %s: %d notices, want %d", when, p, strings.Count(res.Body, "comment removed"), len(placeholders))
			}
		}
	}
	check("anonymous", "")
	check("reader", reader)
	check("owner", owner)

	// A write that targets a placeholder is refused like any unusable parent.
	if res := h.postComment(reader, "x", commentForm("answer to a removed one", b1)); res.Status != 404 {
		t.Errorf("a reply to a placeholder: %d, want 404", res.Status)
	}

	// Unblocking the account restores its comment, with no write to any comment.
	h.exec(`DELETE FROM blocked_accounts WHERE github_id = 9`)
	if n := h.commentCount("1 = 1"); n != rows {
		t.Fatalf("%d comments, want %d", n, rows)
	}
	res := h.serve("GET", "/reports/x")
	arts := parseThread(t, res.Body)
	art := articleByID(arts, e1)
	if art == nil || !strings.Contains(art.Own, "E-blocked-text") || !strings.Contains(art.Own, "blocked-nine") || articleByID(arts, e2) == nil || articleByID(arts, e2).Parent != e1 {
		t.Errorf("after unblocking, comment %d is not back with its reply: %+v", e1, art)
	}
	if got, want := len(arts), len(placeholders)+len(visible); got != want {
		t.Errorf("after unblocking: %d articles, want %d (the account's one comment changes from a placeholder to its text)", got, want)
	}
}

func TestCommentAuthorBadge(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mintUser(store.User{GitHubID: testOwnerID, Login: "report-author"})
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	lookalike := h.mintUser(store.User{GitHubID: 4243, Login: "report-author"})
	reader := h.mint(7, "reader")

	logins := map[int64]string{}
	c1 := h.say(owner, "x", "owner top level", 0)
	c2 := h.say(lookalike, "x", "lookalike reply", c1)
	c3 := h.say(reader, "x", "reader reply", c2)
	c4 := h.say(owner, "x", "owner deep reply", c3)
	c5 := h.say(owner, "x", "owner comment to be removed", 0)
	c6 := h.say(reader, "x", "visible reply under the removed owner comment", c5)
	h.exec(`UPDATE comments SET removed_at = 5 WHERE id = ?`, c5)
	for id, l := range map[int64]string{c1: "report-author", c2: "report-author", c3: "reader", c4: "report-author", c6: "reader"} {
		logins[id] = l
	}

	check := func(when string, sid string, badged map[int64]bool) {
		t.Helper()
		var opts []func(*http.Request)
		if sid != "" {
			opts = append(opts, h.withSID(sid))
		}
		for _, p := range []string{"/reports/x", "/"} {
			res := h.serve("GET", p, opts...)
			arts := parseThread(t, res.Body)
			if len(arts) != 6 {
				t.Fatalf("%s GET %s: %d articles, want 6", when, p, len(arts))
			}
			for id, login := range logins {
				art := articleByID(arts, id)
				if art == nil {
					t.Fatalf("%s GET %s: no comment %d", when, p, id)
				}
				if has := strings.Contains(art.Own, badgeMarkup); has != badged[id] {
					t.Errorf("%s GET %s: comment %d (%s) badge = %v, want %v", when, p, id, login, has, badged[id])
				}
				// The word "author" belongs to the badge: with the login taken out, a
				// comment's own text has it only when it is badged.
				text := strings.ReplaceAll(plain(art.Own), login, "")
				if has := authorWordRE.MatchString(text); has != badged[id] {
					t.Errorf("%s GET %s: comment %d: the word 'author' present = %v in %q, want %v", when, p, id, has, text, badged[id])
				}
			}
			// The removed comment of the pinned account is a placeholder: no badge, no word.
			ph := articleByID(arts, c5)
			if ph == nil || strings.Contains(ph.Own, `class="badge"`) || authorWordRE.MatchString(plain(ph.Own)) {
				t.Errorf("%s GET %s: the owner's removed comment: %+v", when, p, ph)
			}
			want := 0
			for _, b := range badged {
				if b {
					want++
				}
			}
			if got := strings.Count(res.Body, `<span class="badge">`); got != want {
				t.Errorf("%s GET %s: %d badges on the page, want %d (one per visible comment by the pinned account)", when, p, got, want)
			}
		}
	}
	pinned := map[int64]bool{c1: true, c4: true}
	// The badge follows the account that wrote the comment, not who is looking.
	check("anonymous", "", pinned)
	check("owner", owner, pinned)
	check("reader", reader, pinned)
	check("the look-alike", lookalike, pinned)

	// It follows the pinned ID, not the login: pin the look-alike and it moves.
	h.rebuild(4243)
	check("pinned to 4243", "", map[int64]bool{c2: true})
	// With no pin nobody is the author.
	h.rebuild(0)
	check("no pin", "", map[int64]bool{})
	check("no pin, the former owner looking", owner, map[int64]bool{})
}

func TestCommentStoreFailure(t *testing.T) {
	t.Run("the comments read fails while the votes read works", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		h.say(reader, "x", "a comment", 0)
		for _, p := range []string{"/", "/reports/x"} {
			if res := h.serve("GET", p); res.Status != 200 || !strings.Contains(res.Body, "a comment") {
				t.Fatalf("control: GET %s = %d", p, res.Status)
			}
		}
		h.exec(`ALTER TABLE comments RENAME TO comments_gone`)
		if _, err := h.store.DB().Exec(`SELECT count(*) FROM votes`); err != nil {
			t.Fatalf("the votes table must still read: %v", err)
		}
		for viewer, sid := range map[string]string{"anonymous": "", "reader": reader, "owner": owner} {
			var opts []func(*http.Request)
			if sid != "" {
				opts = append(opts, h.withSID(sid))
			}
			for _, p := range []string{"/", "/reports/x"} {
				res := h.serve("GET", p, opts...)
				if res.Status != 503 || !strings.Contains(res.Body, "Reports cannot be shown right now") {
					t.Errorf("%s: GET %s = %d, want the 503 page:\n%s", viewer, p, res.Status, res.Body)
				}
				if strings.Contains(res.Body, "No comments yet.") || strings.Contains(res.Body, "<iframe") || strings.Contains(res.Body, "<textarea") {
					t.Errorf("%s: GET %s draws a thread, an empty one or a report though the comments cannot be read:\n%s", viewer, p, res.Body)
				}
				if got := res.Header.Get("Cache-Control"); got != "private, no-cache" {
					t.Errorf("%s: GET %s Cache-Control = %q", viewer, p, got)
				}
			}
		}
		// A write cannot land either, and says so.
		if res := h.postComment(reader, "x", commentForm("hello", 0)); res.Status != http.StatusServiceUnavailable {
			t.Errorf("a comment with no comments table: %d, want 503", res.Status)
		}
	})

	t.Run("the session lookup fails on a POST", func(t *testing.T) {
		h, _, reader := commentWorld(t)
		h.say(reader, "x", "control, taken while sessions work", 0)
		h.exec(`DROP TABLE sessions`)
		unknown := strings.Repeat("A", 43)
		for name, sid := range map[string]string{"a live session": reader, "an unknown session": unknown} {
			for _, body := range []string{commentForm("hello", 0), commentForm("hello", 1)} {
				res := h.postComment(sid, "x", body)
				if res.Status != http.StatusServiceUnavailable || res.Location() != "" {
					t.Errorf("%s: %d %q, want 503 and no redirect (never the signed-out 303)", name, res.Status, res.Location())
				}
				if res.Header.Get("Cache-Control") != "no-store" {
					t.Errorf("%s: Cache-Control %q", name, res.Header.Get("Cache-Control"))
				}
			}
		}
		if n := h.commentCount("1 = 1"); n != 1 {
			t.Errorf("%d comments after the refused posts, want 1", n)
		}
		// Control: with no cookie nobody is looked up, so it is a plain anonymous refusal.
		if res := h.postComment("", "x", commentForm("hello", 0)); res.Status != http.StatusSeeOther || res.Location() != "/reports/x#comments" {
			t.Errorf("anonymous with the session table gone: %d %q", res.Status, res.Location())
		}
	})

	t.Run("the store is closed", func(t *testing.T) {
		h, _, reader := commentWorld(t)
		h.store.Close()
		res := h.postComment(reader, "x", commentForm("hello", 0))
		if res.Status != http.StatusServiceUnavailable || res.Location() != "" {
			t.Errorf("store closed: %d %q", res.Status, res.Location())
		}
		if n := h.reopenCount("comments"); n != 0 {
			t.Errorf("%d comments after a refused post on a closed store", n)
		}
	})
}

// seedChain stores n comments in one transaction, each a reply to the one
// before, as raw SQL (a test may open a transaction; the package may not), with
// a comment of other every other/interleave rows.
func seedChain(t *testing.T, h *harness, report, other, writer int64, n, interleave int) {
	t.Helper()
	tx, err := h.store.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO comments (report_id, parent_id, github_id, body, created_at) VALUES (?, ?, ?, 'deep', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	var parent, otherParent any
	for i := 1; i <= n; i++ {
		res, err := stmt.Exec(report, parent, writer)
		if err != nil {
			t.Fatalf("level %d: %v", i, err)
		}
		parent, _ = res.LastInsertId()
		if other != 0 && i%interleave == 0 {
			res, err := stmt.Exec(other, otherParent, writer)
			if err != nil {
				t.Fatal(err)
			}
			otherParent, _ = res.LastInsertId()
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestCommentReportLifecycle(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "y", "Report Y", "published", reportHTML(300, "y"))
	x1 := h.say(reader, "x", "x-one", 0)
	h.say(owner, "x", "x-two", x1)
	h.say(reader, "y", "y-one", 0)
	id, _, _, _ := h.reportRow("x")
	shows := func(when, slug string, want ...string) {
		t.Helper()
		body := h.serve("GET", "/reports/"+slug).Body
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: /reports/%s does not show %q", when, slug, w)
			}
		}
	}
	shows("before anything", "x", "x-one", "x-two")

	// Replace by re-upload keeps the report's id, so its comments.
	h.mustUpload(owner, "x", "Report X, revised", "published", reportHTML(500, "x2"))
	if id2, _, _, _ := h.reportRow("x"); id2 != id {
		t.Fatalf("the replace changed the report id %d -> %d", id, id2)
	}
	shows("after a replace", "x", "x-one", "x-two")

	// Unpublished: the page and the form are gone, the comments are kept.
	h.mustAct(owner, "x", "unpublish")
	if res := h.serve("GET", "/reports/x"); res.Status != 404 {
		t.Fatalf("unpublished report: %d", res.Status)
	}
	if res := h.postComment(reader, "x", commentForm("while unpublished", 0)); res.Status != 404 || res.Body != notFoundTxt {
		t.Errorf("a comment on an unpublished report: %d %q", res.Status, res.Body)
	}
	if n := h.commentCount("report_id = ?", id); n != 2 {
		t.Errorf("%d comments while unpublished, want both kept", n)
	}
	h.mustAct(owner, "x", "publish")
	shows("after republishing", "x", "x-one", "x-two")
	h.say(reader, "x", "x-three", 0)

	// Deleted while it holds a 1,500-deep chain, with another report's comments
	// interleaved in id order: all of its comments go, the other report's stay.
	yID, _, _, _ := h.reportRow("y")
	seedChain(t, h, id, yID, 7, 1500, 250)
	yBefore := h.commentCount("report_id = ?", yID)
	if yBefore != 1+6 || h.commentCount("report_id = ?", id) != 1503 {
		t.Fatalf("seeded x with %d and y with %d comments", h.commentCount("report_id = ?", id), yBefore)
	}
	h.mustAct(owner, "x", "delete")
	if n := h.commentCount("report_id = ?", id); n != 0 {
		t.Errorf("%d comments left after the report was deleted", n)
	}
	if n := h.commentCount("report_id = ?", yID); n != yBefore {
		t.Errorf("the other report has %d comments, want %d", n, yBefore)
	}
	shows("after the other report was deleted", "y", "y-one")
	h.mustUpload(owner, "x", "Report X again", "published", reportHTML(300, "again"))
	if body := h.serve("GET", "/reports/x").Body; strings.Contains(body, "x-one") || !strings.Contains(body, "No comments yet.") {
		t.Errorf("a deleted report's comments reappeared on its successor:\n%s", body)
	}
}

// TestThreadTenThousandTiming measures what one page view pays for a thread of
// 10,000 comments: the read, the tree, and the render for a signed-in viewer,
// who is drawn a reply form for every comment. About a quarter are hidden.
func TestThreadTenThousandTiming(t *testing.T) {
	h, _, _ := commentWorld(t)
	const tops, replies = 2500, 3
	for i := 0; i < 10; i++ {
		h.mint(int64(100+i), "writer"+strconv.Itoa(i))
	}
	h.exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (109, 5)`)
	rep, _, _, _ := h.reportRow("x")

	tx, err := h.store.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO comments (report_id, parent_id, github_id, body, created_at, removed_at) VALUES (?, ?, ?, ?, 1, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	removedAt := func(n int) any {
		if n%5 == 0 {
			return 7
		}
		return nil
	}
	for i := 0; i < tops; i++ {
		res, err := stmt.Exec(rep, nil, 100+i%10, "a top-level comment with a few words in it, number "+strconv.Itoa(i), removedAt(i))
		if err != nil {
			t.Fatal(err)
		}
		top, _ := res.LastInsertId()
		for j := 0; j < replies; j++ {
			if _, err := stmt.Exec(rep, top, 100+(i+j)%10, "a reply with a few words in it, number "+strconv.Itoa(j), removedAt(i+j+3)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	total := h.commentCount("1 = 1")
	hidden := h.commentCount("removed_at IS NOT NULL OR github_id = 109")
	if total != tops*(1+replies) || hidden < total/5 {
		t.Fatalf("seeded %d comments, %d of them hidden", total, hidden)
	}

	book := h.book()
	t0 := time.Now()
	nodes, err := book.Tree(bg, rep)
	treeTook := time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}
	t1 := time.Now()
	views := buildThread(nodes, "x", true, testOwnerID, threadDepthCap)
	buildTook := time.Since(t1)
	pages := template.Must(template.ParseFS(templateFS, "templates/*.html"))
	data := readerData{
		pageData:  pageData{Title: "x", SignedIn: true, Viewer: auth.Viewer{GitHubID: 7, Login: "reader"}},
		Report:    reports.Report{ID: rep, Slug: "x", Title: "Report X"},
		HasReport: true,
		Thread:    views,
	}
	var buf bytes.Buffer
	t2 := time.Now()
	if err := pages.ExecuteTemplate(&buf, "report.html", data); err != nil {
		t.Fatal(err)
	}
	renderTook := time.Since(t2)
	t.Logf("%d comments (%d hidden): Tree %s, buildThread %s, render %s, page %d bytes", total, hidden, treeTook, buildTook, renderTook, buf.Len())
	for name, d := range map[string]time.Duration{"Tree": treeTook, "buildThread": buildTook, "render": renderTook} {
		if d > time.Second {
			t.Errorf("%s took %s for %d comments, want under 1s", name, d, total)
		}
	}
	if got := strings.Count(buf.String(), `<article class="comment"`); got != len(nodes)+countReplies(nodes) {
		t.Errorf("%d articles drawn, want %d", got, len(nodes)+countReplies(nodes))
	}
}

func countReplies(nodes []*comments.Node) int {
	n := 0
	for _, x := range nodes {
		n += len(x.Replies) + countReplies(x.Replies)
	}
	return n
}

// TestCommentStyles pins the rules of the comment chrome that matter beyond
// looks: a body is clipped to its own box (stacked combining marks cannot paint
// over a byline or the Author badge), wraps without widening the page, keeps its
// line breaks, a replies block never sets display (so [hidden] folds it), and no
// rule uses a colour the render assay reads as a script run (lesson #941).
func TestCommentStyles(t *testing.T) {
	h := newHarness(t, false)
	res := h.serve("GET", "/static/site.css")
	if res.Status != 200 {
		t.Fatalf("GET /static/site.css: %d", res.Status)
	}
	rule := func(sel string) string {
		m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(sel) + ` \{([^}]*)\}`).FindStringSubmatch(res.Body)
		if m == nil {
			t.Fatalf("site.css has no rule for %s", sel)
		}
		return m[1]
	}
	for _, want := range []string{"overflow: clip", "overflow-wrap: anywhere", "white-space: pre-wrap"} {
		if !strings.Contains(rule(".body"), want) {
			t.Errorf(".body lacks %q", want)
		}
	}
	if strings.Contains(rule(".replies"), "display") {
		t.Error(".replies sets display, which would defeat [hidden]")
	}
	if !strings.Contains(res.Body, ".replies[hidden] { display: none; }") {
		t.Error("site.css does not fold a hidden replies block")
	}
	if loud := regexp.MustCompile(`(?i)#(f00|ff0000|0f0|00ff00)\b|\b(red|lime|green)\b|rgba?\(`).FindString(res.Body); loud != "" {
		t.Errorf("site.css uses %q, a colour the render assay reads as a script run", loud)
	}
}
