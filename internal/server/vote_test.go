package server

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const formType = "application/x-www-form-urlencoded"

// postVote sends the vote form for slug. sid "" is a visitor with no session.
func (h *harness) postVote(sid, slug, body string, extra ...func(*http.Request)) result {
	h.t.Helper()
	opts := []func(*http.Request){withBody(formType, []byte(body))}
	if sid != "" {
		opts = append(opts, h.withSID(sid))
	}
	return h.serve("POST", "/reports/"+slug+"/vote", append(opts, extra...)...)
}

// mustVote sends vote=<value> and fails unless it is answered with the 303 back
// to the report.
func (h *harness) mustVote(sid, slug, value string) result {
	h.t.Helper()
	res := h.postVote(sid, slug, "vote="+value)
	if res.Status != http.StatusSeeOther || res.Location() != "/reports/"+slug+"#votes" {
		h.t.Fatalf("vote=%s on %s: %d %q %q", value, slug, res.Status, res.Location(), res.Body)
	}
	return res
}

var likesRE = regexp.MustCompile(`<p class="likes">([^<]*)</p>`)

// likesLine is the text of the page's like line ("1 like", "3 likes").
func likesLine(t *testing.T, body string) string {
	t.Helper()
	m := likesRE.FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("want exactly one like line, found %d in:\n%s", len(m), body)
	}
	return m[0][1]
}

func (h *harness) voteKind(slug string, github int64) string {
	h.t.Helper()
	var kind string
	err := h.store.DB().QueryRow(
		"SELECT kind FROM votes WHERE github_id = ? AND report_id = (SELECT id FROM reports WHERE slug = ?)", github, slug).Scan(&kind)
	if err != nil {
		return ""
	}
	return kind
}

func TestVoteSignedInFlow(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	reader := h.mint(7, "reader")
	const form = `<form method="post" action="/reports/x/vote">`

	page := h.serve("GET", "/reports/x", h.withSID(reader))
	if !strings.Contains(page.Body, form) || !strings.Contains(page.Body, `value="up">Thumbs up</button>`) ||
		!strings.Contains(page.Body, `value="down">Thumbs down</button>`) {
		t.Fatalf("a signed-in reader is offered no vote form:\n%s", page.Body)
	}
	if strings.Contains(page.Body, "Take back my vote") || strings.Contains(page.Body, "Your vote") {
		t.Errorf("a reader with no vote is shown one:\n%s", page.Body)
	}
	if got := likesLine(t, page.Body); got != "0 likes" {
		t.Errorf("like line = %q", got)
	}

	// The same 303, with nothing in it, whatever was cast.
	var first result
	for i, step := range []struct{ value, likes, mine string }{
		{"up", "1 like", "Your vote: thumbs up."},
		{"up", "1 like", "Your vote: thumbs up."}, // pressing it again changes nothing
		{"down", "0 likes", "Your vote: thumbs down."},
		{"down", "0 likes", "Your vote: thumbs down."},
		{"up", "1 like", "Your vote: thumbs up."},
		{"none", "0 likes", ""},
		{"none", "0 likes", ""}, // withdrawing twice
	} {
		res := h.mustVote(reader, "x", step.value)
		if i == 0 {
			first = res
		}
		if res.Body != "" || res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("vote=%s: body %q Cache-Control %q", step.value, res.Body, res.Header.Get("Cache-Control"))
		}
		if d := sameResponse(first, res); d != "" {
			t.Errorf("vote=%s answers differently from the first vote: %s", step.value, d)
		}
		for _, p := range []string{"/reports/x", "/"} {
			got := h.serve("GET", p, h.withSID(reader))
			if likesLine(t, got.Body) != step.likes {
				t.Errorf("after vote=%s, GET %s like line = %q, want %q", step.value, p, likesLine(t, got.Body), step.likes)
			}
			if has := strings.Contains(got.Body, "Your vote"); has != (step.mine != "") || (step.mine != "" && !strings.Contains(got.Body, step.mine)) {
				t.Errorf("after vote=%s, GET %s: your-vote sentence wrong (want %q):\n%s", step.value, p, step.mine, got.Body)
			}
			if has := strings.Contains(got.Body, `value="none">Take back my vote</button>`); has != (step.mine != "") {
				t.Errorf("after vote=%s, GET %s: take-back button present = %v", step.value, p, has)
			}
		}
	}
	if n := h.count("votes"); n != 0 {
		t.Fatalf("%d vote rows after withdrawing", n)
	}

	// The owner votes like anybody else, and the public sees only the like.
	h.mustVote(owner, "x", "up")
	h.mustVote(reader, "x", "down")
	anon := h.serve("GET", "/reports/x")
	if got := likesLine(t, anon.Body); got != "1 like" {
		t.Errorf("anonymous like line = %q", got)
	}
	if !strings.Contains(anon.Body, `class="vote-hint">Sign in to vote.</p>`) || strings.Contains(anon.Body, "<form method=\"post\" action=\"/reports/") {
		t.Errorf("anonymous visitors are offered a vote form, or no hint:\n%s", anon.Body)
	}
	tally := h.serve("GET", "/admin/reports/x", h.withSID(owner))
	if !strings.Contains(tally.Body, `<p class="tally">1 like</p>`) || !strings.Contains(tally.Body, `<p class="tally">1 dislike</p>`) {
		t.Errorf("the owner's tally:\n%s", tally.Body)
	}
}

func TestVoteAnonymousRefused(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))

	// The page an anonymous visitor is shown has the like count and a hint, and no vote form.
	for _, p := range []string{"/reports/x", "/"} {
		res := h.serve("GET", p)
		if res.Status != 200 || strings.Contains(res.Body, "/vote") || strings.Contains(res.Body, `name="vote"`) ||
			!strings.Contains(res.Body, "Sign in to vote.") || likesLine(t, res.Body) != "0 likes" {
			t.Errorf("anonymous GET %s: %d\n%s", p, res.Status, res.Body)
		}
	}

	expired := h.mint(7, "reader")
	h.clock.Advance(31 * 24 * time.Hour) // past the 30-day session
	unknown := strings.Repeat("A", 43)   // grammar-valid, no such session
	for name, sid := range map[string]string{
		"no session": "", "an expired session": expired, "an unknown session": unknown, "a malformed cookie": "garbage",
	} {
		for _, value := range []string{"up", "down", "none"} {
			res := h.postVote(sid, "x", "vote="+value)
			if res.Status != http.StatusSeeOther || res.Location() != "/reports/x#votes" || res.Body != "" {
				t.Errorf("%s, vote=%s: %d %q %q, want 303 to /reports/x#votes", name, value, res.Status, res.Location(), res.Body)
			}
			if res.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s, vote=%s: Cache-Control %q", name, value, res.Header.Get("Cache-Control"))
			}
		}
	}
	// The expired reader is shown the anonymous page too.
	if res := h.serve("GET", "/reports/x", h.withSID(expired)); strings.Contains(res.Body, `name="vote"`) {
		t.Error("an expired session is offered the vote form")
	}
	if n := h.count("votes"); n != 0 {
		t.Fatalf("%d vote rows written for visitors who are not signed in", n)
	}
	// Control: the same request with a live session is taken.
	live := h.mint(8, "reader8")
	h.mustVote(live, "x", "up")
	if n := h.count("votes"); n != 1 {
		t.Fatalf("control: %d vote rows", n)
	}
}

func TestVoteCrossSiteRefused(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	reader := h.mint(7, "reader")

	for _, hdr := range [][2]string{
		{"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}, {"Origin", "https://evil.example"},
	} {
		for _, value := range []string{"up", "down", "none"} {
			res := h.postVote(reader, "x", "vote="+value, withHeader(hdr[0], hdr[1]))
			if res.Status != http.StatusForbidden {
				t.Errorf("%s: %s, vote=%s: %d, want 403", hdr[0], hdr[1], value, res.Status)
			}
		}
	}
	if n := h.count("votes"); n != 0 {
		t.Fatalf("%d vote rows written by requests from another site", n)
	}

	// Controls: the site's own forms carry these headers, and are taken.
	for _, hdr := range [][2]string{{"Sec-Fetch-Site", "same-origin"}, {"Origin", h.cfg.Origin}} {
		if res := h.postVote(reader, "x", "vote=up", withHeader(hdr[0], hdr[1])); res.Status != http.StatusSeeOther {
			t.Errorf("control %s: %s: %d", hdr[0], hdr[1], res.Status)
		}
	}
	if n := h.count("votes"); n != 1 {
		t.Fatalf("control: %d vote rows", n)
	}
	// A cross-site withdrawal leaves the vote where it was.
	if res := h.postVote(reader, "x", "vote=none", withHeader("Sec-Fetch-Site", "cross-site")); res.Status != 403 || h.voteKind("x", 7) != "up" {
		t.Errorf("cross-site withdrawal: %d, vote now %q", res.Status, h.voteKind("x", 7))
	}
}

func TestVoteHiddenReportsAreIndistinguishable(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	reader := h.mint(7, "reader")
	h.mustUpload(owner, "live", "Live", "published", reportHTML(300, "live"))
	h.mustUpload(owner, "drafty", "Drafty", "draft", reportHTML(300, "drafty"))
	h.mustUpload(owner, "taken-down", "Taken down", "published", reportHTML(300, "down"))
	h.mustAct(owner, "taken-down", "unpublish")
	h.mustUpload(owner, "deleted", "Deleted", "published", reportHTML(300, "deleted"))
	h.mustAct(owner, "deleted", "delete")

	// Control: a published report takes the vote.
	h.mustVote(reader, "live", "up")
	h.mustVote(reader, "live", "none")

	baseline := h.postVote("", "unknown-slug", "vote=up")
	if baseline.Status != 404 || baseline.Body != "404 page not found\n" {
		t.Fatalf("baseline: %d %q", baseline.Status, baseline.Body)
	}
	for viewer, sid := range map[string]string{"anonymous": "", "reader": reader, "owner": owner} {
		for _, slug := range []string{"drafty", "taken-down", "deleted", "unknown-slug", "UPPER-CASE", strings.Repeat("a", 81)} {
			for _, body := range []string{"vote=up", "vote=down", "vote=none", "vote=sideways", ""} {
				if d := sameResponse(h.postVote(sid, slug, body), baseline); d != "" {
					t.Errorf("%s: POST /reports/%s/vote %q differs from an unknown slug: %s", viewer, slug, body, d)
				}
			}
		}
	}
	if n := h.count("votes"); n != 0 {
		t.Fatalf("%d vote rows attached to a report nobody can see", n)
	}
}

func TestVoteBadFormRefused(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	reader := h.mint(7, "reader")

	pad := func(n int) string { return "vote=up&pad=" + strings.Repeat("a", n-len("vote=up&pad=")) }
	bad := []struct {
		name string
		post func() result
		want int
	}{
		{"no vote field", func() result { return h.postVote(reader, "x", "") }, 400},
		{"another field only", func() result { return h.postVote(reader, "x", "kind=up") }, 400},
		{"repeated", func() result { return h.postVote(reader, "x", "vote=up&vote=down") }, 400},
		{"repeated alike", func() result { return h.postVote(reader, "x", "vote=up&vote=up") }, 400},
		{"unknown value", func() result { return h.postVote(reader, "x", "vote=sideways") }, 400},
		{"empty value", func() result { return h.postVote(reader, "x", "vote=") }, 400},
		{"wrong case", func() result { return h.postVote(reader, "x", "vote=UP") }, 400},
		{"padded value", func() result { return h.postVote(reader, "x", "vote=up%20") }, 400},
		{"query string only", func() result {
			return h.serve("POST", "/reports/x/vote?vote=up", h.withSID(reader), withBody(formType, nil))
		}, 400},
		{"query string and no body at all", func() result {
			return h.serve("POST", "/reports/x/vote?vote=down", h.withSID(reader))
		}, 400},
		{"not a form", func() result {
			return h.serve("POST", "/reports/x/vote", h.withSID(reader), withBody("application/json", []byte(`{"vote":"up"}`)))
		}, 400},
		{"plain text", func() result {
			return h.serve("POST", "/reports/x/vote", h.withSID(reader), withBody("text/plain", []byte("vote=up")))
		}, 400},
		{"body over 1 KiB", func() result { return h.postVote(reader, "x", pad(1025)) }, 400},
		{"body far over 1 KiB", func() result { return h.postVote(reader, "x", pad(64<<10)) }, 400},
	}
	for _, c := range bad {
		res := c.post()
		if res.Status != c.want {
			t.Errorf("%s: %d %q, want %d", c.name, res.Status, res.Body, c.want)
		}
		if res.Location() != "" {
			t.Errorf("%s: a refused form carries Location %q", c.name, res.Location())
		}
	}
	if n := h.count("votes"); n != 0 {
		t.Fatalf("%d vote rows written by forms that were refused", n)
	}

	// Controls: exactly 1 KiB is taken; the query string is ignored when the
	// body carries the one value.
	if res := h.postVote(reader, "x", pad(1024)); res.Status != http.StatusSeeOther {
		t.Errorf("a body of exactly 1 KiB: %d", res.Status)
	}
	res := h.serve("POST", "/reports/x/vote?vote=up", h.withSID(reader), withBody(formType, []byte("vote=down")))
	if res.Status != http.StatusSeeOther || h.voteKind("x", 7) != "down" {
		t.Errorf("body vote=down with ?vote=up: %d, vote now %q", res.Status, h.voteKind("x", 7))
	}
}

func TestVoteIdentityUnavailable(t *testing.T) {
	t.Run("the session lookup fails but the library works", func(t *testing.T) {
		h := newHarness(t, false)
		owner := h.mint(testOwnerID, "the-owner")
		h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
		reader := h.mint(7, "reader")
		h.mustVote(reader, "x", "up") // control: taken while sessions work

		if _, err := h.store.DB().Exec("DROP TABLE sessions"); err != nil {
			t.Fatal(err)
		}
		unknown := strings.Repeat("A", 43)
		for name, sid := range map[string]string{"a live session": reader, "an unknown session": unknown} {
			for _, value := range []string{"down", "none", "up"} {
				res := h.postVote(sid, "x", "vote="+value)
				if res.Status != http.StatusServiceUnavailable || res.Location() != "" {
					t.Errorf("%s, vote=%s: %d %q, want 503 and no redirect", name, value, res.Status, res.Location())
				}
				if res.Header.Get("Cache-Control") != "no-store" {
					t.Errorf("%s: Cache-Control %q", name, res.Header.Get("Cache-Control"))
				}
			}
		}
		if got := h.voteKind("x", 7); got != "up" {
			t.Errorf("a vote answered 503 changed the kept vote to %q", got)
		}
		// Control: with no cookie nobody is looked up, so it is a plain anonymous refusal.
		if res := h.postVote("", "x", "vote=down"); res.Status != http.StatusSeeOther {
			t.Errorf("anonymous with the session table gone: %d", res.Status)
		}
	})

	t.Run("the store is closed", func(t *testing.T) {
		h := newHarness(t, false)
		owner := h.mint(testOwnerID, "the-owner")
		h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
		reader := h.mint(7, "reader")
		h.store.Close()
		res := h.postVote(reader, "x", "vote=up")
		if res.Status != http.StatusServiceUnavailable || res.Location() != "" {
			t.Errorf("store closed: %d %q", res.Status, res.Location())
		}
		if n := h.reopenCount("votes"); n != 0 {
			t.Errorf("%d vote rows after a refused vote on a closed store", n)
		}
	})
}

func TestReaderVotesReadFails(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	reader := h.mint(7, "reader")
	for _, p := range []string{"/", "/reports/x"} {
		if res := h.serve("GET", p, h.withSID(reader)); res.Status != 200 {
			t.Fatalf("control: GET %s = %d", p, res.Status)
		}
	}
	if _, err := h.store.DB().Exec("DROP TABLE votes"); err != nil {
		t.Fatal(err)
	}

	for viewer, opts := range map[string][]func(*http.Request){
		"anonymous": nil, "reader": {h.withSID(reader)}, "owner": {h.withSID(owner)},
	} {
		for _, p := range []string{"/", "/reports/x"} {
			res := h.serve("GET", p, opts...)
			if res.Status != 503 || !strings.Contains(res.Body, "Reports cannot be shown right now") {
				t.Errorf("%s: GET %s = %d, want the 503 page:\n%s", viewer, p, res.Status, res.Body)
			}
			if strings.Contains(res.Body, "<iframe") || strings.Contains(res.Body, "like") || strings.Contains(res.Body, "<form method=\"post\" action=\"/reports/") {
				t.Errorf("%s: GET %s shows a report, a count or a vote form without being able to read the votes:\n%s", viewer, p, res.Body)
			}
			if got := res.Header.Get("Cache-Control"); got != "private, no-cache" {
				t.Errorf("%s: GET %s Cache-Control = %q", viewer, p, got)
			}
		}
	}
	// A write cannot land either, and says so.
	if res := h.postVote(reader, "x", "vote=up"); res.Status != http.StatusServiceUnavailable {
		t.Errorf("a vote with no votes table: %d", res.Status)
	}
	// The owner's tally says it could not be read, with the admin convention.
	if res := h.serve("GET", "/admin/reports/x", h.withSID(owner)); res.Status != 500 || strings.Contains(res.Body, "dislike") {
		t.Errorf("the owner's report page with no votes table: %d\n%s", res.Status, res.Body)
	}
}

func TestVotesFollowReportLifecycle(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	h.mustUpload(owner, "x", "Report X", "published", reportHTML(300, "x"))
	up, down := h.mint(7, "upvoter"), h.mint(8, "downvoter")
	h.mustVote(up, "x", "up")
	h.mustVote(down, "x", "down")
	id, _, _, _ := h.reportRow("x")

	likes := func(when string, want string) {
		t.Helper()
		if got := likesLine(t, h.serve("GET", "/reports/x").Body); got != want {
			t.Errorf("%s: like line %q, want %q", when, got, want)
		}
	}
	tally := func(when, likes, dislikes string) {
		t.Helper()
		res := h.serve("GET", "/admin/reports/x", h.withSID(owner))
		if !strings.Contains(res.Body, `<p class="tally">`+likes+`</p>`) || !strings.Contains(res.Body, `<p class="tally">`+dislikes+`</p>`) {
			t.Errorf("%s: the owner's tally, want %s and %s:\n%s", when, likes, dislikes, res.Body)
		}
	}
	likes("before anything", "1 like")
	tally("before anything", "1 like", "1 dislike")

	// Replace by re-upload keeps the report's identity, so its votes.
	h.mustUpload(owner, "x", "Report X, revised", "published", reportHTML(500, "x2"))
	if id2, _, _, _ := h.reportRow("x"); id2 != id {
		t.Fatalf("the replace changed the report id %d -> %d", id, id2)
	}
	likes("after a replace", "1 like")
	if res := h.serve("GET", "/reports/x", h.withSID(up)); !strings.Contains(res.Body, "Your vote: thumbs up.") {
		t.Error("after a replace the reader no longer sees their own vote")
	}
	tally("after a replace", "1 like", "1 dislike")

	// Unpublished: the page is gone, a vote is refused, the votes are kept and
	// the author still sees them on the draft.
	h.mustAct(owner, "x", "unpublish")
	if res := h.serve("GET", "/reports/x"); res.Status != 404 {
		t.Fatalf("unpublished report: %d", res.Status)
	}
	if res := h.postVote(up, "x", "vote=down"); res.Status != 404 {
		t.Errorf("a vote on an unpublished report: %d", res.Status)
	}
	if n := h.count("votes"); n != 2 {
		t.Errorf("%d vote rows while unpublished, want both kept", n)
	}
	tally("while unpublished", "1 like", "1 dislike")
	h.mustAct(owner, "x", "publish")
	likes("after republishing", "1 like")
	if id3, _, _, _ := h.reportRow("x"); id3 != id {
		t.Fatalf("unpublish and publish changed the report id %d -> %d", id, id3)
	}

	// Deleted: the votes go with it, and a new report under the same slug starts at zero.
	h.mustAct(owner, "x", "delete")
	if n := h.count("votes"); n != 0 {
		t.Errorf("%d vote rows left after the report was deleted", n)
	}
	h.mustUpload(owner, "x", "Report X again", "published", reportHTML(300, "again"))
	likes("on a new report under the old slug", "0 likes")
	if res := h.serve("GET", "/reports/x", h.withSID(up)); strings.Contains(res.Body, "Your vote") {
		t.Error("a deleted report's vote reappeared on its successor")
	}
}

// observation is one response per route, by who asked.
type observation map[string]result

// dislikeWorld is a site with two reports and the three kinds of non-owner
// observer plus the owner, for the noninterference comparison.
type dislikeWorld struct {
	h                                 *harness
	owner, nonvoter, upvoter, ownerID string
	// first is the id of the seeded top-level comment on x, whose branch page is
	// one of the pages observed.
	first int64
}

func newDislikeWorld(t *testing.T) *dislikeWorld {
	h := newHarness(t, false)
	w := &dislikeWorld{h: h}
	// The owner's session is minted once and used for every owner request.
	w.owner = h.mint(testOwnerID, "the-owner")
	h.mustUpload(w.owner, "x", "Report X", "published", reportHTML(300, "x"))
	h.clock.Advance(time.Hour)
	h.mustUpload(w.owner, "y", "Report Y", "published", reportHTML(300, "y"))
	w.nonvoter = h.mint(7, "nonvoter")
	w.upvoter = h.mint(8, "upvoter")
	h.mustVote(w.upvoter, "x", "up")
	h.mustVote(w.upvoter, "y", "up")
	// A thread on x: a comment, a reply and a comment by the owner. None of the
	// text says "dislike", so the page-wide check below stays meaningful.
	first := h.say(w.nonvoter, "x", "a comment from a reader", 0)
	w.first = first
	h.say(w.upvoter, "x", "a reply from another reader", first)
	h.say(w.owner, "x", "a comment from the owner", 0)
	if page := h.serve("GET", "/reports/x"); !strings.Contains(page.Body, "a reply from another reader") || !strings.Contains(page.Body, badgeMarkup) {
		t.Fatalf("INVALID: the seeded thread is not on the page, so the comparison would test nothing:\n%s", page.Body)
	}
	return w
}

// reader mints a reader and returns the session.
func (w *dislikeWorld) reader(id int64) string {
	return w.h.mint(id, "reader"+strconv.FormatInt(id, 10))
}

// observe records everything the non-owners can receive, and the owner's reader
// pages. Each observer's own vote POST sets the state they already hold, so
// asking changes nothing. The owner's reader pages must really be drawn for the
// owner, or the comparison would pass without testing anything.
func (w *dislikeWorld) observe(t *testing.T) observation {
	t.Helper()
	h, obs := w.h, observation{}
	reads := []string{"/", "/reports/x", "/reports/y", "/reports", "/admin", "/admin/reports/x",
		// The branch page of the seeded comment: another reader page, drawn from the
		// thread, which must never mention a dislike either.
		"/reports/x/comments/" + strconv.FormatInt(w.first, 10)}
	// Each observer also sends a comment POST and a feedback POST that write
	// nothing: a visitor's is answered with the 303 back to the report, and a
	// signed-in reader's empty body with a 400. They also try the owner's reply
	// route, which refuses each of them.
	for _, who := range []struct {
		name, sid, own, comment, feedback string
	}{
		{"anonymous", "", "vote=up", "body=nothing+is+stored", "body=nothing+is+stored"},
		{"nonvoter", w.nonvoter, "vote=none", "body=", "body="},
		{"upvoter", w.upvoter, "vote=up", "body=", "body="},
	} {
		var opts []func(*http.Request)
		if who.sid != "" {
			opts = append(opts, h.withSID(who.sid))
		}
		for _, p := range reads {
			obs[who.name+" GET "+p] = h.serve("GET", p, opts...)
		}
		obs[who.name+" POST /reports/x/vote"] = h.postVote(who.sid, "x", who.own)
		obs[who.name+" POST /reports/x/comments"] = h.postComment(who.sid, "x", who.comment)
		obs[who.name+" POST /reports/x/feedback"] = h.postFeedback(who.sid, "x", who.feedback)
		// The owner's reply route is closed to all three: the same 403 for a message
		// that exists and for one that does not, and nothing written.
		obs[who.name+" POST /admin/feedback/1/reply"] = h.postReply(who.sid, "1", who.feedback)
	}
	for _, p := range []string{"/", "/reports/x", "/reports/y"} {
		res := h.serve("GET", p, h.withSID(w.owner))
		if res.Status != 200 || !strings.Contains(res.Body, `<a href="/admin">Admin</a>`) || !strings.Contains(res.Body, "Signed in as @the-owner") {
			t.Fatalf("INVALID: the owner's GET %s was not drawn for the owner (%d):\n%s", p, res.Status, res.Body)
		}
		obs["owner GET "+p] = res
	}
	return obs
}

// adminTally is the owner's report page: the declassifying sink, which must change.
func (w *dislikeWorld) adminTally(t *testing.T, slug string) string {
	t.Helper()
	res := w.h.serve("GET", "/admin/reports/"+slug, w.h.withSID(w.owner))
	if res.Status != 200 || !hasMarker(res.Body) {
		t.Fatalf("INVALID: the owner's admin page for %s: %d", slug, res.Status)
	}
	return res.Body
}

func sameObservation(t *testing.T, what string, a, b observation) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s: %d observations against %d", what, len(a), len(b))
	}
	for k, ra := range a {
		rb, ok := b[k]
		if !ok {
			t.Errorf("%s: no observation %q", what, k)
			continue
		}
		if d := sameResponse(ra, rb); d != "" {
			t.Errorf("%s: %s differs: %s", what, k, d)
		}
	}
}

// TestDislikesDoNotInterfere compares what a visitor, a non-voting reader, an
// up-voting reader and the owner's own reader pages receive before and after
// other readers cast thumbs down: to anyone but the owner's admin page, the
// answers must be byte-identical. It also compares two worlds that differ only in
// whether a like was switched to a dislike or withdrawn.
func TestDislikesDoNotInterfere(t *testing.T) {
	t.Run("other readers' dislikes", func(t *testing.T) {
		w := newDislikeWorld(t)
		before := w.observe(t)
		adminBefore := w.adminTally(t, "x")
		if !strings.Contains(adminBefore, `<p class="tally">0 dislikes</p>`) || !strings.Contains(adminBefore, `<p class="tally">1 like</p>`) {
			t.Fatalf("the owner's starting tally:\n%s", adminBefore)
		}

		r20, r21, r22 := w.reader(20), w.reader(21), w.reader(22)
		for _, c := range []struct{ sid, slug string }{{r20, "x"}, {r21, "x"}, {r22, "x"}, {r20, "y"}} {
			w.h.mustVote(c.sid, c.slug, "down")
		}
		after := w.observe(t)
		sameObservation(t, "after three dislikes on x and one on y", before, after)

		// Sensitivity: the owner's report page does change, with each number in its label's block.
		adminAfter := w.adminTally(t, "x")
		if !strings.Contains(adminAfter, `<p class="tally">3 dislikes</p>`) || strings.Contains(adminAfter, `0 dislikes`) {
			t.Errorf("the owner's tally did not go from 0 to 3 dislikes:\n%s", adminAfter)
		}
		if y := w.adminTally(t, "y"); !strings.Contains(y, `<p class="tally">1 dislike</p>`) {
			t.Errorf("the owner's tally of y:\n%s", y)
		}
		// Sensitivity: a like, which is public, does change the anonymous page.
		w.h.mustVote(w.reader(23), "x", "up")
		if d := sameResponse(before["anonymous GET /reports/x"], w.h.serve("GET", "/reports/x")); d == "" {
			t.Error("the comparison is blind: an extra like left the anonymous page unchanged")
		}
		// Nothing a reader page says mentions a dislike, whoever the reader is.
		for k, res := range after {
			if strings.Contains(k, "GET /admin") {
				continue
			}
			if strings.Contains(strings.ToLower(res.Body), "dislike") {
				t.Errorf("%s mentions dislikes:\n%s", k, res.Body)
			}
		}
	})

	t.Run("a like switched to a dislike looks like one withdrawn", func(t *testing.T) {
		switched, withdrawn := newDislikeWorld(t), newDislikeWorld(t)
		for _, w := range []*dislikeWorld{switched, withdrawn} {
			w.h.mustVote(w.reader(30), "x", "up")
		}
		mid := switched.observe(t)

		switched.h.mustVote(switched.reader(30), "x", "down")
		withdrawn.h.mustVote(withdrawn.reader(30), "x", "none")
		a, b := switched.observe(t), withdrawn.observe(t)
		sameObservation(t, "switched against withdrawn", a, b)

		// Sensitivity: the like really went away, and the owner can tell the two apart.
		if d := sameResponse(mid["anonymous GET /reports/x"], a["anonymous GET /reports/x"]); d == "" {
			t.Error("the comparison is blind: losing a like left the anonymous page unchanged")
		}
		if !strings.Contains(switched.adminTally(t, "x"), `<p class="tally">1 dislike</p>`) ||
			!strings.Contains(withdrawn.adminTally(t, "x"), `<p class="tally">0 dislikes</p>`) {
			t.Error("the owner's tallies do not tell a switch from a withdrawal")
		}
	})
}
