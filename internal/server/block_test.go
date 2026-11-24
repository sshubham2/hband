package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/comments"
)

// blockRoute sends the owner's block or unblock POST for the account with the id as
// spelled (a test may send an id that is not a canonical number), with the given
// body. sid "" is a visitor with no session.
func (h *harness) blockRoute(sid, id, action string, body func(*http.Request), extra ...func(*http.Request)) result {
	h.t.Helper()
	opts := []func(*http.Request){body}
	if sid != "" {
		opts = append(opts, h.withSID(sid))
	}
	return h.serve("POST", "/admin/users/"+id+"/"+action, append(opts, extra...)...)
}

func noBody() func(*http.Request) { return withBody(formType, nil) }

// mustBlock blocks account u as the owner and fails unless the answer is the 303
// to its section of the Blocked users page.
func (h *harness) mustBlock(owner string, u int64) {
	h.t.Helper()
	res := h.blockRoute(owner, itoa(u), "block", noBody())
	if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked#blocked-"+itoa(u) {
		h.t.Fatalf("blocking %d: %d %q %q", u, res.Status, res.Location(), res.Body)
	}
}

// mustUnblock unblocks account u as the owner and fails unless the answer is the
// 303 to the Blocked users page.
func (h *harness) mustUnblock(owner string, u int64) {
	h.t.Helper()
	res := h.blockRoute(owner, itoa(u), "unblock", noBody())
	if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked" {
		h.t.Fatalf("unblocking %d: %d %q %q", u, res.Status, res.Location(), res.Body)
	}
}

// blockedAt is the stored time of an account's block, and whether it is blocked.
func (h *harness) blockedAt(u int64) (int64, bool) {
	h.t.Helper()
	var at int64
	err := h.store.DB().QueryRow(`SELECT blocked_at FROM blocked_accounts WHERE github_id = ?`, u).Scan(&at)
	return at, err == nil
}

func TestBlock(t *testing.T) {
	t.Run("the owner blocks, and a repeat changes nothing", func(t *testing.T) {
		h, owner, _ := commentWorld(t)
		h.mint(8, "eight")
		res := h.blockRoute(owner, "8", "block", noBody())
		if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked#blocked-8" || res.Body != "" {
			t.Fatalf("block: %d %q %q, want a 303 to /admin/blocked#blocked-8", res.Status, res.Location(), res.Body)
		}
		if got := res.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		first, ok := h.blockedAt(8)
		if !ok || first != h.clock.Now().Unix() {
			t.Fatalf("blocked_at = %d (blocked %v), want the clock's %d", first, ok, h.clock.Now().Unix())
		}
		h.clock.Advance(time.Hour)
		h.mustBlock(owner, 8)
		if again, _ := h.blockedAt(8); again != first || h.count("blocked_accounts") != 1 {
			t.Errorf("a repeated block: blocked_at %d (want the first %d), %d rows", again, first, h.count("blocked_accounts"))
		}
		res = h.blockRoute(owner, "8", "unblock", noBody())
		if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked" || res.Body != "" {
			t.Fatalf("unblock: %d %q %q, want a 303 to /admin/blocked", res.Status, res.Location(), res.Body)
		}
		if _, ok := h.blockedAt(8); ok {
			t.Error("the account is still blocked after the unblock")
		}
		h.mustUnblock(owner, 8) // a repeat, and one for an account never blocked
		h.mustUnblock(owner, 12345)
	})

	t.Run("the author's own account is refused", func(t *testing.T) {
		h, owner, _ := commentWorld(t)
		res := h.blockRoute(owner, itoa(testOwnerID), "block", noBody())
		if res.Status != http.StatusBadRequest || !strings.Contains(res.Body, "The author cannot be blocked.") || res.Location() != "" {
			t.Errorf("blocking the author: %d %q %q, want a 400 saying so", res.Status, res.Body, res.Location())
		}
		if n := h.count("blocked_accounts"); n != 0 {
			t.Errorf("%d rows: the author was blocked", n)
		}
		// The author can still comment, so the refusal changed nothing.
		h.say(owner, "x", "the author still takes part", 0)
	})

	t.Run("ids that are not canonical", func(t *testing.T) {
		h, owner, _ := commentWorld(t)
		for _, id := range []string{"x", "0", "-5", "+5", "007", "5.0", "0x5", "5e1", "99999999999999999999", "%205", "5%20", "%E2%82%AC"} {
			for _, action := range []string{"block", "unblock"} {
				res := h.blockRoute(owner, id, action, noBody())
				if res.Status != http.StatusNotFound || res.Location() != "" {
					t.Errorf("POST /admin/users/%s/%s: %d %q, want a 404", id, action, res.Status, res.Location())
				}
			}
		}
		if n := h.count("blocked_accounts"); n != 0 {
			t.Errorf("%d rows written by ids that are not accounts", n)
		}
	})

	t.Run("an account that never signed in", func(t *testing.T) {
		h, owner, _ := commentWorld(t)
		h.mustBlock(owner, 123456)
		page := h.page(owner, "/admin/blocked")
		if !strings.Contains(page, `id="blocked-123456"`) || !strings.Contains(page, "GitHub user 123456") {
			t.Errorf("an account with no sign-in is not listed as 'GitHub user 123456':\n%s", page)
		}
	})

	t.Run("everyone but the owner is refused with nothing changed", func(t *testing.T) {
		h, owner, reader := commentWorld(t)
		h.mint(8, "eight")
		h.mustBlock(owner, 9)
		h.clock.Advance(time.Hour)
		for who, opts := range map[string][]func(*http.Request){
			"anonymous":                    nil,
			"a signed-in reader":           {h.withSID(reader)},
			"the owner, from another site": {h.withSID(owner), withHeader("Sec-Fetch-Site", "cross-site"), withHeader("Origin", "https://evil.example")},
		} {
			for _, c := range []struct{ id, action string }{{"8", "block"}, {"9", "unblock"}, {itoa(testOwnerID), "block"}} {
				res := h.serve("POST", "/admin/users/"+c.id+"/"+c.action, append([]func(*http.Request){noBody()}, opts...)...)
				if res.Status != http.StatusForbidden || res.Location() != "" {
					t.Errorf("%s POST /admin/users/%s/%s: %d %q, want a 403", who, c.id, c.action, res.Status, res.Location())
				}
			}
		}
		if _, ok := h.blockedAt(8); ok {
			t.Error("an account was blocked by a request the owner did not make")
		}
		if _, ok := h.blockedAt(9); !ok {
			t.Error("an account was unblocked by a request the owner did not make")
		}
		if res := h.serve("GET", "/admin/blocked", h.withSID(reader)); res.Status != http.StatusForbidden || hasMarker(res.Body) {
			t.Errorf("a reader's GET /admin/blocked: %d, want a 403 with no admin content", res.Status)
		}
		if res := h.serve("GET", "/admin/blocked"); res.Status != http.StatusSeeOther {
			t.Errorf("a visitor's GET /admin/blocked: %d, want a 303 to sign-in", res.Status)
		}
	})

	// The class (lesson #973): neither route has a field, and the body of a request is
	// never read, so no body of any encoding or size can steer or break either. Every
	// body of the remove form's corpus is sent to both, and each is the plain press.
	t.Run("a body of any kind is ignored", func(t *testing.T) {
		h, owner, _ := commentWorld(t)
		for _, c := range removeBodyRows() {
			res := h.blockRoute(owner, "8", "block", withBody(c.ctype, []byte(c.body)))
			if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked#blocked-8" {
				t.Errorf("block with %s: %d %q, want the plain 303", c.name, res.Status, res.Location())
			}
			if _, ok := h.blockedAt(8); !ok {
				t.Errorf("block with %s: the account is not blocked", c.name)
			}
			res = h.blockRoute(owner, "8", "unblock", withBody(c.ctype, []byte(c.body)))
			if res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked" {
				t.Errorf("unblock with %s: %d %q, want the plain 303", c.name, res.Status, res.Location())
			}
			if _, ok := h.blockedAt(8); ok {
				t.Errorf("unblock with %s: the account is still blocked", c.name)
			}
		}
		// A query string does not steer them either.
		res := h.serve("POST", "/admin/users/8/block?from=admin&id=9", h.withSID(owner), noBody())
		if res.Location() != "/admin/blocked#blocked-8" {
			t.Errorf("a query string changed the answer: %q", res.Location())
		}
		if _, ok := h.blockedAt(9); ok {
			t.Error("an id in the query string blocked another account")
		}
	})

	t.Run("methods", func(t *testing.T) {
		h, owner, _ := commentWorld(t)
		for _, m := range []string{"GET", "HEAD", "PUT", "DELETE"} {
			for _, action := range []string{"block", "unblock"} {
				if res := h.serve(m, "/admin/users/8/"+action, h.withSID(owner)); res.Status != http.StatusMethodNotAllowed {
					t.Errorf("%s /admin/users/8/%s: %d, want 405", m, action, res.Status)
				}
			}
		}
		if res := h.serve("POST", "/admin/blocked", h.withSID(owner), noBody()); res.Status != http.StatusMethodNotAllowed {
			t.Errorf("POST /admin/blocked: %d, want 405", res.Status)
		}
		for _, m := range []string{"GET", "HEAD"} {
			if res := h.serve(m, "/admin/blocked", h.withSID(owner)); res.Status != http.StatusOK {
				t.Errorf("%s /admin/blocked: %d, want 200", m, res.Status)
			}
		}
	})
}

// blockedWorld is a site with the owner, a reader (7), an account that will be
// blocked (8) and one that downvotes (9), on two published reports.
type blockedWorld struct {
	h                         *harness
	owner, reader, bad, other string
	top, own                  int64
}

func newBlockedWorld(t *testing.T) *blockedWorld {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "y", "Report Y", "published", reportHTML(300, "y"))
	w := &blockedWorld{h: h, owner: owner, reader: reader, bad: h.mint(8, "bad-actor"), other: h.mint(9, "downvoter")}
	w.top = h.say(reader, "x", "a visible comment", 0)
	w.own = h.say(w.bad, "x", "the account's earlier comment", 0)
	h.mustVote(w.bad, "x", "up")
	h.mustVote(reader, "x", "up")
	h.mustVote(w.other, "x", "down")
	return w
}

// state is every number a refused attempt must leave alone.
type blockedState struct {
	comments, feedback, votes int
	kind8, kind8y             string
	likes, dislikes           int
	public                    string
}

func (w *blockedWorld) state(t *testing.T) blockedState {
	t.Helper()
	h := w.h
	rep := h.reportOf("x")
	pub, err := h.app.votes.Public(bg, rep, 8)
	if err != nil {
		t.Fatal(err)
	}
	tal, err := h.app.votes.OwnerTally(bg, rep)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Likes != tal.Likes {
		t.Fatalf("Public likes %d and OwnerTally likes %d differ", pub.Likes, tal.Likes)
	}
	return blockedState{
		comments: h.count("comments"), feedback: h.count("feedback"), votes: h.count("votes"),
		kind8: h.voteKind("x", 8), kind8y: h.voteKind("y", 8), likes: tal.Likes, dislikes: tal.Dislikes,
		public: likesLine(t, h.serve("GET", "/reports/x").Body),
	}
}

func TestBlockedRefusals(t *testing.T) {
	w := newBlockedWorld(t)
	h := w.h
	before := w.state(t)
	if before.kind8 != "up" || before.likes != 2 || before.dislikes != 1 || before.public != "2 likes" || before.comments != 2 {
		t.Fatalf("INVALID: the starting state is not the one the comparison assumes: %+v", before)
	}
	h.mustBlock(w.owner, 8)

	// Blocking changes no vote number.
	if got := w.state(t); got.likes != before.likes || got.dislikes != before.dislikes || got.public != before.public || got.kind8 != "up" {
		t.Errorf("blocking changed a vote number: %+v, before %+v", got, before)
	}

	for _, c := range []struct {
		name string
		do   func() result
	}{
		{"a top-level comment", func() result { return h.postComment(w.bad, "x", commentForm("new top level", 0)) }},
		{"a reply to a visible comment", func() result { return h.postComment(w.bad, "x", commentForm("new reply", w.top)) }},
		{"a reply to its own comment", func() result { return h.postComment(w.bad, "x", commentForm("new self reply", w.own)) }},
		{"a reply to a comment that is not there", func() result { return h.postComment(w.bad, "x", commentForm("new reply to nothing", 9999)) }},
		{"feedback", func() result { return h.postFeedback(w.bad, "x", feedbackForm("a private message")) }},
		{"a vote up on the report it voted on", func() result { return h.postVote(w.bad, "x", "vote=up") }},
		{"a vote down on the report it voted on", func() result { return h.postVote(w.bad, "x", "vote=down") }},
		{"a withdrawal", func() result { return h.postVote(w.bad, "x", "vote=none") }},
		{"a vote up on a report it never voted on", func() result { return h.postVote(w.bad, "y", "vote=up") }},
		{"a vote down on a report it never voted on", func() result { return h.postVote(w.bad, "y", "vote=down") }},
		{"a withdrawal of no vote", func() result { return h.postVote(w.bad, "y", "vote=none") }},
	} {
		res := c.do()
		if res.Status != http.StatusForbidden || res.Location() != "" || !strings.Contains(res.Body, blockedRefusal) {
			t.Errorf("%s: %d %q (Location %q), want a 403 saying %q", c.name, res.Status, res.Body, res.Location(), blockedRefusal)
		}
		if got := res.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", c.name, got)
		}
	}
	after := w.state(t)
	if after != (blockedState{comments: before.comments, feedback: before.feedback, votes: before.votes, kind8: "up", kind8y: "",
		likes: before.likes, dislikes: before.dislikes, public: before.public}) {
		t.Errorf("after eleven refused attempts: %+v, want the starting state %+v", after, before)
	}

	// A body the handler would refuse for another reason is still that refusal.
	if res := h.postComment(w.bad, "x", "body="); res.Status != http.StatusBadRequest {
		t.Errorf("an empty comment from a blocked account: %d, want the usual 400", res.Status)
	}
	// Control: the other readers are not refused, and the refusal is not the report's.
	h.say(w.reader, "x", "the reader still takes part", 0)
	h.mustVote(w.other, "x", "up")
	h.sendFeedback(w.reader, "x", "from the reader")

	// Unblocked, it comments, sends feedback and votes again.
	h.mustUnblock(w.owner, 8)
	h.say(w.bad, "x", "back again", w.top)
	h.sendFeedback(w.bad, "x", "back again, privately")
	h.mustVote(w.bad, "x", "down")
	h.mustVote(w.bad, "x", "none")
}

// TestBlockedReadFault: a store fault while the answer is chosen is a 503 with
// nothing stored, on every route, and is never a 403 the store did not decide or a
// success. The table is replaced by a view that raises an error whenever it is
// evaluated, so the statements that reach it fail, and so does the read that
// follows a statement that did not.
func TestBlockedReadFault(t *testing.T) {
	w := newBlockedWorld(t)
	h := w.h
	before := w.state(t)
	h.exec(`DROP TABLE blocked_accounts`)
	h.exec(`CREATE VIEW blocked_accounts AS SELECT abs(-9223372036854775807 - 1) AS github_id, 0 AS blocked_at`)
	for _, c := range []struct {
		name string
		res  result
	}{
		{"a comment", h.postComment(w.other, "x", commentForm("while the table is failing", 0))},
		{"a reply to a comment that is not there", h.postComment(w.other, "x", commentForm("to nothing", 9999))},
		{"feedback", h.postFeedback(w.other, "x", feedbackForm("while the table is failing"))},
		{"a vote down", h.postVote(w.other, "y", "vote=down")},
		{"a withdrawal of no vote", h.postVote(w.other, "y", "vote=none")},
		{"a withdrawal of a vote", h.postVote(w.other, "x", "vote=none")},
	} {
		if c.res.Status != http.StatusServiceUnavailable || c.res.Location() != "" {
			t.Errorf("%s: %d %q (Location %q), want a 503", c.name, c.res.Status, c.res.Body, c.res.Location())
		}
	}
	h.exec(`DROP VIEW blocked_accounts`)
	h.exec(`CREATE TABLE blocked_accounts (github_id INTEGER PRIMARY KEY CHECK (github_id > 0), blocked_at INTEGER NOT NULL) STRICT`)
	if got := w.state(t); got.comments != before.comments || got.feedback != before.feedback || got.votes != before.votes {
		t.Errorf("a refused write left rows behind: %+v, before %+v", got, before)
	}
	if h.voteKind("x", 9) != "down" {
		t.Errorf("a failed withdrawal removed the vote")
	}
}

// blockedSection is one <section class="blocked-account"> of the Blocked users page.
type blockedSection struct {
	ID   int64
	Own  string
	List string // the comments list
	Mail string // the feedback list
}

var (
	blockedSectionRE  = regexp.MustCompile(`(?s)<section class="blocked-account" id="blocked-(\d+)">(.*?)</section>`)
	blockedCommentsRE = regexp.MustCompile(`(?s)<ul class="blocked-comments">(.*?)</ul>`)
	blockedFeedbackRE = regexp.MustCompile(`(?s)<ul class="blocked-feedback">(.*?)</ul>`)
	liRE              = regexp.MustCompile(`(?s)<li>(.*?)</li>`)
)

func blockedSections(page string) []blockedSection {
	var out []blockedSection
	for _, m := range blockedSectionRE.FindAllStringSubmatch(page, -1) {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		s := blockedSection{ID: id, Own: m[2]}
		if c := blockedCommentsRE.FindStringSubmatch(m[2]); c != nil {
			s.List = c[1]
		}
		if f := blockedFeedbackRE.FindStringSubmatch(m[2]); f != nil {
			s.Mail = f[1]
		}
		out = append(out, s)
	}
	return out
}

// entryWith is the one <li> of list that holds text, and fails the test unless there is exactly one.
func entryWith(t *testing.T, what, list, text string) string {
	t.Helper()
	var hit []string
	for _, m := range liRE.FindAllStringSubmatch(list, -1) {
		if strings.Contains(m[1], text) {
			hit = append(hit, m[1])
		}
	}
	if len(hit) != 1 {
		t.Fatalf("%s: %d entries hold %q, want exactly one, in:\n%s", what, len(hit), text, list)
	}
	return hit[0]
}

func TestBlockedPage(t *testing.T) {
	h, owner, reader := commentWorld(t)
	h.mustUpload(owner, "z", "Report Z", "published", reportHTML(300, "z"))
	h.mustUpload(owner, "gone", "Gone Report", "published", reportHTML(300, "gone"))
	bad, other := h.mint(8, "bad-eight"), h.mint(9, "bad-nine")

	// What the first account wrote, on three reports, at several depths.
	ra := h.say(reader, "x", "visible comment by the reader", 0)
	chain := grow(t, h, "x", 7, 0, 7, "Deep") // seven levels: the seventh is below the cap
	xa := h.say(bad, "x", "bad-eight top level", 0)
	xr := h.say(bad, "x", "bad-eight reply to a visible comment", ra)
	xdeep := h.say(bad, "x", "bad-eight reply below the cap", chain[6])
	xh := h.say(bad, "x", "bad-eight parent of itself", 0)
	xhr := h.say(bad, "x", "bad-eight answering itself", xh)
	xrem := h.say(bad, "z", "bad-eight text that will be removed", 0)
	rg := h.say(reader, "gone", "reader on a report that comes down", 0)
	xg := h.say(bad, "gone", "bad-eight answers on the report that comes down", rg)
	h.sendFeedback(bad, "x", "bad-eight private note on x")
	h.sendFeedback(bad, "gone", "bad-eight private note on gone")
	// And the second account, with a comment and a message of its own.
	yz := h.say(other, "z", "bad-nine on z", 0)
	h.sendFeedback(other, "z", "bad-nine private note")
	h.mustAct(owner, "gone", "unpublish")

	// Nobody is blocked yet.
	if page := h.page(owner, "/admin/blocked"); !strings.Contains(page, "Nobody is blocked.") || len(blockedSections(page)) != 0 {
		t.Errorf("the page with nobody blocked:\n%s", page)
	}
	// Blocks, a second apart so the order is known: 8, then 9, then an id that never signed in.
	h.mustBlock(owner, 8)
	h.clock.Advance(time.Second)
	h.mustBlock(owner, 9)
	h.clock.Advance(time.Second)
	h.mustBlock(owner, 55555)
	// The author removes one of the blocked account's comments, from its own page, by
	// the form the page draws.
	removeAction := regexp.MustCompile(`<form method="post" action="(/admin/comments/` + itoa(xrem) + `/remove)">\s*<input type="hidden" name="from" value="(\w+)">`)
	m := removeAction.FindStringSubmatch(h.page(owner, "/admin/blocked"))
	if m == nil || m[2] != "blocked" {
		t.Fatalf("the Blocked users page draws no remove form for the comment with from=blocked: %v", m)
	}
	if res := h.serve("POST", m[1], h.withSID(owner), withBody(formType, []byte("from="+m[2]))); res.Status != http.StatusSeeOther || res.Location() != "/admin/blocked" {
		t.Fatalf("pressing it: %d %q, want a 303 back to /admin/blocked", res.Status, res.Location())
	}

	res := h.serve("GET", "/admin/blocked", h.withSID(owner))
	if res.Status != http.StatusOK || !hasMarker(res.Body) || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /admin/blocked: %d marker %v Cache-Control %q", res.Status, hasMarker(res.Body), res.Header.Get("Cache-Control"))
	}
	page := res.Body
	if strings.Contains(page, "bad-eight text that will be removed") {
		t.Error("the text of a removed comment is on the Blocked users page")
	}
	secs := blockedSections(page)
	if len(secs) != 3 || secs[0].ID != 8 || secs[1].ID != 9 || secs[2].ID != 55555 {
		t.Fatalf("sections %+v, want one each for 8, 9 and 55555, oldest block first", secs)
	}
	s8, s9, s0 := secs[0], secs[1], secs[2]

	// One disjoint section per account: each holds its own login, comments, messages and Unblock.
	for _, c := range []struct {
		s        blockedSection
		own      []string
		notOther []string
	}{
		{s8, []string{"bad-eight", "bad-eight top level", "bad-eight private note on x", "bad-eight private note on gone"}, []string{"bad-nine", "55555"}},
		{s9, []string{"bad-nine", "bad-nine on z", "bad-nine private note"}, []string{"bad-eight", "55555"}},
		{s0, []string{"GitHub user 55555", "No comments.", "No feedback."}, []string{"bad-eight", "bad-nine"}},
	} {
		for _, s := range c.own {
			if !strings.Contains(c.s.Own, s) {
				t.Errorf("section %d lacks %q:\n%s", c.s.ID, s, c.s.Own)
			}
		}
		for _, s := range c.notOther {
			if strings.Contains(c.s.Own, s) {
				t.Errorf("section %d holds %q, which is another account's", c.s.ID, s)
			}
		}
		if want := `action="/admin/users/` + itoa(c.s.ID) + `/unblock"`; !strings.Contains(c.s.Own, want) || !strings.Contains(c.s.Own, ">Unblock</button>") {
			t.Errorf("section %d has no Unblock form (%s)", c.s.ID, want)
		}
	}
	if len(liRE.FindAllString(s8.List, -1)) != 7 || len(liRE.FindAllString(s8.Mail, -1)) != 2 || len(liRE.FindAllString(s9.List, -1)) != 1 || len(liRE.FindAllString(s9.Mail, -1)) != 1 {
		t.Errorf("entries: account 8 has %d comments and %d messages, account 9 has %d and %d, want 7 and 2, 1 and 1",
			len(liRE.FindAllString(s8.List, -1)), len(liRE.FindAllString(s8.Mail, -1)), len(liRE.FindAllString(s9.List, -1)), len(liRE.FindAllString(s9.Mail, -1)))
	}

	// One disjoint <li> per comment: its text, its report, and where the comment it answered is.
	link := func(href string) string { return `href="` + href + `"` }
	e := entryWith(t, "a top-level comment", s8.List, "bad-eight top level")
	if !strings.Contains(e, link("/reports/x")+">Report X</a>") || strings.Contains(e, "#comment-") || strings.Contains(e, "answer") {
		t.Errorf("a top-level comment's entry:\n%s", e)
	}
	e = entryWith(t, "a reply to a visible comment", s8.List, "bad-eight reply to a visible comment")
	if !strings.Contains(e, link("/reports/x#comment-"+itoa(ra))) || !strings.Contains(e, link("/reports/x")+">Report X</a>") {
		t.Errorf("a reply's entry has no link to the comment it answered:\n%s", e)
	}
	e = entryWith(t, "a reply below the depth cap", s8.List, "bad-eight reply below the cap")
	if want := link("/reports/x/comments/" + itoa(chain[4]) + "#comment-" + itoa(chain[6])); !strings.Contains(e, want) {
		t.Errorf("a reply below the cap does not link to its parent's branch page (%s):\n%s", want, e)
	}
	e = entryWith(t, "a reply to its own hidden comment", s8.List, "bad-eight answering itself")
	if !strings.Contains(e, "in reply to a hidden comment") || strings.Contains(e, "#comment-") || strings.Contains(e, "<a href=\"/reports/x#") {
		t.Errorf("a reply whose parent is drawn nowhere should say so and link nowhere:\n%s", e)
	}
	e = entryWith(t, "a reply on an unpublished report", s8.List, "bad-eight answers on the report that comes down")
	if !strings.Contains(e, link("/admin/reports/gone")+">Gone Report</a>") || strings.Contains(e, "href=\"/reports/gone") || strings.Contains(e, "#comment-") {
		t.Errorf("an unpublished report's comment should link to its admin page and not to a parent anchor:\n%s", e)
	}
	// The removed comment is listed with no text and no remove form; the rest have one.
	e = entryWith(t, "the removed comment", s8.List, "removed</span>")
	if !strings.Contains(e, ">Report Z</a>") || strings.Contains(e, "<form") || strings.Contains(e, "bad-eight") {
		t.Errorf("a removed comment's entry:\n%s", e)
	}
	if n := strings.Count(page, `<input type="hidden" name="from" value="blocked">`); n != 7 {
		t.Errorf("%d remove forms on the page, want one for each of the seven comments that are not removed", n)
	}
	for _, id := range []int64{xa, xr, xdeep, xh, xhr, xg, yz} {
		if !regexp.MustCompile(`action="/admin/comments/` + itoa(id) + `/remove">\s*<input type="hidden" name="from" value="blocked">`).MatchString(page) {
			t.Errorf("no remove form with from=blocked for comment %d", id)
		}
	}
	if strings.Contains(page, `action="/admin/comments/`+itoa(xrem)+`/remove"`) {
		t.Error("a removed comment carries a remove form")
	}
	// The messages are in the section, each with its report (the admin page, where it is answered).
	e = entryWith(t, "a message", s8.Mail, "bad-eight private note on gone")
	if !strings.Contains(e, `href="/admin/reports/gone#feedback-`) || !strings.Contains(e, ">Gone Report</a>") {
		t.Errorf("a message's entry:\n%s", e)
	}
	// No id a reader page could carry: no comment-<n> or feedback-<n> element id.
	if regexp.MustCompile(`id="(comment|feedback)-\d`).MatchString(page) {
		t.Errorf("the page has an element id of a comment or a message")
	}

	// A read that fails is an error page, not an empty list.
	h.app.tree = func(ctx context.Context, id int64) ([]*comments.Node, error) { return nil, errors.New("injected") }
	if res := h.serve("GET", "/admin/blocked", h.withSID(owner)); res.Status != http.StatusInternalServerError || strings.Contains(res.Body, "Nobody is blocked") {
		t.Errorf("the page with a failing thread read: %d", res.Status)
	}
	h.app.tree = h.app.comments.Tree
	h.exec(`ALTER TABLE feedback RENAME TO feedback_gone`)
	if res := h.serve("GET", "/admin/blocked", h.withSID(owner)); res.Status != http.StatusInternalServerError || strings.Contains(res.Body, "Nobody is blocked") {
		t.Errorf("the page with a failing feedback read: %d", res.Status)
	}
	h.exec(`ALTER TABLE feedback_gone RENAME TO feedback`)
	h.exec(`ALTER TABLE blocked_accounts RENAME TO blocked_gone`)
	if res := h.serve("GET", "/admin/blocked", h.withSID(owner)); res.Status != http.StatusInternalServerError || strings.Contains(res.Body, "Nobody is blocked") {
		t.Errorf("the page with a failing blocked read: %d", res.Status)
	}
	h.exec(`ALTER TABLE blocked_gone RENAME TO blocked_accounts`)

	// The way in: the admin landing page links to it.
	if landing := h.page(owner, "/admin"); !strings.Contains(landing, `<a href="/admin/blocked">Blocked users</a>`) {
		t.Errorf("/admin has no link to the Blocked users page:\n%s", landing)
	}
}
