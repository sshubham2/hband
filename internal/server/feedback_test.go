package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/store"
)

// postFeedback sends the feedback form for slug with a raw urlencoded body. sid
// "" is a visitor with no session.
func (h *harness) postFeedback(sid, slug, body string, extra ...func(*http.Request)) result {
	h.t.Helper()
	opts := []func(*http.Request){withBody(formType, []byte(body))}
	if sid != "" {
		opts = append(opts, h.withSID(sid))
	}
	return h.serve("POST", "/reports/"+slug+"/feedback", append(opts, extra...)...)
}

// feedbackForm is the urlencoded form of a message.
func feedbackForm(text string) string { return url.Values{"body": {text}}.Encode() }

// sendFeedback sends a message as sid and fails unless it is answered with the
// 303 to the sent note.
func (h *harness) sendFeedback(sid, slug, text string) {
	h.t.Helper()
	res := h.postFeedback(sid, slug, feedbackForm(text))
	if res.Status != http.StatusSeeOther || res.Location() != "/reports/"+slug+"#feedback-sent" {
		h.t.Fatalf("feedback %q on %s: %d %q %q", text, slug, res.Status, res.Location(), res.Body)
	}
}

func (h *harness) feedbackCount(where string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.store.DB().QueryRow("SELECT count(*) FROM feedback WHERE "+where, args...).Scan(&n); err != nil {
		h.t.Fatalf("count feedback: %v", err)
	}
	return n
}

// inboxItem is one <article id="feedback-N"> of an owner's admin page.
type inboxItem struct {
	ID  int64
	Own string
}

var inboxArticleRE = regexp.MustCompile(`(?s)<article class="feedback-message" id="feedback-(\d+)">(.*?)</article>`)

// inboxOf reads the feedback articles of an admin page, in page order.
func inboxOf(page string) []inboxItem {
	var out []inboxItem
	for _, m := range inboxArticleRE.FindAllStringSubmatch(page, -1) {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		out = append(out, inboxItem{ID: id, Own: m[2]})
	}
	return out
}

// adminPageOf is the owner's report page for slug, which must be drawn.
func (h *harness) adminPageOf(owner, slug string) string {
	h.t.Helper()
	res := h.serve("GET", "/admin/reports/"+slug, h.withSID(owner))
	if res.Status != 200 || !hasMarker(res.Body) {
		h.t.Fatalf("the owner's admin page for %s: %d", slug, res.Status)
	}
	return res.Body
}

// feedbackIDRE finds a feedback row id wherever a page could print one.
var feedbackIDRE = regexp.MustCompile(`feedback-\d|/admin/feedback/`)

func TestFeedbackSend(t *testing.T) {
	h, owner, _ := commentWorld(t)
	reader := h.mintUser(store.User{GitHubID: 7, Login: "reader"})

	res := h.postFeedback(reader, "x", feedbackForm("Hello author"))
	if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#feedback-sent" || res.Body != "" {
		t.Fatalf("the first message: %d %q %q, want a 303 to /reports/x#feedback-sent", res.Status, res.Location(), res.Body)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var reportID, sender int64
	var body string
	if err := h.store.DB().QueryRow(`SELECT report_id, github_id, body FROM feedback WHERE id = 1`).Scan(&reportID, &sender, &body); err != nil {
		t.Fatal(err)
	}
	if id, _, _, _ := h.reportRow("x"); reportID != id || sender != 7 || body != "Hello author" {
		t.Errorf("stored (report %d, by %d, %q)", reportID, sender, body)
	}
	if n := h.commentCount("1 = 1"); n != 0 {
		t.Errorf("%d comments written by a feedback message", n)
	}

	// The 303 carries no row id: the second message is answered with the same bytes.
	second := h.postFeedback(reader, "x", feedbackForm("A second message"))
	if d := sameResponse(res, second); d != "" {
		t.Errorf("the second message is answered differently from the first (a row id leaked?): %s", d)
	}
	// The owner may send too, like any signed-in account.
	h.sendFeedback(owner, "x", "A note from the owner's own account")
	if n := h.feedbackCount("report_id = ?", reportID); n != 3 {
		t.Fatalf("%d messages stored, want 3", n)
	}
	if n := h.feedbackCount("github_id = ?", testOwnerID); n != 1 {
		t.Errorf("the owner's message is not stored under the owner's account")
	}

	// The owner reads them, with each sender, on the report's admin page.
	admin := h.adminPageOf(owner, "x")
	items := inboxOf(admin)
	if len(items) != 3 || items[0].ID != 3 || items[1].ID != 2 || items[2].ID != 1 {
		t.Fatalf("the admin page lists %+v, want messages 3, 2, 1", items)
	}
	if !strings.Contains(items[2].Own, `<span class="login">reader</span>`) || !strings.Contains(items[2].Own, `<p class="body">Hello author</p>`) {
		t.Errorf("message 1 lacks its sender or its text:\n%s", items[2].Own)
	}
	if !strings.Contains(items[0].Own, `<span class="login">the-owner</span>`) {
		t.Errorf("message 3 is not shown under the owner's login:\n%s", items[0].Own)
	}

	// A message is not a comment: no reader page shows it, to anyone.
	for viewer, sid := range map[string]string{"anonymous": "", "the sender": reader, "the owner": owner} {
		var opts []func(*http.Request)
		if sid != "" {
			opts = append(opts, h.withSID(sid))
		}
		for _, p := range []string{"/reports/x", "/"} {
			got := h.serve("GET", p, opts...)
			for _, text := range []string{"Hello author", "A second message", "A note from the owner"} {
				if strings.Contains(got.Body, text) {
					t.Errorf("%s: GET %s shows the private message %q", viewer, p, text)
				}
			}
			if !strings.Contains(got.Body, "No comments yet.") {
				t.Errorf("%s: GET %s: the comment thread is not empty:\n%s", viewer, p, got.Body)
			}
			if feedbackIDRE.MatchString(got.Body) {
				t.Errorf("%s: GET %s prints a feedback id", viewer, p)
			}
		}
	}
}

func TestFeedbackRefusedSenders(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "drafty", "Drafty", "draft", reportHTML(300, "drafty"))
	h.sendFeedback(reader, "x", "a control message")
	before := h.feedbackCount("1 = 1")

	expired := h.mint(8, "reader8")
	h.clock.Advance(31 * 24 * time.Hour) // past the 30-day session
	unknown := strings.Repeat("A", 43)   // grammar-valid, no such session
	huge := "body=" + strings.Repeat("a", 70<<10)
	for name, sid := range map[string]string{
		"no session": "", "an expired session": expired, "an unknown session": unknown, "a malformed cookie": "garbage",
	} {
		for what, body := range map[string]string{
			"a normal body": feedbackForm("ghost message"),
			// Nothing is parsed before the visitor is known: a form that a signed-in
			// reader would be refused is answered the same way here.
			"an empty body":     "body=",
			"garbage":           "%zz&&=&body",
			"no form at all":    "",
			"a body over 64KiB": huge,
		} {
			res := h.postFeedback(sid, "x", body)
			if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#feedback" || res.Body != "" {
				t.Errorf("%s, %s: %d %q %q, want 303 to /reports/x#feedback", name, what, res.Status, res.Location(), res.Body)
			}
			if res.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s, %s: Cache-Control %q", name, what, res.Header.Get("Cache-Control"))
			}
		}
	}
	if n := h.feedbackCount("1 = 1"); n != before {
		t.Fatalf("%d messages written for visitors who are not signed in, want %d", n-before, 0)
	}
	// (the owner signs in again: the clock moved past the first session)
	if got := h.adminPageOf(h.mint(testOwnerID, "the-owner"), "x"); strings.Contains(got, "ghost message") || len(inboxOf(got)) != 1 {
		t.Errorf("the owner's page shows a message from a visitor who is not signed in:\n%s", got)
	}

	// A signed-in reader's POST from another site stops at cross-origin
	// protection, before the handler.
	live := h.mint(9, "reader9")
	for _, hdr := range [][2]string{{"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}, {"Origin", "https://evil.example"}} {
		if res := h.postFeedback(live, "x", feedbackForm("from another site"), withHeader(hdr[0], hdr[1])); res.Status != http.StatusForbidden {
			t.Errorf("%s: %s: %d, want 403", hdr[0], hdr[1], res.Status)
		}
	}
	if n := h.feedbackCount("1 = 1"); n != before {
		t.Fatalf("%d messages written by requests from another site", n-before)
	}

	// Controls: the site's own forms carry these headers and are taken.
	for _, hdr := range [][2]string{{"Sec-Fetch-Site", "same-origin"}, {"Origin", h.cfg.Origin}} {
		if res := h.postFeedback(live, "x", feedbackForm("control"), withHeader(hdr[0], hdr[1])); res.Status != http.StatusSeeOther {
			t.Errorf("control %s: %s: %d", hdr[0], hdr[1], res.Status)
		}
	}
	if n := h.feedbackCount("1 = 1"); n != before+2 {
		t.Fatalf("control: %d messages, want %d", n, before+2)
	}
}

func TestFeedbackBodies(t *testing.T) {
	h, owner, reader := commentWorld(t)
	enc := url.QueryEscape
	rn := func(s string, n int) string { return strings.Repeat(s, n) }
	bad := []struct{ name, body string }{
		{"no body field", "text=hello"},
		{"another field only", "parent=1"},
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
		res := h.postFeedback(reader, "x", c.body)
		if res.Status != http.StatusBadRequest || res.Location() != "" {
			t.Errorf("%s: %d %q, want 400 and no redirect", c.name, res.Status, res.Location())
		}
	}
	for name, res := range map[string]result{
		"only in the query string": h.serve("POST", "/reports/x/feedback?body=hello", h.withSID(reader), withBody(formType, nil)),
		"query string and no body": h.serve("POST", "/reports/x/feedback?body=hello", h.withSID(reader)),
		"not a form":               h.serve("POST", "/reports/x/feedback", h.withSID(reader), withBody("application/json", []byte(`{"body":"hello"}`))),
		"plain text":               h.serve("POST", "/reports/x/feedback", h.withSID(reader), withBody("text/plain", []byte("body=hello"))),
		"multipart":                h.serve("POST", "/reports/x/feedback", h.withSID(reader), withBody("multipart/form-data; boundary=b", []byte("--b\r\nContent-Disposition: form-data; name=\"body\"\r\n\r\nhello\r\n--b--\r\n"))),
	} {
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, res.Status)
		}
	}
	if n := h.feedbackCount("1 = 1"); n != 0 {
		t.Fatalf("%d messages written by forms that were refused", n)
	}

	// Controls: the limits are exact, the text is stored as normalised, and
	// markup is stored as typed.
	const script = "<script>x</script>"
	good := []struct{ name, body, want string }{
		{"exactly 5000 runes", "body=" + rn("a", 5000), rn("a", 5000)},
		{"exactly 5000 four-byte runes", "body=" + enc(rn("😀", 5000)), rn("😀", 5000)},
		{"CRLF is stored as LF", "body=a%0D%0Ab", "a\nb"},
		{"a lone CR is stored as LF", "body=a%0Db", "a\nb"},
		{"multi-line text", "body=" + enc("line one\nline two\n\nline four"), "line one\nline two\n\nline four"},
		{"tab and newline inside are kept", "body=" + enc("a\tb\nc"), "a\tb\nc"},
		{"the ends are trimmed", "body=" + enc("  hi  "), "hi"},
		{"literal markup", "body=" + enc(script), script},
	}
	for i, c := range good {
		res := h.postFeedback(reader, "x", c.body)
		if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#feedback-sent" {
			t.Errorf("%s: %d %q, want a 303 to the sent note", c.name, res.Status, res.Location())
			continue
		}
		var got string
		if err := h.store.DB().QueryRow(`SELECT body FROM feedback WHERE id = ?`, i+1).Scan(&got); err != nil || got != c.want {
			t.Errorf("%s: stored %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if n := h.feedbackCount("1 = 1"); n != len(good) {
		t.Errorf("%d messages after the controls, want %d", n, len(good))
	}

	// The markup is drawn as text on the owner's page: escaped, never live.
	admin := h.adminPageOf(owner, "x")
	if !strings.Contains(admin, "&lt;script&gt;x&lt;/script&gt;") || strings.Contains(admin, "<script") {
		t.Errorf("the admin page does not show the markup as escaped text:\n%s", admin)
	}
	if len(inboxOf(admin)) != len(good) {
		t.Errorf("the admin page lists %d articles, want %d", len(inboxOf(admin)), len(good))
	}
}

func TestFeedbackHiddenReports(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "drafty", "Drafty", "draft", reportHTML(300, "drafty"))
	h.mustUpload(owner, "taken-down", "Taken down", "published", reportHTML(300, "down"))
	h.mustAct(owner, "taken-down", "unpublish")
	h.mustUpload(owner, "deleted", "Deleted", "published", reportHTML(300, "deleted"))
	h.mustAct(owner, "deleted", "delete")

	baseline := h.postFeedback("", "unknown-slug", feedbackForm("hello"))
	if baseline.Status != 404 || baseline.Body != notFoundTxt {
		t.Fatalf("baseline: %d %q", baseline.Status, baseline.Body)
	}
	live, liveOwner := h.mint(9, "reader9"), h.mint(testOwnerID, "the-owner")
	for viewer, sid := range map[string]string{"anonymous": "", "reader": live, "owner": liveOwner} {
		for _, slug := range []string{"drafty", "taken-down", "deleted", "unknown-slug", "UPPER-CASE", strings.Repeat("a", 81)} {
			for _, body := range []string{feedbackForm("hello"), "", "body="} {
				if d := sameResponse(h.postFeedback(sid, slug, body), baseline); d != "" {
					t.Errorf("%s: POST /reports/%s/feedback %q differs from an unknown slug: %s", viewer, slug, body, d)
				}
			}
		}
	}
	if n := h.feedbackCount("1 = 1"); n != 0 {
		t.Fatalf("%d messages written to reports that are not published", n)
	}
	// Control: the published report takes one.
	h.sendFeedback(reader, "x", "control")

	// The report is published when the handler looks it up and gone when the
	// write runs: a test-only trigger makes the insert write nothing, which is
	// what the in-statement published check does in that race. The answer is the
	// 404 of a report that is not there, not a 503 and not a 303.
	h.exec(`CREATE TRIGGER fb_skip BEFORE INSERT ON feedback BEGIN SELECT RAISE(IGNORE); END;`)
	before := h.feedbackCount("1 = 1")
	race := h.postFeedback(reader, "x", feedbackForm("lost in the race"))
	if race.Status != 404 || race.Body != notFoundTxt || race.Location() != "" {
		t.Errorf("a send whose write found no published report: %d %q %q, want http.NotFound's 404", race.Status, race.Body, race.Location())
	}
	if d := sameResponse(race, h.postFeedback(reader, "unknown-slug", feedbackForm("lost in the race"))); d != "" {
		t.Errorf("the race answer differs from an unknown slug's for the same session: %s", d)
	}
	if n := h.feedbackCount("1 = 1"); n != before {
		t.Errorf("%d rows written through a skipped insert", n-before)
	}
	h.exec(`DROP TRIGGER fb_skip`)
	h.sendFeedback(reader, "x", "after the trigger is gone")
}

func TestFeedbackStoreFailure(t *testing.T) {
	t.Run("the session lookup fails on a POST", func(t *testing.T) {
		h, _, reader := commentWorld(t)
		h.sendFeedback(reader, "x", "control, taken while sessions work")
		h.exec(`DROP TABLE sessions`)
		unknown := strings.Repeat("A", 43)
		for name, sid := range map[string]string{"a live session": reader, "an unknown session": unknown} {
			res := h.postFeedback(sid, "x", feedbackForm("hello"))
			if res.Status != http.StatusServiceUnavailable || res.Location() != "" {
				t.Errorf("%s: %d %q, want 503 and no redirect (never the signed-out 303)", name, res.Status, res.Location())
			}
			if res.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s: Cache-Control %q", name, res.Header.Get("Cache-Control"))
			}
		}
		if n := h.feedbackCount("1 = 1"); n != 1 {
			t.Errorf("%d messages after the refused sends, want 1", n)
		}
		// Control: with no cookie nobody is looked up, so it is a plain anonymous refusal.
		if res := h.postFeedback("", "x", feedbackForm("hello")); res.Status != http.StatusSeeOther || res.Location() != "/reports/x#feedback" {
			t.Errorf("anonymous with the session table gone: %d %q", res.Status, res.Location())
		}
	})

	t.Run("the write fails", func(t *testing.T) {
		h, _, reader := commentWorld(t)
		h.exec(`ALTER TABLE feedback RENAME TO feedback_gone`)
		res := h.postFeedback(reader, "x", feedbackForm("hello"))
		if res.Status != http.StatusServiceUnavailable || res.Location() != "" {
			t.Errorf("a send with no feedback table: %d %q, want 503 and no redirect", res.Status, res.Location())
		}
		if n := countAt(t, h.store, "feedback_gone"); n != 0 {
			t.Errorf("%d rows written to a table that was being refused", n)
		}
		// A refused body is still a 400: it never reaches the table.
		if res := h.postFeedback(reader, "x", "body="); res.Status != http.StatusBadRequest {
			t.Errorf("an empty body with no feedback table: %d, want 400", res.Status)
		}
		// The reader pages do not read the table, so they still draw.
		if page := h.serve("GET", "/reports/x", h.withSID(reader)); page.Status != 200 {
			t.Errorf("GET /reports/x with no feedback table: %d", page.Status)
		}
	})

	t.Run("the owner's inbox read fails", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		h.sendFeedback(reader, "x", "a message that cannot be read back")
		if got := h.adminPageOf(owner, "x"); !strings.Contains(got, "a message that cannot be read back") {
			t.Fatalf("control: the admin page does not show the message:\n%s", got)
		}
		h.exec(`ALTER TABLE feedback RENAME TO feedback_gone`)
		res := h.serve("GET", "/admin/reports/x", h.withSID(owner))
		if res.Status != http.StatusInternalServerError {
			t.Errorf("GET /admin/reports/x with an unreadable inbox: %d, want 500", res.Status)
		}
		if hasMarker(res.Body) || strings.Contains(res.Body, "feedback-") || strings.Contains(res.Body, "No feedback on this report yet.") ||
			strings.Contains(res.Body, "a message that cannot be read back") {
			t.Errorf("the page draws an inbox, an empty one or the message though the table cannot be read:\n%s", res.Body)
		}
	})

	t.Run("the store is closed", func(t *testing.T) {
		h, _, reader := commentWorld(t)
		h.store.Close()
		res := h.postFeedback(reader, "x", feedbackForm("hello"))
		if res.Status != http.StatusServiceUnavailable || res.Location() != "" {
			t.Errorf("store closed: %d %q", res.Status, res.Location())
		}
		if n := h.reopenCount("feedback"); n != 0 {
			t.Errorf("%d messages after a refused send on a closed store", n)
		}
	})
}

func TestFeedbackDoesNotInterfere(t *testing.T) {
	t.Run("other readers' feedback", func(t *testing.T) {
		w := newDislikeWorld(t)
		before := w.observe(t)
		adminBefore := w.adminTally(t, "x")
		if len(inboxOf(adminBefore)) != 0 || !strings.Contains(adminBefore, "No feedback on this report yet.") {
			t.Fatalf("the owner's starting page already lists feedback:\n%s", adminBefore)
		}

		r20, r21 := w.reader(20), w.reader(21)
		sent := []struct{ sid, slug, text string }{
			{r20, "x", "secret-twenty-on-x"}, {r21, "x", "secret-twentyone-on-x"},
			{r20, "y", "secret-twenty-on-y"}, {r21, "y", "secret-twentyone-on-y"},
			{r20, "x", "secret-twenty-on-x-again"},
		}
		for _, s := range sent {
			w.h.sendFeedback(s.sid, s.slug, s.text)
		}
		after := w.observe(t)
		sameObservation(t, "after feedback on x and on y", before, after)

		// Sensitivity: the owner's report pages do change, one article per message
		// of that report, each with exactly its sender's login.
		x, y := inboxOf(w.adminTally(t, "x")), inboxOf(w.adminTally(t, "y"))
		if len(x) != 3 || len(y) != 2 {
			t.Fatalf("the owner's pages list %d messages on x and %d on y, want 3 and 2", len(x), len(y))
		}
		for _, it := range append(append([]inboxItem{}, x...), y...) {
			twenty := strings.Contains(it.Own, "secret-twenty-on") && !strings.Contains(it.Own, "secret-twentyone")
			if twenty != (strings.Contains(it.Own, ">reader20<") && !strings.Contains(it.Own, ">reader21<")) {
				t.Errorf("article %d does not carry exactly its sender's login:\n%s", it.ID, it.Own)
			}
		}
		if strings.Contains(w.adminTally(t, "y"), "secret-twenty-on-x") || strings.Contains(w.adminTally(t, "x"), "-on-y") {
			t.Error("a report's admin page lists another report's messages")
		}
		// No reader page and no answer to a non-owner says anything about them.
		for k, res := range after {
			if strings.Contains(res.Body, "secret-") || feedbackIDRE.MatchString(res.Body) {
				t.Errorf("%s shows a private message or a feedback id:\n%s", k, res.Body)
			}
		}
		// Sensitivity: the comparison is not blind to the box. A like is public and
		// does change the anonymous page.
		w.h.mustVote(w.reader(23), "x", "up")
		if d := sameResponse(before["anonymous GET /reports/x"], w.h.serve("GET", "/reports/x")); d == "" {
			t.Error("the comparison is blind: an extra like left the anonymous page unchanged")
		}
	})

	t.Run("the sender's own pages are unchanged by a send", func(t *testing.T) {
		w := newDislikeWorld(t)
		h := w.h
		r20 := w.reader(20)
		pages := func() map[string]result {
			out := map[string]result{}
			for _, p := range []string{"/", "/reports/x"} {
				out[p] = h.serve("GET", p, h.withSID(r20))
			}
			return out
		}
		before := pages()
		// "/" draws the newest report, which is y in this world.
		for p, action := range map[string]string{"/": `action="/reports/y/feedback"`, "/reports/x": `action="/reports/x/feedback"`} {
			if res := before[p]; res.Status != 200 || !strings.Contains(res.Body, action) {
				t.Fatalf("INVALID: the sender's GET %s is not drawn with the box (%d)", p, res.Status)
			}
		}
		h.sendFeedback(r20, "x", "secret-from-the-sender")
		after := pages()
		for p := range before {
			if d := sameResponse(before[p], after[p]); d != "" {
				t.Errorf("the sender's GET %s changed after its own send: %s", p, d)
			}
			if strings.Contains(after[p].Body, "secret-from-the-sender") {
				t.Errorf("the sender's GET %s shows its own message", p)
			}
		}
		// Sensitivity: the owner, who is not the sender, does see it.
		if got := w.adminTally(t, "x"); !strings.Contains(got, "secret-from-the-sender") {
			t.Errorf("the owner's page does not show the message:\n%s", got)
		}
	})

	t.Run("feedback does not shift the ids of public comments", func(t *testing.T) {
		// Two worlds that differ only in whether readers sent feedback first: the
		// next public comment must get the same id and the same page, or the ids
		// would tell a reader how much feedback exists.
		var ids [2]int64
		var pages [2]result
		for i, withFeedback := range []bool{false, true} {
			h, _, _ := commentWorld(t)
			sender := h.mint(20, "reader20")
			if withFeedback {
				for _, text := range []string{"one", "two", "three"} {
					h.sendFeedback(sender, "x", text)
				}
			}
			commenter := h.mint(40, "reader40")
			ids[i] = h.say(commenter, "x", "a real comment", 0)
			pages[i] = h.serve("GET", "/reports/x")
		}
		if ids[0] != ids[1] || ids[0] != 1 {
			t.Errorf("the comment got id %d without feedback and %d with it, want 1 in both", ids[0], ids[1])
		}
		if d := sameResponse(pages[0], pages[1]); d != "" {
			t.Errorf("the public page differs between the worlds: %s", d)
		}
	})
}

func TestFeedbackFollowsReportLifecycle(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "y", "Report Y", "published", reportHTML(300, "y"))
	h.sendFeedback(reader, "x", "x-one")
	h.sendFeedback(reader, "x", "x-two")
	h.sendFeedback(reader, "y", "y-one")
	id, _, _, _ := h.reportRow("x")
	// Replies are written only by a later slice; the schema carries them now, so
	// the two-hop cascade is pinned with raw rows.
	for _, fb := range []int{1, 2, 3} {
		h.exec(`INSERT INTO feedback_replies (feedback_id, body, created_at) VALUES (?, 'a reply', 1)`, fb)
	}
	counts := func(when string, feedback, replies int) {
		t.Helper()
		if n := h.count("feedback"); n != feedback {
			t.Errorf("%s: %d feedback rows, want %d", when, n, feedback)
		}
		if n := h.count("feedback_replies"); n != replies {
			t.Errorf("%s: %d reply rows, want %d", when, n, replies)
		}
	}
	listed := func(when, slug string, want ...string) {
		t.Helper()
		page := h.adminPageOf(owner, slug)
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("%s: /admin/reports/%s does not show %q", when, slug, w)
			}
		}
	}
	counts("before anything", 3, 3)
	listed("before anything", "x", "x-one", "x-two", ">reader<")

	// Replace by re-upload keeps the report's id, so its feedback.
	h.mustUpload(owner, "x", "Report X, revised", "published", reportHTML(500, "x2"))
	if id2, _, _, _ := h.reportRow("x"); id2 != id {
		t.Fatalf("the replace changed the report id %d -> %d", id, id2)
	}
	counts("after a replace", 3, 3)
	listed("after a replace", "x", "x-one", "x-two")

	// Unpublished: the form's POST is a 404, the owner still reads the messages.
	h.mustAct(owner, "x", "unpublish")
	if res := h.postFeedback(reader, "x", feedbackForm("while unpublished")); res.Status != 404 || res.Body != notFoundTxt {
		t.Errorf("a message to an unpublished report: %d %q", res.Status, res.Body)
	}
	counts("while unpublished", 3, 3)
	listed("while unpublished", "x", "x-one", "x-two")
	h.mustAct(owner, "x", "publish")
	h.sendFeedback(reader, "x", "x-three")

	// The sender signs in again under a new login: the messages follow the
	// account's id, and show the new name.
	h.mintUser(store.User{GitHubID: 7, Login: "reader-renamed"})
	counts("after a second sign-in", 4, 3)
	page := h.adminPageOf(owner, "x")
	if !strings.Contains(page, ">reader-renamed<") || strings.Contains(page, ">reader<") || len(inboxOf(page)) != 3 {
		t.Errorf("after the sender signed in under a new login:\n%s", page)
	}

	// Deleted: Library.Delete answers nil (the action is a 303), the messages and,
	// through them, the replies go, the other report's stay, and the ids are not
	// handed out again.
	maxBefore := 0
	if err := h.store.DB().QueryRow(`SELECT max(id) FROM feedback`).Scan(&maxBefore); err != nil {
		t.Fatal(err)
	}
	if err := h.lib.Delete(bg, "x"); err != nil {
		t.Fatalf("Library.Delete of a report with messages and replies: %v", err)
	}
	counts("after the report was deleted", 1, 1)
	if n := h.feedbackCount("report_id = ?", id); n != 0 {
		t.Errorf("%d messages left for the deleted report", n)
	}
	if n := h.count("pragma_foreign_key_check"); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
	listed("after the other report was deleted", "y", "y-one")
	h.sendFeedback(reader, "y", "y-two")
	var next int
	if err := h.store.DB().QueryRow(`SELECT max(id) FROM feedback`).Scan(&next); err != nil || next <= maxBefore {
		t.Errorf("the next message has id %d, want above the deleted ones (%d): %v", next, maxBefore, err)
	}
	h.mustUpload(owner, "x", "Report X again", "published", reportHTML(300, "again"))
	if got := h.adminPageOf(owner, "x"); len(inboxOf(got)) != 0 || !strings.Contains(got, "No feedback on this report yet.") {
		t.Errorf("a deleted report's feedback reappeared on its successor:\n%s", got)
	}
}

func TestFeedbackBox(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "older", "The Older One", "published", reportHTML(300, "older"))
	h.clock.Advance(time.Hour)
	h.mustUpload(owner, "newest", "The Newest One", "published", reportHTML(300, "newest"))
	reader := h.mint(7, "reader")
	h.say(reader, "newest", "a public comment", 0)

	const (
		form    = `<form method="post" action="/reports/newest/feedback">`
		hint    = "Sign in to send the author private feedback."
		ownerLn = `<a href="/admin/reports/newest#feedback">its admin page</a>`
		sent    = `<p id="feedback-sent" class="feedback-sent" hidden>Sent to the author.</p>`
	)
	for _, p := range []string{"/", "/reports/newest"} {
		anon := h.serve("GET", p)
		if !strings.Contains(anon.Body, `<section class="feedback" id="feedback">`) || !strings.Contains(anon.Body, "<h2>Feedback for the author</h2>") ||
			!strings.Contains(anon.Body, hint) {
			t.Errorf("anonymous GET %s: no box or no hint:\n%s", p, anon.Body)
		}
		if strings.Contains(anon.Body, "<textarea") || strings.Contains(anon.Body, "/feedback\"") || strings.Contains(anon.Body, "feedback-sent") {
			t.Errorf("anonymous GET %s is offered a form or the sent note", p)
		}

		got := h.serve("GET", p, h.withSID(reader))
		for _, want := range []string{form, `<textarea name="body" required maxlength="5000" rows="4"></textarea>`, "Send to the author", "Only the author reads this. It is not posted as a comment."} {
			if !strings.Contains(got.Body, want) {
				t.Errorf("non-owner GET %s lacks %q:\n%s", p, want, got.Body)
			}
		}
		// The sent note is in every page a signed-in non-owner is shown, hidden until
		// the success fragment targets it: a lost style rule must degrade to no note
		// at all.
		if !strings.Contains(got.Body, sent) {
			t.Errorf("non-owner GET %s lacks the hidden sent note %q", p, sent)
		}
		if strings.Contains(got.Body, hint) || strings.Contains(got.Body, "its admin page") {
			t.Errorf("non-owner GET %s shows the anonymous hint or the owner's link", p)
		}
		// Between the votes and the comments, and outside every comment.
		votes, box, comments := strings.Index(got.Body, `id="votes"`), strings.Index(got.Body, `id="feedback"`), strings.Index(got.Body, `id="comments"`)
		if !(votes >= 0 && votes < box && box < comments) {
			t.Errorf("non-owner GET %s: votes at %d, feedback box at %d, comments at %d: want that order", p, votes, box, comments)
		}
		for _, art := range parseThread(t, got.Body) {
			if strings.Contains(art.Own, "feedback") {
				t.Errorf("non-owner GET %s: a comment carries feedback markup: %s", p, art.Own)
			}
		}

		own := h.serve("GET", p, h.withSID(owner))
		if !strings.Contains(own.Body, ownerLn) || strings.Contains(own.Body, form) || strings.Contains(own.Body, "feedback-sent") || strings.Contains(own.Body, hint) {
			t.Errorf("owner GET %s: want the admin link and no form, note or hint:\n%s", p, own.Body)
		}
		for who, res := range map[string]result{"anonymous": anon, "non-owner": got, "owner": own} {
			if feedbackIDRE.MatchString(res.Body) {
				t.Errorf("%s GET %s prints a feedback id", who, p)
			}
		}
	}

	// The note's reveal rule is in the stylesheet, and nothing but the success
	// fragment can target the note.
	css := h.serve("GET", "/static/site.css")
	if css.Status != 200 || !strings.Contains(css.Body, "#feedback-sent:target { display: block; }") {
		t.Errorf("site.css has no reveal rule for the sent note:\n%s", css.Body)
	}
	if loud := regexp.MustCompile(`(?i)#(f00|ff0000|0f0|00ff00)\b|\b(red|lime|green)\b|rgba?\(`).FindString(css.Body); loud != "" {
		t.Errorf("site.css uses %q, a colour the render assay reads as a script run", loud)
	}
	for _, sel := range []string{".comments, .feedback, .feedback-inbox {", ".comments textarea, .feedback textarea, .feedback-inbox textarea {", ".feedback-message {"} {
		if !strings.Contains(css.Body, sel) {
			t.Errorf("site.css has no rule %q", sel)
		}
	}
}

func TestFeedbackOwnerList(t *testing.T) {
	// The page prints a literal "UTC", so the zone is moved off UTC here: a time
	// converted without .UTC() would then print the wrong hour.
	saved := time.Local
	time.Local = time.FixedZone("test+5:30", 5*3600+1800)
	t.Cleanup(func() { time.Local = saved })

	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	h.mustUpload(owner, "y", "Report Y", "published", reportHTML(300, "y"))
	h.mustUpload(owner, "drafty", "Drafty", "published", reportHTML(300, "drafty"))
	const avatar = "https://avatars.githubusercontent.com/u/7?v=4"
	withAvatar := h.mintUser(store.User{GitHubID: 7, Login: "reader-seven", AvatarURL: avatar})
	noAvatar := h.mint(8, "reader-eight")

	adminX := func() string { return h.adminPageOf(owner, "x") }
	if page := adminX(); len(inboxOf(page)) != 0 || !strings.Contains(page, "No feedback on this report yet.") ||
		!strings.Contains(page, `<section class="feedback-inbox" id="feedback">`) {
		t.Fatalf("a report with no feedback:\n%s", page)
	}

	type sentMsg struct {
		text  string
		login string
		at    time.Time
	}
	var onX []sentMsg
	send := func(sid, slug, text, login string) {
		h.clock.Advance(97 * time.Minute) // so the times differ, and are not whole hours
		h.sendFeedback(sid, slug, text)
		if slug == "x" {
			onX = append(onX, sentMsg{text: text, login: login, at: h.clock.Now().UTC()})
		}
	}
	send(withAvatar, "x", "first, from seven", "reader-seven")
	send(noAvatar, "x", "second, from eight", "reader-eight")
	send(withAvatar, "y", "a message on y", "")
	send(noAvatar, "x", "third, from eight\nover two lines", "reader-eight")
	send(withAvatar, "drafty", "a message on a report that is then unpublished", "")
	h.mustAct(owner, "drafty", "unpublish")

	page := adminX()
	items := inboxOf(page)
	if len(items) != 3 {
		t.Fatalf("%d messages listed on x, want 3:\n%s", len(items), page)
	}
	// Newest first, each with only its own sender's login, avatar and time.
	for i, item := range items {
		want := onX[len(onX)-1-i]
		other := "reader-seven"
		if want.login == "reader-seven" {
			other = "reader-eight"
		}
		if !strings.Contains(item.Own, `<span class="login">`+want.login+`</span>`) || strings.Contains(item.Own, other) {
			t.Errorf("position %d (%q): not exactly %s's login:\n%s", i, want.text, want.login, item.Own)
		}
		if !strings.Contains(item.Own, `<p class="body">`+want.text+`</p>`) {
			t.Errorf("position %d: the text %q is not drawn:\n%s", i, want.text, item.Own)
		}
		if hasImg := strings.Contains(item.Own, "<img"); hasImg != (want.login == "reader-seven") {
			t.Errorf("position %d (%s): avatar present = %v", i, want.login, hasImg)
		}
		if want.login == "reader-seven" && !strings.Contains(item.Own, `<img class="avatar" src="`+avatar+`" alt="" width="24" height="24">`) {
			t.Errorf("position %d: the avatar is not drawn as the thread draws it:\n%s", i, item.Own)
		}
		// The time is the clock's instant in UTC, whatever zone the machine is in.
		if !strings.Contains(item.Own, `<time datetime="`+want.at.Format("2006-01-02T15:04:05Z")+`">`+want.at.Format("2006-01-02 15:04 UTC")+`</time>`) {
			t.Errorf("position %d: the time is not %s:\n%s", i, want.at.Format("2006-01-02 15:04 UTC"), item.Own)
		}
	}
	if items[0].ID != 4 || items[1].ID != 2 || items[2].ID != 1 {
		t.Errorf("the articles carry the ids %d, %d, %d, want 4, 2, 1 (global row ids, newest first)", items[0].ID, items[1].ID, items[2].ID)
	}
	// Another report's messages are not here, and an unpublished report's are
	// listed on its page.
	if strings.Contains(page, "a message on y") || strings.Contains(page, "then unpublished") {
		t.Error("report x's page lists another report's message")
	}
	if got := h.adminPageOf(owner, "y"); len(inboxOf(got)) != 1 || !strings.Contains(got, "a message on y") {
		t.Errorf("report y's page:\n%s", got)
	}
	if got := h.adminPageOf(owner, "drafty"); len(inboxOf(got)) != 1 || !strings.Contains(got, "a message on a report that is then unpublished") {
		t.Errorf("an unpublished report's page does not list its feedback:\n%s", got)
	}

	// With two dislikes cast, exactly one number sits beside a dislike label, and
	// the list adds no number beside any of the words the page counts.
	h.mustVote(h.mint(30, "voter30"), "x", "down")
	h.mustVote(h.mint(31, "voter31"), "x", "down")
	page = adminX()
	labelled := regexp.MustCompile(`(?i)\b(\d+)\s*(comments?|likes?|dislikes?|views?|open)\b`).FindAllStringSubmatch(page, -1)
	if len(labelled) != 2 || labelled[0][0] != "0 likes" || labelled[1][0] != "2 dislikes" {
		t.Errorf("numbers beside a label on the admin page: %q, want just the like and dislike counts", labelled)
	}
	if len(inboxOf(page)) != 3 || !strings.Contains(page, `<p class="tally">2 dislikes</p>`) {
		t.Errorf("the list changed with the votes:\n%s", page)
	}
	// The inbox sits next to the dislike count, before the preview.
	if tally, box, preview := strings.Index(page, `2 dislikes`), strings.Index(page, `id="feedback"`), strings.Index(page, `<h2>Preview</h2>`); !(tally < box && box < preview) {
		t.Errorf("order on the page: tally %d, inbox %d, preview %d", tally, box, preview)
	}

	// Only the owner can see it: the sender gets the forbidden page, a visitor
	// the sign-in redirect, and neither the text.
	for who, opts := range map[string][]func(*http.Request){"the sender": {h.withSID(withAvatar)}, "anonymous": nil} {
		res := h.serve("GET", "/admin/reports/x", opts...)
		if res.Status < 300 || res.Status >= 500 || hasMarker(res.Body) || strings.Contains(res.Body, "first, from seven") || strings.Contains(res.Body, "feedback-") {
			t.Errorf("%s: GET /admin/reports/x = %d, want a refusal with no admin content:\n%s", who, res.Status, res.Body)
		}
	}
}
